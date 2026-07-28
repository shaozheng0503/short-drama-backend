package handler

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 0.14.0 删除 Admin 发票列表/详情/审核接口（发票跟提现绑定，通过提现记录查看）

// === Admin 侧：结算单 ===

// adminListSettlements —— GET /v1/admin/settlements
// 财务/超管查看所有结算单（按创作者/月份/状态筛选）。
func (s *Server) adminListSettlements(c *gin.Context) {
	page, pageSize := paginate(c)
	q := s.db.Model(&model.Settlement{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("creator_id"); v != "" {
		if id := parseUint(v); id > 0 {
			q = q.Where("creator_id = ?", id)
		}
	}
	if v := c.Query("period"); v != "" {
		q = q.Where("period = ?", v)
	}
	if v := c.Query("contract_no"); v != "" {
		q = q.Where("contract_no = ?", v)
	}
	if v := c.Query("keyword"); v != "" {
		like := "%" + v + "%"
		q = q.Where("settlement_no LIKE ? OR remark LIKE ?", like, like)
	}
	var total int64
	q.Count(&total)
	var rows []model.Settlement
	q.Order("period desc, id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)

	// 批量查创作者名（避免 N+1）
	creatorIDs := make([]uint64, 0, len(rows))
	for _, r := range rows {
		creatorIDs = append(creatorIDs, r.CreatorID)
	}
	creatorNameMap := map[uint64]string{}
	if len(creatorIDs) > 0 {
		var creators []model.Creator
		s.db.Select("id, name, nickname, org_name").Where("id IN ?", creatorIDs).Find(&creators)
		for _, cr := range creators {
			name := cr.Name
			if cr.OrgName != "" {
				name = cr.OrgName
			}
			if cr.Nickname != "" {
				name = cr.Nickname
			}
			creatorNameMap[cr.ID] = name
		}
	}

	list := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		list = append(list, gin.H{
			"id":            r.ID,
			"settlement_no": r.SettlementNo,
			"creator_id":    r.CreatorID,
			"creator_name":  creatorNameMap[r.CreatorID],
			"cycle_key":     r.CycleKey,
			"status":        r.Status,
			"gross_cents":   r.GrossCents,
			"net_cents":     r.NetCents,
		})
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

// adminGetSettlement —— GET /v1/admin/settlements/:id
func (s *Server) adminGetSettlement(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var st model.Settlement
	if err := s.db.First(&st, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "结算单不存在")
		} else {
			response.ServerError(c, "查询失败")
		}
		return
	}
	// 0.14.0 发票跟提现绑定，结算单不再返回发票
	// 创作者信息
	var cr model.Creator
	s.db.First(&cr, st.CreatorID)
	creatorName := cr.Name
	if cr.Nickname != "" {
		creatorName = cr.Nickname
	}
	// 0.14.0 返回该结算单关联的提现记录（通过 invoice.settlement_id 关联）
	var withdrawals []model.Withdrawal
	s.db.Joins("LEFT JOIN invoices ON invoices.id = withdrawals.invoice_id").
		Where("invoices.settlement_id = ?", st.ID).
		Order("withdrawals.created_at desc").
		Find(&withdrawals)
	wdViews := make([]gin.H, 0, len(withdrawals))
	for _, w := range withdrawals {
		v := gin.H{
			"id":          w.ID,
			"gross_cents": w.AmountCents,
			"net_cents":   w.NetCents,
			"status":      w.Status,
			"created_at":  w.CreatedAt,
			"reviewed_at": w.ReviewedAt,
			"paid_at":     w.PaidAt,
		}
		// 带上发票信息
		if w.InvoiceID != nil {
			var inv model.Invoice
			if err := s.db.First(&inv, *w.InvoiceID).Error; err == nil {
				v["invoice"] = gin.H{
					"invoice_type":     inv.InvoiceType,
					"invoice_file_url": inv.FileURL,
				}
			}
		}
		wdViews = append(wdViews, v)
	}
	response.OK(c, gin.H{
		"id":            st.ID,
		"settlement_no": st.SettlementNo,
		"creator_id":    st.CreatorID,
		"creator_name":  creatorName,
		"creator_phone": cr.Phone,
		"drama_summary": s.settlementDramaSummarySafe(st.ID),
		"creator_party": s.buildCreatorParty(cr, st),
		"withdrawals":   wdViews,
		"period":        st.Period,
		"cycle_key":     st.CycleKey,
		"period_range":  st.PeriodRange,
		"gross_cents":   st.GrossCents,
		"net_cents":     st.NetCents,
		"status":        st.Status,
		"remark":        st.Remark,
		"opened_at":     st.OpenedAt,
		"closed_at":     st.ClosedAt,
		"created_at":    st.CreatedAt,
	})
}

// adminGenerateSettlements —— 已废弃，2026-07-28 邱嘉诚要求删除
// 保留注释标记位置

type adminCloseSettlementRequest struct {
	Action string `json:"action" binding:"required"` // "mark_paid" / "void"
	Remark string `json:"remark"`
}

// adminCloseSettlement —— POST /v1/admin/settlements/:id/close
// 财务手动关账：
//   - mark_paid：结算单 → paid（已打款）；同时把关联 invoice（approved）的 settlement 标 paid
//   - void：作废（一般不出现，预留）
func (s *Server) adminCloseSettlement(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var req adminCloseSettlementRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "action 必填（mark_paid/void）")
		return
	}
	var newStatus string
	switch req.Action {
	case "mark_paid":
		newStatus = model.SettlementStatusPaid
	case "void":
		newStatus = model.SettlementStatusVoid
	default:
		response.InvalidParam(c, "action 只能是 mark_paid / void")
		return
	}
	now := time.Now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 行锁结算单，防止并发关账（与 adminConfirmDistributorSettlement 对称）
		var st model.Settlement
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&st, id).Error; err != nil {
			return err
		}
		if st.Status == model.SettlementStatusPaid {
			return fmt.Errorf("结算单已 paid，不能重复关账")
		}
		if st.Status == model.SettlementStatusVoid {
			return fmt.Errorf("结算单已 void，不能关账")
		}
		// void 安全检查：如果结算单已进入 invoiced 阶段（创作者已发起提现，
		// 余额已从 balance 扣到 frozen），直接 void 会导致冻结余额永久无法释放。
		// 必须先驳回关联提现（退回冻结→余额）再 void。
		if req.Action == "void" && st.Status == model.SettlementStatusInvoiced {
			var activeWithdrawalCount int64
			tx.Model(&model.Withdrawal{}).
				Where("invoice_id IN (?) AND status IN ?",
					tx.Model(&model.Invoice{}).Select("id").Where("settlement_id = ?", id),
					[]string{model.WithdrawalStatusPending, model.WithdrawalStatusApproved},
				).Count(&activeWithdrawalCount)
			if activeWithdrawalCount > 0 {
				return fmt.Errorf("结算单已关联 %d 笔活跃提现（pending/approved），void 前请先驳回关联提现以退回冻结余额", activeWithdrawalCount)
			}
		}
		updates := map[string]interface{}{
			"status":     newStatus,
			"closed_at":  now,
		}
		if req.Remark != "" {
			updates["remark"] = req.Remark
		}
		return tx.Model(&st).Updates(updates).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.NotFound(c, "结算单不存在")
		} else {
			response.Conflict(c, err.Error())
		}
		return
	}
	response.OK(c, gin.H{"id": id, "status": newStatus, "closed_at": now})
}

// adminDownloadSettlementPDF —— GET /v1/admin/settlements/:id/download.pdf
// 财务下载任意创作者结算单的 PDF 对账单（与创作者侧版式一致，便于存档/对账）。
func (s *Server) adminDownloadSettlementPDF(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var st model.Settlement
	if err := s.db.First(&st, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "结算单不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	var items []model.SettlementItem
	s.db.Where("settlement_id = ?", st.ID).Order("paid_at asc, id asc").Find(&items)

	var buf bytes.Buffer
	if err := s.renderSettlementPDF(st, items, s.platformCompanyFromConfig(), &buf); err != nil {
		log.Printf("[settlement-pdf admin] render err id=%d err=%v", id, err)
		response.ServerError(c, "生成 PDF 失败")
		return
	}
	filename := fmt.Sprintf("settlement_%s_%s.pdf", st.SettlementNo, time.Now().Format("20060102"))
	c.Header("Content-Type", "application/pdf")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Data(http.StatusOK, "application/pdf", buf.Bytes())
}
