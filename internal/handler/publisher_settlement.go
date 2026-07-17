package handler

import (
	"fmt"
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// GET /v1/publisher/settlements/summary —— 收益结算中心汇总
func (s *Server) publisherSettlementSummary(c *gin.Context) {
	id := middleware.CurrentID(c)
	var d model.Distributor
	if err := s.db.Select("total_income_cents, balance_cents, deposit_deducted_cents").First(&d, id).Error; err != nil {
		response.NotFound(c, "发行商不存在")
		return
	}

	// 未结清应付合计 = 所有非 settled 状态结算单的 payable_cents 之和
	var outstandingPayable int64
	s.db.Model(&model.DistributorSettlement{}).
		Where("distributor_id = ? AND status IN ?", id, []string{
			model.DistSettlementPendingPayment, model.DistSettlementPaymentSubmitted,
		}).
		Select("COALESCE(SUM(payable_cents),0)").Scan(&outstandingPayable)

	response.OK(c, gin.H{
		"total_income_cents":          d.TotalIncomeCents,
		"deducted_deposit_cents":      d.DepositDeductedCents,
		"outstanding_payable_cents":   outstandingPayable,
	})
}

// GET /v1/publisher/settlements —— 结算单列表
func (s *Server) publisherListSettlements(c *gin.Context) {
	id := middleware.CurrentID(c)
	page, pageSize := paginate(c)
	q := s.db.Model(&model.DistributorSettlement{}).Where("distributor_id = ?", id)
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	var total int64
	q.Count(&total)
	var items []model.DistributorSettlement
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items)

	list := make([]gin.H, 0, len(items))
	for _, r := range items {
		list = append(list, gin.H{
			"id":                     r.ID,
			"settlement_no":          r.SettlementNo,
			"cycle_key":              r.CycleKey,
			"period_range":           r.PeriodRange,
			"status":                 r.Status,
			"gross_cents":            r.GrossCents,
			"deducted_deposit_cents": r.DeductedDepositCents,
			"payable_cents":          r.PayableCents,
			"transaction_no":         r.TransactionNo,
			"payment_submitted_at":   r.PaymentSubmittedAt,
			"receipt_confirmed_at":   r.ReceiptConfirmedAt,
			"receipt_reject_reason":  r.ReceiptRejectReason,
			"created_at":             r.CreatedAt,
		})
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

// GET /v1/publisher/settlements/:id —— 结算单详情
func (s *Server) publisherGetSettlement(c *gin.Context) {
	id := middleware.CurrentID(c)
	sid := parseUint(c.Param("id"))
	var st model.DistributorSettlement
	if err := s.db.Where("id = ? AND distributor_id = ?", sid, id).First(&st).Error; err != nil {
		response.NotFound(c, "结算单不存在")
		return
	}

	var dist model.Distributor
	s.db.First(&dist, id)

	// 剧集收益汇总
	type dramaIncomeRow struct {
		DramaID     uint64
		IncomeCents int64
	}
	startDate := ""
	endDate := ""
	if len(st.PeriodRange) >= 21 {
		startDate = st.PeriodRange[:10]
		endDate = st.PeriodRange[len(st.PeriodRange)-10:]
	}

	var dramaRows []dramaIncomeRow
	q := s.db.Table("distributor_income_daily").
		Select("drama_id, COALESCE(SUM(income_cents),0) as income_cents").
		Where("distributor_id = ?", id)
	if startDate != "" && endDate != "" {
		q = q.Where("stat_date >= ? AND stat_date <= ?", startDate, endDate)
	}
	q.Group("drama_id").Scan(&dramaRows)

	dramaSummary := make([]gin.H, 0, len(dramaRows))
	for _, r := range dramaRows {
		var title string
		s.db.Table("dramas").Select("title").Where("id = ?", r.DramaID).Scan(&title)
		dramaSummary = append(dramaSummary, gin.H{
			"drama_id":     r.DramaID,
			"drama_title":  title,
			"income_cents": r.IncomeCents,
		})
	}

	v := s.distributorSettlementDetailView(&st, &dist)
	v["drama_summary"] = dramaSummary
	response.OK(c, v)
}

// POST /v1/publisher/settlements/:id/remittance —— 发行商提交已打款信息
func (s *Server) publisherSubmitRemittance(c *gin.Context) {
	id := middleware.CurrentID(c)
	sid := parseUint(c.Param("id"))

	var req struct {
		TransactionNo  string `json:"transaction_no" binding:"required"`
		PaidAt          string `json:"paid_at" binding:"required"`
		ProofFileKey   string `json:"proof_file_key"`
		Remark         string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "transaction_no 和 paid_at 必填")
		return
	}

	paidAt, err := time.Parse(time.RFC3339, req.PaidAt)
	if err != nil {
		response.InvalidParam(c, "paid_at 格式错误（需 RFC3339，如 2026-07-16T15:00:00+08:00）")
		return
	}

	now := time.Now()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var st model.DistributorSettlement
		if err := tx.Where("id = ? AND distributor_id = ?", sid, id).First(&st).Error; err != nil {
			return fmt.Errorf("结算单不存在")
		}
		if st.Status != model.DistSettlementPendingPayment {
			return fmt.Errorf("仅待打款状态可提交，当前状态: %s", st.Status)
		}
		return tx.Model(&st).Updates(map[string]interface{}{
			"status":                  model.DistSettlementPaymentSubmitted,
			"transaction_no":          req.TransactionNo,
			"paid_at":                 paidAt,
			"payment_proof_file_key":  req.ProofFileKey,
			"payment_remark":          req.Remark,
			"payment_submitted_at":    now,
			"receipt_reject_reason":   "",
		}).Error
	})
	if err != nil {
		response.Conflict(c, err.Error())
		return
	}

	var st model.DistributorSettlement
	s.db.First(&st, sid)
	response.OK(c, gin.H{
		"id":     sid,
		"status": st.Status,
	})
}

