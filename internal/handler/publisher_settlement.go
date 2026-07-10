package handler

import (
	"fmt"
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GET /v1/publisher/settlements/summary —— 收益结算中心汇总
func (s *Server) publisherSettlementSummary(c *gin.Context) {
	id := middleware.CurrentID(c)
	var d model.Distributor
	if err := s.db.Select("total_income_cents, balance_cents, deposit_deducted_cents").First(&d, id).Error; err != nil {
		response.NotFound(c, "发行商不存在")
		return
	}
	response.OK(c, gin.H{
		"total_income_cents":      d.TotalIncomeCents,
		"deducted_deposit_cents":  d.DepositDeductedCents,
		"withdrawable_cents":      d.BalanceCents,
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
			"id":                      r.ID,
			"settlement_no":           r.SettlementNo,
			"cycle_key":               r.CycleKey,
			"status":                  r.Status,
			"gross_cents":             r.GrossCents,
			"deducted_deposit_cents":  r.DeductedDepositCents,
			"withdrawable_cents":      r.WithdrawableCents,
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

	// 剧集收益汇总
	type dramaIncomeRow struct {
		DramaID     uint64
		DramaTitle  string
		IncomeCents int64
	}
	var dramaRows []dramaIncomeRow
	s.db.Table("distributor_income_daily").
		Select("drama_id, '' as drama_title, COALESCE(SUM(income_cents),0) as income_cents").
		Where("distributor_id = ? AND stat_date >= ? AND stat_date <= ?",
			id, st.PeriodRange[:10], st.PeriodRange[len(st.PeriodRange)-10:]).
		Group("drama_id").Scan(&dramaRows)
	// 查剧名
	for i, r := range dramaRows {
		var title string
		s.db.Table("dramas").Select("title").Where("id = ?", r.DramaID).Scan(&title)
		dramaRows[i].DramaTitle = title
	}
	dramaSummary := make([]gin.H, 0, len(dramaRows))
	for _, r := range dramaRows {
		dramaSummary = append(dramaSummary, gin.H{
			"drama_id":     r.DramaID,
			"drama_title":  r.DramaTitle,
			"income_cents": r.IncomeCents,
		})
	}

	// 提现记录
	var withdrawals []model.DistributorWithdrawal
	s.db.Where("settlement_id = ?", st.ID).Find(&withdrawals)
	wdViews := make([]gin.H, 0, len(withdrawals))
	for _, w := range withdrawals {
		wdViews = append(wdViews, gin.H{
			"id":           w.ID,
			"withdrawal_no": w.WithdrawalNo,
			"amount_cents": w.AmountCents,
			"status":       w.Status,
			"created_at":   w.CreatedAt,
			"paid_at":      w.PaidAt,
		})
	}

	response.OK(c, gin.H{
		"id":                     st.ID,
		"settlement_no":          st.SettlementNo,
		"cycle_key":              st.CycleKey,
		"period_range":           st.PeriodRange,
		"status":                 st.Status,
		"gross_cents":            st.GrossCents,
		"platform_cents":         st.PlatformCents,
		"net_cents":              st.NetCents,
		"deducted_deposit_cents": st.DeductedDepositCents,
		"withdrawable_cents":     st.WithdrawableCents,
		"drama_summary":          dramaSummary,
		"withdrawals":            wdViews,
		"created_at":             st.CreatedAt,
	})
}

// GET /v1/publisher/settlements/:id/withdrawal-preview —— 提现预览
func (s *Server) publisherWithdrawalPreview(c *gin.Context) {
	id := middleware.CurrentID(c)
	sid := parseUint(c.Param("id"))
	var st model.DistributorSettlement
	if err := s.db.Where("id = ? AND distributor_id = ?", sid, id).First(&st).Error; err != nil {
		response.NotFound(c, "结算单不存在")
		return
	}

	// 查发行商银行信息
	var d model.Distributor
	s.db.Select("bank_name, bank_card_no_masked, org_name, name, verify_status").First(&d, id)

	// 查是否已有 pending 提现
	var existingCount int64
	s.db.Model(&model.DistributorWithdrawal{}).Where("settlement_id = ? AND status = ?", st.ID, "pending").Count(&existingCount)

	creatorName := distributorName(&d)

	response.OK(c, gin.H{
		"settlement_id":      st.ID,
		"settlement_no":      st.SettlementNo,
		"cycle_key":          st.CycleKey,
		"withdrawable_cents": st.WithdrawableCents,
		"bank_account": gin.H{
			"bank_name":       d.BankName,
			"bank_no_masked":  d.BankCardNoMasked,
			"creator_name":    creatorName,
		},
		"invoice_required":  d.VerifyStatus == model.DistributorVerifyVerified, // 企业认证需发票
		"already_pending":   existingCount > 0,
	})
}

// ============================================================
// 提现
// ============================================================

type publisherWithdrawalRequest struct {
	SettlementID   uint64 `json:"settlement_id" binding:"required"`
	InvoiceFileURL string `json:"invoice_file_url"`
	InvoiceType    string `json:"invoice_type"`
}

// POST /v1/publisher/withdrawals —— 提交提现申请
func (s *Server) publisherCreateWithdrawal(c *gin.Context) {
	id := middleware.CurrentID(c)
	var req publisherWithdrawalRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "settlement_id 必填")
		return
	}

	var st model.DistributorSettlement
	if err := s.db.Where("id = ? AND distributor_id = ?", req.SettlementID, id).First(&st).Error; err != nil {
		response.NotFound(c, "结算单不存在")
		return
	}
	if st.Status != "open" && st.Status != "invoiced" {
		response.Conflict(c, "结算单状态不可提现")
		return
	}
	if st.WithdrawableCents <= 0 {
		response.Conflict(c, "可提现金额为 0")
		return
	}

	// 检查是否已有 pending
	var existingCount int64
	s.db.Model(&model.DistributorWithdrawal{}).Where("settlement_id = ? AND status = ?", st.ID, "pending").Count(&existingCount)
	if existingCount > 0 {
		response.Conflict(c, "该结算单已有待处理的提现申请")
		return
	}

	var d model.Distributor
	s.db.First(&d, id)

	// 创建提现 + 扣余额
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var dist model.Distributor
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&dist, id).Error; err != nil {
			return err
		}
		if dist.BalanceCents < st.WithdrawableCents {
			return fmt.Errorf("可提现余额不足")
		}
		dist.BalanceCents -= st.WithdrawableCents
		dist.FrozenCents += st.WithdrawableCents
		if err := tx.Save(&dist).Error; err != nil {
			return err
		}

		wd := model.DistributorWithdrawal{
			WithdrawalNo:       fmt.Sprintf("WD%06d", time.Now().UnixMilli()%1000000),
			DistributorID:      id,
			SettlementID:       st.ID,
			AmountCents:        st.WithdrawableCents,
			BankNameSnapshot:   dist.BankName,
			BankCardNoSnapshot: dist.BankCardNoMasked,
			Status:             "pending",
		}

		// 发票（企业必传）
		if d.VerifyStatus == model.DistributorVerifyVerified && req.InvoiceFileURL != "" {
			inv := model.DistributorInvoice{
				InvoiceNo:    fmt.Sprintf("INV%06d", time.Now().UnixMilli()%1000000),
				SettlementID: st.ID,
				DistributorID: id,
				InvoiceType:  req.InvoiceType,
				AmountCents:  st.WithdrawableCents,
				FileURL:      req.InvoiceFileURL,
				Status:       "pending",
			}
			if err := tx.Create(&inv).Error; err != nil {
				return err
			}
			wd.InvoiceID = &inv.ID
		}

		return tx.Create(&wd).Error
	})
	if err != nil {
		response.Conflict(c, err.Error())
		return
	}

	// 更新结算单状态
	s.db.Model(&st).Update("status", "invoiced")

	response.OK(c, gin.H{"status": "pending", "message": "提现申请已提交"})
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
		v := gin.H{
			"id":             w.ID,
			"withdrawal_no":  w.WithdrawalNo,
			"amount_cents":   w.AmountCents,
			"status":         w.Status,
			"created_at":     w.CreatedAt,
			"reviewed_at":    w.ReviewedAt,
			"paid_at":        w.PaidAt,
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
