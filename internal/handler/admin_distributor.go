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

// ============================================================
// Admin 发行商管理（0.15.0）
// ============================================================

// GET /admin/distributors —— 发行商列表
func (s *Server) adminListDistributors(c *gin.Context) {
	page, pageSize := paginate(c)
	q := s.db.Model(&model.Distributor{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("verify_status"); v != "" {
		q = q.Where("verify_status = ?", v)
	}
	if v := c.Query("keyword"); v != "" {
		q = q.Where("phone ILIKE ? OR name ILIKE ? OR org_name ILIKE ?", "%"+v+"%", "%"+v+"%", "%"+v+"%")
	}
	var total int64
	q.Count(&total)
	var items []model.Distributor
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items)

	list := make([]gin.H, 0, len(items))
	for _, d := range items {
		list = append(list, gin.H{
			"id":                     d.ID,
			"phone":                  d.Phone,
			"name":                   distributorName(&d),
			"org_name":               d.OrgName,
			"verify_status":          d.VerifyStatus,
			"status":                 d.Status,
			"deposit_available_cents": d.DepositAvailableCents,
			"deposit_frozen_cents":   d.DepositFrozenCents,
			"balance_cents":          d.BalanceCents,
			"created_at":             d.CreatedAt,
		})
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

// GET /admin/distributors/:id —— 发行商详情
func (s *Server) adminGetDistributor(c *gin.Context) {
	id := parseUint(c.Param("id"))
	var d model.Distributor
	if err := s.db.First(&d, id).Error; err != nil {
		response.NotFound(c, "发行商不存在")
		return
	}
	response.OK(c, distributorDetailView(&d))
}

// POST /admin/distributors/:id/verification/approve —— 认证通过
func (s *Server) adminApproveDistributorVerification(c *gin.Context) {
	id := parseUint(c.Param("id"))
	var d model.Distributor
	if err := s.db.First(&d, id).Error; err != nil {
		response.NotFound(c, "发行商不存在")
		return
	}
	if d.VerifyStatus != model.DistributorVerifyPending {
		response.Conflict(c, "仅待审核的发行商可审核通过")
		return
	}
	now := time.Now()
	s.db.Model(&d).Updates(map[string]interface{}{
		"verify_status":    model.DistributorVerifyVerified,
		"verify_checked_at": now,
		"verify_reject_reason": "",
		"verify_reject_fields": "",
	})
	response.OK(c, gin.H{"id": id, "verify_status": model.DistributorVerifyVerified})
}

// POST /admin/distributors/:id/verification/reject —— 认证驳回
func (s *Server) adminRejectDistributorVerification(c *gin.Context) {
	id := parseUint(c.Param("id"))
	var req struct {
		Reason string `json:"reason" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "reason 必填")
		return
	}
	var d model.Distributor
	if err := s.db.First(&d, id).Error; err != nil {
		response.NotFound(c, "发行商不存在")
		return
	}
	if d.VerifyStatus != model.DistributorVerifyPending {
		response.Conflict(c, "仅待审核的发行商可驳回")
		return
	}
	now := time.Now()
	s.db.Model(&d).Updates(map[string]interface{}{
		"verify_status":      model.DistributorVerifyRejected,
		"verify_reject_reason": req.Reason,
		"verify_checked_at":  now,
	})
	response.OK(c, gin.H{"id": id, "verify_status": model.DistributorVerifyRejected})
}

// POST /admin/distributors/:id/ban —— 封禁
func (s *Server) adminBanDistributor(c *gin.Context) {
	id := parseUint(c.Param("id"))
	s.db.Model(&model.Distributor{}).Where("id = ?", id).Update("status", model.StatusBanned)
	response.OK(c, gin.H{"id": id, "status": model.StatusBanned})
}

// POST /admin/distributors/:id/unban —— 解封
func (s *Server) adminUnbanDistributor(c *gin.Context) {
	id := parseUint(c.Param("id"))
	s.db.Model(&model.Distributor{}).Where("id = ?", id).Update("status", model.StatusActive)
	response.OK(c, gin.H{"id": id, "status": model.StatusActive})
}

// ============================================================
// Admin 认领审核
// ============================================================

// GET /admin/distributor-claims —— 认领申请列表
func (s *Server) adminListDistributorClaims(c *gin.Context) {
	page, pageSize := paginate(c)
	q := s.db.Model(&model.DistributorApplication{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	var total int64
	q.Count(&total)
	var items []model.DistributorApplication
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items)

	// 批量查发行商名 + 剧名
	distIDs := make([]uint64, 0, len(items))
	dramaIDs := make([]uint64, 0, len(items))
	for _, cl := range items {
		distIDs = append(distIDs, cl.DistributorID)
		dramaIDs = append(dramaIDs, cl.DramaID)
	}
	distMap := map[uint64]string{}
	if len(distIDs) > 0 {
		var dists []model.Distributor
		s.db.Select("id, name, org_name, nickname").Where("id IN ?", distIDs).Find(&dists)
		for _, d := range dists {
			distMap[d.ID] = distributorName(&d)
		}
	}
	dramaMap := map[uint64]string{}
	if len(dramaIDs) > 0 {
		var dramas []model.Drama
		s.db.Select("id, title").Where("id IN ?", dramaIDs).Find(&dramas)
		for _, d := range dramas {
			dramaMap[d.ID] = d.Title
		}
	}

	list := make([]gin.H, 0, len(items))
	for _, cl := range items {
		list = append(list, gin.H{
			"id":                  cl.ID,
			"application_no":      cl.ApplicationNo,
			"distributor_id":      cl.DistributorID,
			"distributor_name":    distMap[cl.DistributorID],
			"drama_id":            cl.DramaID,
			"drama_title":         dramaMap[cl.DramaID],
			"platforms":           parsePlatforms(cl.Platforms),
			"deposit_amount_cents": cl.DepositAmountCents,
			"deposit_status":      cl.DepositStatus,
			"status":              cl.Status,
			"contract_status":     cl.ContractStatus,
			"reject_reason":       cl.RejectReason,
			"created_at":          cl.CreatedAt,
			"reviewed_at":         cl.ReviewedAt,
		})
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

// POST /admin/distributor-claims/:id/approve —— 审核通过（创建授权 + 合同）
func (s *Server) adminApproveClaim(c *gin.Context) {
	claimID := parseUint(c.Param("id"))
	var claim model.DistributorApplication
	if err := s.db.First(&claim, claimID).Error; err != nil {
		response.NotFound(c, "认领申请不存在")
		return
	}
	if claim.Status != model.ClaimReviewPending {
		response.Conflict(c, "仅待审核状态可审核通过")
		return
	}

	now := time.Now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 更新认领状态
		if err := tx.Model(&claim).Updates(map[string]interface{}{
			"status":      model.ClaimContractPending,
			"reviewed_at": now,
		}).Error; err != nil {
			return err
		}
		// 创建授权记录
		dd := model.DistributorDrama{
			DistributorID:      claim.DistributorID,
			DramaID:            claim.DramaID,
			ApplicationID:      claim.ID,
			Platforms:          claim.Platforms,
			Status:             model.DistDramaAuthorized,
			AuthorizedAt:       &now,
			DepositAmountCents: claim.DepositAmountCents,
			DepositStatus:      model.DepositFrozen,
		}
		if err := tx.Create(&dd).Error; err != nil {
			return err
		}
		// 创建合同
		ct := model.DistributorContract{
			DistributorID:      claim.DistributorID,
			DramaID:            claim.DramaID,
			DistributorDramaID: dd.ID,
			ContractNo:         fmt.Sprintf("CT-DIST%06d", dd.ID),
			AmountCents:        0, // 合同金额线下确定
			PaymentStatus:      model.ContractPayUnpaid,
			Status:             "pending",
		}
		if err := tx.Create(&ct).Error; err != nil {
			return err
		}
		// 关联合同到授权记录
		return tx.Model(&dd).Update("contract_id", ct.ID).Error
	})
	if err != nil {
		response.ServerError(c, "审核通过失败")
		return
	}

	response.OK(c, gin.H{"id": claimID, "status": model.ClaimContractPending})
}

// POST /admin/distributor-claims/:id/reject —— 驳回（释放押金）
func (s *Server) adminRejectClaim(c *gin.Context) {
	claimID := parseUint(c.Param("id"))
	var req struct {
		Reason string `json:"reason" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "reason 必填")
		return
	}
	var claim model.DistributorApplication
	if err := s.db.First(&claim, claimID).Error; err != nil {
		response.NotFound(c, "认领申请不存在")
		return
	}
	if claim.Status != model.ClaimReviewPending {
		response.Conflict(c, "仅待审核状态可驳回")
		return
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		// 如果已付押金，释放回可用
		if claim.DepositStatus == model.ClaimDepositPaid {
			var dist model.Distributor
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&dist, claim.DistributorID).Error; err != nil {
				return err
			}
			dist.DepositFrozenCents -= claim.DepositAmountCents
			dist.DepositAvailableCents += claim.DepositAmountCents
			if err := tx.Save(&dist).Error; err != nil {
				return err
			}
			s.recordDepositTx(tx, claim.DistributorID, model.DepositTxUnfreeze, claim.DepositAmountCents, dist.DepositAvailableCents, "claim", claim.ApplicationNo, "认领驳回释放押金")
		}
		return tx.Model(&claim).Updates(map[string]interface{}{
			"status":         model.ClaimRejected,
			"reject_reason":  req.Reason,
			"reviewed_at":    now,
		}).Error
	})
	if err != nil {
		response.ServerError(c, "驳回失败")
		return
	}

	response.OK(c, gin.H{"id": claimID, "status": model.ClaimRejected})
}

