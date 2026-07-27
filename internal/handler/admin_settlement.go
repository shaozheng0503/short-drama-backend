package handler

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
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

type adminGenerateSettlementsRequest struct {
	Period     string `json:"period"      binding:"required"` // "2026-05"
	DryRun     bool   `json:"dry_run"`                       // true 时只返回预览，不写库
	ContractNo string `json:"contract_no"`                   // 可选：只生成某个合同的结算单
	CreatorID  uint64 `json:"creator_id"`                    // 可选：只生成某个创作者的
	Remark     string `json:"remark"`
}

// adminGenerateSettlements —— POST /v1/admin/settlements/generate
// 财务手动触发月度结算（日常由 cron 自动跑，财务对账时也可手动）。
// 数据源：
//   - creator_stats_daily.income_cents（创作者每日分成实得）
//   - 按 (creator_id, period) 汇总
//
// 输出：每 (creator_id, contract_no, period) 一张结算单。
func (s *Server) adminGenerateSettlements(c *gin.Context) {
	var req adminGenerateSettlementsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "period 必填（YYYY-MM）")
		return
	}
	if _, err := time.Parse("2006-01", req.Period); err != nil {
		response.InvalidParam(c, "period 格式错误（YYYY-MM）")
		return
	}
	// 算 period 范围 [period-01, next-month-01) 字符串比较（stat_date 是 varchar(10)）
	startStr := req.Period + "-01"
	endMonth, _ := time.Parse("2006-01", req.Period)
	endStr := endMonth.AddDate(0, 1, 0).Format("2006-01-02")

	// 先看这段时间内有哪些 creator_id 产生过分成（按 creator 聚合）
	type creatorAgg struct {
		CreatorID    uint64
		ContractNo   string
		IncomeCents  int64
		PlayCount    int64
	}
	var aggs []creatorAgg
	statsQ := s.db.Table("creator_stats_daily").
		Select("creator_id, COALESCE(SUM(income_cents),0) AS income_cents, COALESCE(SUM(play_count),0) AS play_count").
		Where("stat_date >= ? AND stat_date < ?", startStr, endStr)
	if req.CreatorID > 0 {
		statsQ = statsQ.Where("creator_id = ?", req.CreatorID)
	}
	statsQ.Group("creator_id").Scan(&aggs)

	// 合同号（创作者可对应多个合同；本期简化：按 creator 取最新 contract）
	type creatorContract struct {
		CreatorID   uint64
		ContractNo  string
	}
	var ccs []creatorContract
	s.db.Table("contracts").Select("creator_id, contract_no").
		Order("created_at desc").Scan(&ccs)
	contractMap := map[uint64]string{}
	for _, cc := range ccs {
		if _, ok := contractMap[cc.CreatorID]; !ok {
			contractMap[cc.CreatorID] = cc.ContractNo
		}
	}

	// 税率（平台分成已在导入时扣除，此字段为税率）
	creatorShareRate := s.cfg.CreatorShareRate
	if creatorShareRate <= 0 || creatorShareRate > 1 {
		creatorShareRate = 0.7 // 兜底 70%
	}

	previewList := make([]gin.H, 0, len(aggs))
	for _, a := range aggs {
		contractNo := req.ContractNo
		if contractNo == "" {
			contractNo = contractMap[a.CreatorID]
		}
		if contractNo == "" {
			// 跳过无合同的创作者（不能出结算单）
			continue
		}
		// 查重：是否已有该 (creator, period, contract) 的结算单
		var existCount int64
		s.db.Model(&model.Settlement{}).
			Where("creator_id = ? AND period = ? AND contract_no = ?", a.CreatorID, req.Period, contractNo).
			Count(&existCount)
		if existCount > 0 {
			continue
		}
		// 金额拆分：
		//   gross = income / creatorShareRate  （反推总流水）
		//   platform = gross - income
		grossCents := int64(float64(a.IncomeCents) / creatorShareRate)
		platformCents := grossCents - a.IncomeCents
		previewList = append(previewList, gin.H{
			"creator_id":     a.CreatorID,
			"contract_no":    contractNo,
			"gross_cents":    grossCents,
			"tax_cents":      platformCents,
			"net_cents":      a.IncomeCents,
			"play_count":     a.PlayCount,
		})
	}

	if req.DryRun {
		response.OK(c, gin.H{
			"period":            req.Period,
			"dry_run":           true,
			"creator_share_rate": creatorShareRate,
			"count":             len(previewList),
			"preview":           previewList,
		})
		return
	}

	// 真生成
	now := time.Now()
	createdIDs := make([]uint64, 0, len(previewList))
	for _, p := range previewList {
		creatorID := p["creator_id"].(uint64)
		contractNo := p["contract_no"].(string)
		grossCents := p["gross_cents"].(int64)
		platformCents := p["tax_cents"].(int64)
		netCents := p["net_cents"].(int64)
		bizNo := "ST" + now.Format("200601") + "-" + strconv.FormatUint(uint64(now.UnixNano()%10000), 10)
		openedAt := now
		st := model.Settlement{
			SettlementNo:  bizNo,
			CreatorID:     creatorID,
			ContractNo:    contractNo,
			Period:        req.Period,
			GrossCents:    grossCents,
			PlatformCents: platformCents,
			NetCents:      netCents,
			Status:        model.SettlementStatusOpen,
			OpenedAt:      &openedAt,
			Remark:        req.Remark,
		}
		if err := s.db.Transaction(func(tx *gorm.DB) error {
		// 事务内重新查重 + Create，防止并发生成或 cron 并发产生重复结算单
		var existCount int64
		if err := tx.Model(&model.Settlement{}).
			Where("creator_id = ? AND period = ? AND contract_no = ?", creatorID, req.Period, contractNo).
			Count(&existCount).Error; err != nil {
			return err
		}
		if existCount > 0 {
			return nil // 已存在，跳过
		}
			if err := tx.Create(&st).Error; err != nil {
				if isUniqueViolation(err) {
					// settlement_no 唯一索引兜底：并发创建被拦截，跳过
					return nil
				}
				return err
			}
			// 生成 settlement_items：把该 creator 该月的所有 creator_stats_daily 关联到结算单
			var stats []model.CreatorStatsDaily
			tx.Where("creator_id = ? AND stat_date >= ? AND stat_date < ?", creatorID, startStr, endStr).
				Find(&stats)
			for _, stt := range stats {
				// creator_stats_daily.drama_id 有可能是 0（汇总行），跳过
				if stt.DramaID == 0 {
					continue
				}
				// 查 order_no：取该 (creator, drama, stat_date) 时间段内最早的 paid order
				var orderNo string
				var orderID uint64
				dayStart, _ := time.Parse("2006-01-02", stt.StatDate)
				dayEnd := dayStart.AddDate(0, 0, 1)
				tx.Table("orders").Select("id, order_no").
					Where("drama_id = ? AND paid_at >= ? AND paid_at < ? AND status IN ?",
						stt.DramaID, dayStart, dayEnd,
						[]string{model.OrderStatusPaid, model.OrderStatusPartialRefunded}).
					Order("paid_at asc").Limit(1).Row().Scan(&orderID, &orderNo)
				item := model.SettlementItem{
					SettlementID: st.ID,
					OrderID:      orderID,
					DramaID:      stt.DramaID,
					Source:       "self",
					AmountCents:  stt.IncomeCents,
					OrderNo:      orderNo,
					PaidAt:       nil,
				}
				tx.Create(&item)
			}
			createdIDs = append(createdIDs, st.ID)
			return nil
		}); err != nil {
			response.ServerError(c, "生成结算单失败："+err.Error())
			return
		}
	}
	response.OK(c, gin.H{
		"period":            req.Period,
		"dry_run":           false,
		"creator_share_rate": creatorShareRate,
		"count":             len(createdIDs),
		"created_ids":       createdIDs,
	})
}

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
