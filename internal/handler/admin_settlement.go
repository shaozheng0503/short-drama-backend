package handler

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// === Admin 侧：发票审核 ===

// adminListInvoices —— GET /v1/admin/invoices
// 财务/超管查看发票列表（按状态/创作者/结算单筛选）。
func (s *Server) adminListInvoices(c *gin.Context) {
	page, pageSize := paginate(c)
	q := s.db.Model(&model.Invoice{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("creator_id"); v != "" {
		if id := parseUint(v); id > 0 {
			q = q.Where("creator_id = ?", id)
		}
	}
	if v := c.Query("settlement_id"); v != "" {
		if id := parseUint(v); id > 0 {
			q = q.Where("settlement_id = ?", id)
		}
	}
	if v := c.Query("settlement_no"); v != "" {
		// 支持按结算单号搜索
		q = q.Joins("LEFT JOIN settlements st ON st.id = invoices.settlement_id").
			Where("st.settlement_no = ?", v)
	}
	var total int64
	q.Count(&total)
	var rows []model.Invoice
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)
	list := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		list = append(list, gin.H{
			"id":            r.ID,
			"invoice_no":    r.InvoiceNo,
			"settlement_id": r.SettlementID,
			"creator_id":    r.CreatorID,
			"invoice_type":  r.InvoiceType,
			"external_no":   r.ExternalNo,
			"amount_cents":  r.AmountCents,
			"file_url":      r.FileURL,
			"file_hash":     r.FileHash,
			"file_size":     r.FileSize,
			"status":        r.Status,
			"reject_reason": r.RejectReason,
			"reviewed_by":   r.ReviewedBy,
			"reviewed_at":   r.ReviewedAt,
			"created_at":    r.CreatedAt,
			"updated_at":    r.UpdatedAt,
		})
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

// adminGetInvoice —— GET /v1/admin/invoices/:id
func (s *Server) adminGetInvoice(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var inv model.Invoice
	if err := s.db.First(&inv, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "发票不存在")
		} else {
			response.ServerError(c, "查询失败")
		}
		return
	}
	// 顺带返结算单 + 创作者名
	var st model.Settlement
	s.db.First(&st, inv.SettlementID)
	var cr model.Creator
	s.db.First(&cr, inv.CreatorID)
	creatorName := cr.Name
	if cr.Nickname != "" {
		creatorName = cr.Nickname
	}
	creatorPhone := cr.Phone
	response.OK(c, gin.H{
		"id":            inv.ID,
		"invoice_no":    inv.InvoiceNo,
		"settlement_id": inv.SettlementID,
		"settlement_no": st.SettlementNo,
		"creator_id":    inv.CreatorID,
		"creator_name":  creatorName,
		"creator_phone": creatorPhone,
		"invoice_type":  inv.InvoiceType,
		"external_no":   inv.ExternalNo,
		"amount_cents":  inv.AmountCents,
		"file_url":      inv.FileURL,
		"file_hash":     inv.FileHash,
		"file_size":     inv.FileSize,
		"status":        inv.Status,
		"reject_reason": inv.RejectReason,
		"reviewed_by":   inv.ReviewedBy,
		"reviewed_at":   inv.ReviewedAt,
		"created_at":    inv.CreatedAt,
		"updated_at":    inv.UpdatedAt,
	})
}

type adminInvoiceApproveRequest struct {
	Remark string `json:"remark"` // 可选备注
}

// adminApproveInvoice —— POST /v1/admin/invoices/:id/approve
// 财务审核通过。结算单状态保持 invoiced（创作者继续走提现）。
func (s *Server) adminApproveInvoice(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var req adminInvoiceApproveRequest
	_ = c.ShouldBindJSON(&req)

	reviewerID := middleware.CurrentID(c)
	now := time.Now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var inv model.Invoice
		if err := tx.First(&inv, id).Error; err != nil {
			return err
		}
		if inv.Status != model.InvoiceStatusPending {
			return fmt.Errorf("仅待审核的发票可审核通过，当前状态：%s", inv.Status)
		}
		return tx.Model(&inv).Updates(map[string]interface{}{
			"status":      model.InvoiceStatusApproved,
			"reviewed_by": reviewerID,
			"reviewed_at": now,
		}).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.NotFound(c, "发票不存在")
		} else {
			response.Conflict(c, err.Error())
		}
		return
	}
	response.OK(c, gin.H{"id": id, "status": model.InvoiceStatusApproved, "reviewed_at": now})

	// 2026-07-06 加 P1-5：时间线
	s.recordTransition("invoice", id, model.InvoiceStatusPending, model.InvoiceStatusApproved, "admin", &reviewerID, "财务审核通过发票", nil)
}

type adminInvoiceRejectRequest struct {
	Reason string `json:"reason" binding:"required"`
}