// POST /admin/distributor-claims/:id/contract —— 上传合同 + 标记完成
func (s *Server) adminUploadContract(c *gin.Context) {
	claimID := parseUint(c.Param("id"))
	var req struct {
		ContractFileURL string `json:"contract_file_url" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "contract_file_url 必填")
		return
	}
	var claim model.DistributorApplication
	if err := s.db.First(&claim, claimID).Error; err != nil {
		response.NotFound(c, "认领申请不存在")
		return
	}
	if claim.Status != model.ClaimContractPending {
		response.Conflict(c, "仅待签署合同状态可上传合同")
		return
	}

	now := time.Now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 更新认领状态为已完成
		if err := tx.Model(&claim).Updates(map[string]interface{}{
			"status":            model.ClaimCompleted,
			"contract_status":   "completed",
			"contract_file_url": req.ContractFileURL,
			"completed_at":      now,
		}).Error; err != nil {
			return err
		}
		// 更新授权记录为 active
		if err := tx.Model(&model.DistributorDrama{}).Where("application_id = ?", claimID).Update("status", model.DistDramaActive).Error; err != nil {
			return err
		}
		// 更新合同
		return tx.Model(&model.DistributorContract{}).Where("distributor_drama_id IN (SELECT id FROM distributor_dramas WHERE application_id = ?)", claimID).Updates(map[string]interface{}{
			"file_url": req.ContractFileURL,
			"status":   "signed",
		}).Error
	})
	if err != nil {
		response.ServerError(c, "上传合同失败")
		return
	}

	response.OK(c, gin.H{"id": claimID, "status": model.ClaimCompleted})
}

// ============================================================
// Admin 收益导入
// ============================================================

type distributorIncomeImportRow struct {
	DistributorID uint64 `json:"distributor_id"`
	DramaID       uint64 `json:"drama_id"`
	Platform      string `json:"platform"`
	StatDate      string `json:"stat_date"`
	GrossCents    int64  `json:"gross_cents"`
}

// POST /admin/finance/distributor-income/import —— 导入发行收益
func (s *Server) adminImportDistributorIncome(c *gin.Context) {
	var rows []distributorIncomeImportRow
	if err := c.ShouldBindJSON(&rows); err != nil {
		response.InvalidParam(c, "参数格式错误")
		return
	}
	if len(rows) == 0 {
		response.InvalidParam(c, "至少导入一条记录")
		return
	}

	batchNo := fmt.Sprintf("BATCH%06d", time.Now().UnixMilli()%1000000)
	successCount := 0
	for i, row := range rows {
		shareBP := 5500 // 55%
		incomeCents := row.GrossCents * int64(shareBP) / 10000

		inc := model.DistributorIncomeDaily{
			DistributorID: row.DistributorID,
			DramaID:       row.DramaID,
			Platform:      row.Platform,
			StatDate:      row.StatDate,
			GrossCents:    row.GrossCents,
			ShareRatioBP:  shareBP,
			IncomeCents:   incomeCents,
			BatchNo:       batchNo,
			ImportRowNo:   i + 1,
		}
		if err := s.db.Create(&inc).Error; err != nil {
			continue
		}
		// 累加发行商收益
		s.db.Model(&model.Distributor{}).Where("id = ?", row.DistributorID).
			UpdateColumns(map[string]interface{}{
				"total_income_cents": gorm.Expr("total_income_cents + ?", incomeCents),
				"balance_cents":      gorm.Expr("balance_cents + ?", incomeCents),
			})
		successCount++
	}

	response.OK(c, gin.H{
		"batch_no":       batchNo,
		"total":          len(rows),
		"success":        successCount,
		"failed":         len(rows) - successCount,
	})
}

// ============================================================
// Admin 结算管理
// ============================================================

// GET /admin/distributor-settlements —— 结算单列表
func (s *Server) adminListDistributorSettlements(c *gin.Context) {
	page, pageSize := paginate(c)
	q := s.db.Model(&model.DistributorSettlement{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("distributor_id"); v != "" {
		q = q.Where("distributor_id = ?", v)
	}
	var total int64
	q.Count(&total)
	var items []model.DistributorSettlement
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items)

	distIDs := make([]uint64, 0, len(items))
	for _, st := range items {
		distIDs = append(distIDs, st.DistributorID)
	}
	distMap := map[uint64]string{}
	if len(distIDs) > 0 {
		var dists []model.Distributor
		s.db.Select("id, name, org_name, nickname").Where("id IN ?", distIDs).Find(&dists)
		for _, d := range dists {
			distMap[d.ID] = distributorName(&d)
		}
	}

	list := make([]gin.H, 0, len(items))
	for _, st := range items {
		list = append(list, gin.H{
			"id":                     st.ID,
			"settlement_no":          st.SettlementNo,
			"distributor_id":         st.DistributorID,
			"distributor_name":       distMap[st.DistributorID],
			"cycle_key":              st.CycleKey,
			"status":                 st.Status,
			"gross_cents":            st.GrossCents,
			"net_cents":              st.NetCents,
			"deducted_deposit_cents": st.DeductedDepositCents,
			"withdrawable_cents":     st.WithdrawableCents,
			"created_at":             st.CreatedAt,
		})
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

// POST /admin/distributor-settlements/generate —— 手动生成结算单
func (s *Server) adminGenerateDistributorSettlement(c *gin.Context) {
	var req struct {
		DistributorID uint64 `json:"distributor_id" binding:"required"`
		CycleKey      string `json:"cycle_key" binding:"required"`
		PeriodRange   string `json:"period_range" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "distributor_id, cycle_key, period_range 必填")
		return
	}

	// 检查是否已存在
	var existingCount int64
	s.db.Model(&model.DistributorSettlement{}).Where("distributor_id = ? AND cycle_key = ?", req.DistributorID, req.CycleKey).Count(&existingCount)
	if existingCount > 0 {
		response.Conflict(c, "该周期结算单已存在")
		return
	}

	// 汇总收益
	type sumRow struct {
		Gross int64
		Inc   int64
	}
	var sr sumRow
	startDate := req.PeriodRange[:10]
	endDate := req.PeriodRange[len(req.PeriodRange)-10:]
	s.db.Table("distributor_income_daily").
		Select("COALESCE(SUM(gross_cents),0) as gross, COALESCE(SUM(income_cents),0) as inc").
		Where("distributor_id = ? AND stat_date >= ? AND stat_date <= ?", req.DistributorID, startDate, endDate).
		Scan(&sr)

	gross := sr.Gross
	platformCents := gross * 45 / 100
	netCents := gross * 55 / 100

	// 押金抵扣：优先从冻结押金中抵扣
	var d model.Distributor
	s.db.First(&d, req.DistributorID)
	deducted := int64(0)
	if d.DepositFrozenCents > 0 && netCents > 0 {
		deducted = d.DepositFrozenCents
		if deducted > netCents {
			deducted = netCents
		}
	}
	withdrawable := netCents - deducted

	st := model.DistributorSettlement{
		SettlementNo:          fmt.Sprintf("ST-DIST%06d", time.Now().UnixMilli()%1000000),
		DistributorID:         req.DistributorID,
		Period:                req.CycleKey[:7],
		CycleKey:              req.CycleKey,
		PeriodRange:           req.PeriodRange,
		GrossCents:            gross,
		PlatformCents:         platformCents,
		NetCents:              netCents,
		DeductedDepositCents:  deducted,
		WithdrawableCents:     withdrawable,
		Status:                "open",
		OpenedAt:              &[]time.Time{time.Now()}[0],
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&st).Error; err != nil {
			return err
		}
		// 押金抵扣
		if deducted > 0 {
			var dist model.Distributor
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&dist, req.DistributorID).Error; err != nil {
				return err
			}
			dist.DepositFrozenCents -= deducted
			dist.DepositDeductedCents += deducted
			if err := tx.Save(&dist).Error; err != nil {
				return err
			}
			s.recordDepositTx(tx, req.DistributorID, model.DepositTxDeduct, -deducted, dist.DepositAvailableCents, "settlement", st.SettlementNo, "收益抵扣押金")
		}
		return nil
	})
	if err != nil {
		response.ServerError(c, "生成结算单失败")
		return
	}

	response.OK(c, gin.H{
		"settlement_no":         st.SettlementNo,
		"gross_cents":           st.GrossCents,
		"net_cents":             st.NetCents,
		"deducted_deposit_cents": st.DeductedDepositCents,
		"withdrawable_cents":    st.WithdrawableCents,
		"status":                st.Status,
	})
}