// ============================================================
// 提现（已废弃，保留只读接口）
// ============================================================

// GET /v1/publisher/settlements/:id/withdrawal-preview —— 提现预览（废弃）
func (s *Server) publisherWithdrawalPreview(c *gin.Context) {
	response.Conflict(c, "提现功能已下线，请使用结算单打款流程")
}

// POST /v1/publisher/withdrawals —— 提交提现申请（废弃）
func (s *Server) publisherCreateWithdrawal(c *gin.Context) {
	response.Conflict(c, "提现功能已下线，请使用结算单打款流程")
}

// GET /v1/publisher/withdrawals —— 提现记录列表
func (s *Server) publisherListWithdrawals(c *gin.Context) {
	id := middleware.CurrentID(c)
	page, pageSize := paginate(c)
	q := s.db.Model(&model.DistributorWithdrawal{}).Where("distributor_id = ?", id)
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	var total int64
	q.Count(&total)
	var items []model.DistributorWithdrawal
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items)

	// 批量查结算单号
	sIDs := make([]uint64, 0, len(items))
	for _, w := range items {
		sIDs = append(sIDs, w.SettlementID)
	}
	stMap := map[uint64]model.DistributorSettlement{}
	if len(sIDs) > 0 {
		var sts []model.DistributorSettlement
		s.db.Where("id IN ?", sIDs).Find(&sts)
		for _, st := range sts {
			stMap[st.ID] = st
		}
	}

	list := make([]gin.H, 0, len(items))
	for _, w := range items {
		// processed_at = paid_at 或 reviewed_at（取有值的最后一个）
		var processedAt *time.Time
		if w.PaidAt != nil {
			processedAt = w.PaidAt
		} else if w.ReviewedAt != nil {
			processedAt = w.ReviewedAt
		}
		v := gin.H{
			"id":            w.ID,
			"withdrawal_no": w.WithdrawalNo,
			"amount_cents":  w.AmountCents,
			"status":        w.Status,
			"created_at":    w.CreatedAt,
			"reviewed_at":   w.ReviewedAt,
			"paid_at":       w.PaidAt,
			"processed_at":  processedAt,
		}
		if st, ok := stMap[w.SettlementID]; ok {
			v["settlement_no"] = st.SettlementNo
			v["cycle_key"] = st.CycleKey
		}
		list = append(list, v)
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

// GET /v1/publisher/withdrawals/:id —— 提现记录详情
func (s *Server) publisherGetWithdrawal(c *gin.Context) {
	id := middleware.CurrentID(c)
	wdID := parseUint(c.Param("id"))
	var w model.DistributorWithdrawal
	if err := s.db.Where("id = ? AND distributor_id = ?", wdID, id).First(&w).Error; err != nil {
		response.NotFound(c, "提现记录不存在")
		return
	}

	var st model.DistributorSettlement
	s.db.First(&st, w.SettlementID)

	var d model.Distributor
	s.db.First(&d, id)

	// processed_at = paid_at 或 reviewed_at
	var processedAt *time.Time
	if w.PaidAt != nil {
		processedAt = w.PaidAt
	} else if w.ReviewedAt != nil {
		processedAt = w.ReviewedAt
	}

	v := gin.H{
		"id":             w.ID,
		"withdrawal_no":  w.WithdrawalNo,
		"settlement_id":  w.SettlementID,
		"settlement_no":  st.SettlementNo,
		"cycle_key":      st.CycleKey,
		"amount_cents":   w.AmountCents,
		"status":         w.Status,
		"created_at":     w.CreatedAt,
		"reviewed_at":    w.ReviewedAt,
		"paid_at":        w.PaidAt,
		"processed_at":   processedAt,
		"remark":         w.Remark,
		"transaction_no": w.TransactionNo,
		"creator_party": gin.H{
			"bank_name":      d.BankName,
			"bank_no_masked": d.BankCardNoMasked,
			"creator_name":   distributorName(&d),
		},
	}

	// 发票
	if w.InvoiceID != nil {
		var inv model.DistributorInvoice
		if err := s.db.First(&inv, *w.InvoiceID).Error; err == nil {
			v["invoice"] = gin.H{
				"invoice_type":     inv.InvoiceType,
				"invoice_file_url": inv.FileURL,
			}
		}
	}

	response.OK(c, v)
}