// adminRejectInvoice —— POST /v1/admin/invoices/:id/reject
// 财务审核驳回（必须带原因）。创作者可重传新发票。
func (s *Server) adminRejectInvoice(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var req adminInvoiceRejectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "reason 必填")
		return
	}
	reviewerID := middleware.CurrentID(c)
	now := time.Now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var inv model.Invoice
		if err := tx.First(&inv, id).Error; err != nil {
			return err
		}
		if inv.Status != model.InvoiceStatusPending {
			return fmt.Errorf("仅待审核的发票可驳回，当前状态：%s", inv.Status)
		}
		return tx.Model(&inv).Updates(map[string]interface{}{
			"status":        model.InvoiceStatusRejected,
			"reject_reason": req.Reason,
			"reviewed_by":   reviewerID,
			"reviewed_at":   now,
		}).Error
	})
	if err != nil {
		if err.Error() == "record not found" {
			response.NotFound(c, "发票不存在")
		} else {
			response.Conflict(c, err.Error())
		}
		return
	}
	response.OK(c, gin.H{"id": id, "status": model.InvoiceStatusRejected, "reject_reason": req.Reason})

	// 2026-07-06 加 P1-5：时间线
	s.recordTransition("invoice", id, model.InvoiceStatusPending, model.InvoiceStatusRejected, "admin", &reviewerID, "财务审核驳回发票", map[string]interface{}{
		"reject_reason": req.Reason,
	})
}

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
	list := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		list = append(list, gin.H{
			"id":             r.ID,
			"settlement_no":  r.SettlementNo,
			"creator_id":     r.CreatorID,
			"contract_no":    r.ContractNo,
			"period":         r.Period,
			"gross_cents":    r.GrossCents,
			"platform_cents": r.PlatformCents,
			"net_cents":      r.NetCents,
			"status":         r.Status,
			"remark":         r.Remark,
			"opened_at":      r.OpenedAt,
			"closed_at":      r.ClosedAt,
			"created_at":     r.CreatedAt,
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
	var items []model.SettlementItem
	s.db.Where("settlement_id = ?", st.ID).Order("paid_at asc, id asc").Find(&items)
	itemViews := make([]gin.H, 0, len(items))
	for _, it := range items {
		itemViews = append(itemViews, gin.H{
			"id":           it.ID,
			"order_id":     it.OrderID,
			"order_no":     it.OrderNo,
			"drama_id":     it.DramaID,
			"source":       it.Source,
			"amount_cents": it.AmountCents,
			"paid_at":      it.PaidAt,
		})
	}
	var invoices []model.Invoice
	s.db.Where("settlement_id = ?", st.ID).Order("created_at desc").Find(&invoices)
	invViews := make([]gin.H, 0, len(invoices))
	approvedSum := int64(0)
	for _, inv := range invoices {
		invViews = append(invViews, gin.H{
			"id":            inv.ID,
			"invoice_no":    inv.InvoiceNo,
			"invoice_type":  inv.InvoiceType,
			"external_no":   inv.ExternalNo,
			"amount_cents":  inv.AmountCents,
			"file_url":      inv.FileURL,
			"status":        inv.Status,
			"reject_reason": inv.RejectReason,
			"created_at":    inv.CreatedAt,
		})
		if inv.Status == model.InvoiceStatusApproved {
			approvedSum += inv.AmountCents
		}
	}
	// 创作者信息
	var cr model.Creator
	s.db.First(&cr, st.CreatorID)
	creatorName := cr.Name
	if cr.Nickname != "" {
		creatorName = cr.Nickname
	}
	response.OK(c, gin.H{
		"id":                     st.ID,
		"settlement_no":          st.SettlementNo,
		"creator_id":             st.CreatorID,
		"creator_name":           creatorName,
		"creator_phone":          cr.Phone,
		"contract_no":            st.ContractNo,
		"contracts":              s.settlementContracts(st.ID), // 2026-07-07 加：关联合同列表
		"period":                 st.Period,
		"gross_cents":            st.GrossCents,
		"platform_cents":         st.PlatformCents,
		"net_cents":              st.NetCents,
		"status":                 st.Status,
		"approved_invoice_cents": approvedSum,
		"items":                  itemViews,
		"invoices":               invViews,
		"remark":                 st.Remark,
		"opened_at":              st.OpenedAt,
		"closed_at":              st.ClosedAt,
		"created_at":             st.CreatedAt,
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

	// 平台抽成比例：直接读 .env 的 CreatorShareRate
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
			"platform_cents": platformCents,
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
		platformCents := p["platform_cents"].(int64)
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
		if err := s.db.Create(&st).Error; err != nil {
			response.ServerError(c, "生成结算单失败："+err.Error())
			return
		}
		// 生成 settlement_items：把该 creator 该月的所有 creator_stats_daily 关联到结算单
		var stats []model.CreatorStatsDaily
		s.db.Where("creator_id = ? AND stat_date >= ? AND stat_date < ?", creatorID, startStr, endStr).
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
			s.db.Table("orders").Select("id, order_no").
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
			s.db.Create(&item)
		}
		createdIDs = append(createdIDs, st.ID)
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
		var st model.Settlement
		if err := tx.First(&st, id).Error; err != nil {
			return err
		}
		if st.Status == model.SettlementStatusPaid {
			return fmt.Errorf("结算单已 paid，不能重复关账")
		}
		if st.Status == model.SettlementStatusVoid {
			return fmt.Errorf("结算单已 void，不能关账")
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