// ============================================================
// Admin 提现管理
// ============================================================

// GET /admin/distributor-withdrawals —— 提现列表
func (s *Server) adminListDistributorWithdrawals(c *gin.Context) {
	page, pageSize := paginate(c)
	q := s.db.Model(&model.DistributorWithdrawal{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	var total int64
	q.Count(&total)
	var items []model.DistributorWithdrawal
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items)

	distIDs := make([]uint64, 0, len(items))
	for _, w := range items {
		distIDs = append(distIDs, w.DistributorID)
	}
	distMap := map[uint64]model.Distributor{}
	if len(distIDs) > 0 {
		var dists []model.Distributor
		s.db.Where("id IN ?", distIDs).Find(&dists)
		for _, d := range dists {
			distMap[d.ID] = d
		}
	}

	list := make([]gin.H, 0, len(items))
	for _, w := range items {
		v := gin.H{
			"id":            w.ID,
			"withdrawal_no": w.WithdrawalNo,
			"distributor_id": w.DistributorID,
			"amount_cents":  w.AmountCents,
			"status":        w.Status,
			"created_at":    w.CreatedAt,
			"reviewed_at":   w.ReviewedAt,
			"paid_at":       w.PaidAt,
		}
		if d, ok := distMap[w.DistributorID]; ok {
			v["distributor_name"] = distributorName(&d)
			v["bank_name"] = d.BankName
			v["bank_no_masked"] = d.BankCardNoMasked
			// 解密完整银行卡号
			if s.cryptor != nil && d.BankCardNoEnc != "" {
				if full, err := s.cryptor.Decrypt(d.BankCardNoEnc); err == nil {
					v["bank_card_no_full"] = full
				}
			}
		}
		list = append(list, v)
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

// POST /admin/distributor-withdrawals/:id/review —— 审核提现
func (s *Server) adminReviewDistributorWithdrawal(c *gin.Context) {
	id := parseUint(c.Param("id"))
	var req struct {
		Action  string `json:"action" binding:"required"` // approve / reject
		Remark  string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "action 必填（approve/reject）")
		return
	}
	var w model.DistributorWithdrawal
	if err := s.db.First(&w, id).Error; err != nil {
		response.NotFound(c, "提现记录不存在")
		return
	}
	if w.Status != "pending" {
		response.Conflict(c, "仅待处理状态可审核")
		return
	}

	reviewerID := middleware.CurrentID(c)
	now := time.Now()

	if req.Action == "approve" {
		s.db.Model(&w).Updates(map[string]interface{}{
			"status":      "approved",
			"reviewed_by": reviewerID,
			"reviewed_at": now,
			"remark":      req.Remark,
		})
		response.OK(c, gin.H{"id": id, "status": "approved"})
	} else if req.Action == "reject" {
		// 驳回：退回余额
		err := s.db.Transaction(func(tx *gorm.DB) error {
			var dist model.Distributor
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&dist, w.DistributorID).Error; err != nil {
				return err
			}
			dist.FrozenCents -= w.AmountCents
			dist.BalanceCents += w.AmountCents
			if err := tx.Save(&dist).Error; err != nil {
				return err
			}
			return tx.Model(&w).Updates(map[string]interface{}{
				"status":      "rejected",
				"reviewed_by": reviewerID,
				"reviewed_at": now,
				"remark":      req.Remark,
			}).Error
		})
		if err != nil {
			response.ServerError(c, "驳回失败")
			return
		}
		response.OK(c, gin.H{"id": id, "status": "rejected"})
	} else {
		response.InvalidParam(c, "action 只能是 approve 或 reject")
	}
}

// POST /admin/distributor-withdrawals/:id/mark-paid —— 标记已打款
func (s *Server) adminMarkPaidDistributorWithdrawal(c *gin.Context) {
	id := parseUint(c.Param("id"))
	var req struct {
		TransactionNo string `json:"transaction_no"`
	}
	_ = c.ShouldBindJSON(&req)
	var w model.DistributorWithdrawal
	if err := s.db.First(&w, id).Error; err != nil {
		response.NotFound(c, "提现记录不存在")
		return
	}
	if w.Status != "approved" {
		response.Conflict(c, "仅打款中状态可标记已打款")
		return
	}

	now := time.Now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var dist model.Distributor
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&dist, w.DistributorID).Error; err != nil {
			return err
		}
		dist.FrozenCents -= w.AmountCents
		if err := tx.Save(&dist).Error; err != nil {
			return err
		}
		return tx.Model(&w).Updates(map[string]interface{}{
			"status":         "paid",
			"paid_at":        now,
			"transaction_no": req.TransactionNo,
		}).Error
	})
	if err != nil {
		response.ServerError(c, "标记已打款失败")
		return
	}

	// 更新结算单状态
	s.db.Model(&model.DistributorSettlement{}).Where("id = ?", w.SettlementID).Update("status", "paid")

	response.OK(c, gin.H{"id": id, "status": "paid"})
}
