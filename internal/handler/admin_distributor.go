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

	// 批量查发行商名 + 剧名 + 封面
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
	type dramaInfo struct {
		Title    string
		CoverURL string
	}
	dramaMap := map[uint64]dramaInfo{}
	if len(dramaIDs) > 0 {
		var dramas []model.Drama
		s.db.Select("id, title, cover_url").Where("id IN ?", dramaIDs).Find(&dramas)
		for _, d := range dramas {
			dramaMap[d.ID] = dramaInfo{Title: d.Title, CoverURL: d.CoverURL}
		}
	}

	list := make([]gin.H, 0, len(items))
	for _, cl := range items {
		di := dramaMap[cl.DramaID]
		list = append(list, gin.H{
			"id":                   cl.ID,
			"application_no":       cl.ApplicationNo,
			"distributor_id":       cl.DistributorID,
			"distributor_name":     distMap[cl.DistributorID],
			"drama_id":             cl.DramaID,
			"drama_title":          di.Title,
			"drama_cover_url":      di.CoverURL,
			"platforms":            parsePlatforms(cl.Platforms),
			"deposit_amount_cents": cl.DepositAmountCents,
			"deposit_status":       cl.DepositStatus,
			"status":               cl.Status,
			"contract_status":      cl.ContractStatus,
			"contract_file_url":    s.contractPresignedURL(cl.ContractFileKey),
			"reject_reason":        cl.RejectReason,
			"created_at":           cl.CreatedAt,
			"reviewed_at":          cl.ReviewedAt,
		})
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

// GET /admin/distributor-claims/:id —— 认领申请详情
func (s *Server) adminGetDistributorClaim(c *gin.Context) {
	claimID := parseUint(c.Param("id"))
	var claim model.DistributorApplication
	if err := s.db.First(&claim, claimID).Error; err != nil {
		response.NotFound(c, "认领申请不存在")
		return
	}

	// 查发行商名
	var dist model.Distributor
	s.db.First(&dist, claim.DistributorID)
	// 查剧信息
	var drama model.Drama
	s.db.Select("id, title, cover_url, total_episodes").First(&drama, claim.DramaID)
	// 查授权记录
	var dds []model.DistributorDrama
	s.db.Where("application_id = ?", claimID).Find(&dds)
	// 查合同
	var contract *model.DistributorContract
	for _, dd := range dds {
		if dd.ContractID != nil {
			var ct model.DistributorContract
			if err := s.db.First(&ct, *dd.ContractID).Error; err == nil {
				contract = &ct
				break
			}
		}
	}

	v := gin.H{
		"id":                      claim.ID,
		"application_no":          claim.ApplicationNo,
		"distributor_id":          claim.DistributorID,
		"distributor_name":        distributorName(&dist),
		"drama_id":                claim.DramaID,
		"drama_title":             drama.Title,
		"drama_cover_url":         drama.CoverURL,
		"episode_count":           drama.TotalEpisodes,
		"platforms":               parsePlatforms(claim.Platforms),
		"deposit_amount_cents":    claim.DepositAmountCents,
		"deposit_status":          claim.DepositStatus,
		"status":                  claim.Status,
		"contract_status":         claim.ContractStatus,
		"contract_file_url":       s.contractPresignedURL(claim.ContractFileKey),
		"reject_reason":           claim.RejectReason,
		"authorization_confirmed": claim.AuthorizationConfirmed,
		"reviewed_at":             claim.ReviewedAt,
		"authorized_at":           claim.AuthorizedAt,
		"completed_at":            claim.CompletedAt,
		"created_at":              claim.CreatedAt,
	}
	if contract != nil {
		v["contract_no"] = contract.ContractNo
		v["contract_amount_cents"] = contract.AmountCents
	}
	response.OK(c, v)
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
	// 押金状态校验：审核通过前必须确认押金已冻结（deposit_status=paid）。
	// 防止手动跳过 publisherPayDeposit 步骤导致押金未扣。
	if claim.DepositStatus != model.ClaimDepositPaid {
		response.Conflict(c, fmt.Sprintf("押金未冻结（当前状态: %s），无法审核通过。请确保发行商已完成押金支付流程", claim.DepositStatus))
		return
	}
	// 兜底校验：检查是否已有这笔认领的冻结流水记录。
	// 如果 deposit_status=paid 但实际没有冻结流水，事务内自动补充冻结。
	var freezeTxCount int64
	s.db.Model(&model.DistributorDepositTransaction{}).
		Where("distributor_id = ? AND type = ? AND related_business_no = ?",
			claim.DistributorID, model.DepositTxFreeze, claim.ApplicationNo).
		Count(&freezeTxCount)

	now := time.Now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 兜底冻结：如果 deposit_status=paid 但实际没有冻结流水，自动补充冻结
		if freezeTxCount == 0 && claim.DepositAmountCents > 0 {
			var d model.Distributor
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&d, claim.DistributorID).Error; err != nil {
				return err
			}
			if d.DepositAvailableCents < claim.DepositAmountCents {
				return fmt.Errorf("发行商可用押金余额不足（需要 %.2f 元，可用 %.2f 元），无法补充冻结",
					float64(claim.DepositAmountCents)/100, float64(d.DepositAvailableCents)/100)
			}
			d.DepositAvailableCents -= claim.DepositAmountCents
			d.DepositFrozenCents += claim.DepositAmountCents
			if err := tx.Save(&d).Error; err != nil {
				return err
			}
			s.recordDepositTx(tx, claim.DistributorID, model.DepositTxFreeze,
				-claim.DepositAmountCents, d.DepositAvailableCents,
				"claim", claim.ApplicationNo, "认领审核兜底冻结押金")
		}
		// 更新认领状态
		if err := tx.Model(&claim).Updates(map[string]interface{}{
			"status":          model.ClaimContractPending,
			"contract_status": model.ClaimContractPending_,
			"reviewed_at":     now,
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
		response.Conflict(c, err.Error())
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
			"status":          model.ClaimRejected,
			"deposit_status":  model.ClaimDepositReleased,
			"contract_status": model.ClaimContractNone,
			"reject_reason":   req.Reason,
			"reviewed_at":     now,
		}).Error
	})
	if err != nil {
		response.ServerError(c, "驳回失败")
		return
	}

	response.OK(c, gin.H{"id": claimID, "status": model.ClaimRejected})
}

// POST /admin/distributor-claims/:id/contract —— 上传合同 + 标记完成
// 前端流程：先调 POST /admin/uploads/contract-sign 拿 upload_url + key → PUT 上传 PDF → 回填 contract_file_key 到本接口
func (s *Server) adminUploadContract(c *gin.Context) {
	claimID := parseUint(c.Param("id"))
	var req struct {
		ContractFileKey string `json:"contract_file_key" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "contract_file_key 必填")
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

	// 存 COS key（私有桶），返回时动态生成 presigned GET
	contractFileKey := req.ContractFileKey

	now := time.Now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 更新认领状态为已完成，同时存 key
		if err := tx.Model(&claim).Updates(map[string]interface{}{
			"status":            model.ClaimCompleted,
			"contract_status":   "completed",
			"contract_file_key": contractFileKey,
			"contract_file_url": s.cos.PublicURL(contractFileKey), // 存裸 URL 做记录
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
			"file_key": contractFileKey,
			"file_url": s.cos.PublicURL(contractFileKey),
			"status":   "signed",
		}).Error
	})
	if err != nil {
		response.ServerError(c, "上传合同失败")
		return
	}

	// 返回 presigned GET 下载链接
	presignedURL, _, _ := s.cos.PresignedGET(contractFileKey)
	response.OK(c, gin.H{"id": claimID, "status": model.ClaimCompleted, "contract_file_url": presignedURL})
}

// contractPresignedURL 从 COS key 生成短时 presigned GET 下载链接。
// 如果 key 为空则返回空串。
func (s *Server) contractPresignedURL(key string) string {
	if key == "" {
		return ""
	}
	url, _, err := s.cos.PresignedGET(key)
	if err != nil {
		return ""
	}
	return url
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
	failedCount := 0
	var failedReasons []string
	for i, row := range rows {
		// 验证 distributor_drama 关联存在性（发行商必须已认领该剧）
		var ddCount int64
		s.db.Model(&model.DistributorDrama{}).
			Where("distributor_id = ? AND drama_id = ? AND status IN ?", row.DistributorID, row.DramaID, []string{"authorized", "active"}).
			Count(&ddCount)
		if ddCount == 0 {
			failedCount++
			failedReasons = append(failedReasons, fmt.Sprintf("row %d: 发行商 %d 未认领剧集 %d", i+1, row.DistributorID, row.DramaID))
			continue
		}

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
			failedCount++
			failedReasons = append(failedReasons, fmt.Sprintf("row %d: %v", i+1, err))
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
		"batch_no":        batchNo,
		"total":           len(rows),
		"success":         successCount,
		"failed":          failedCount,
		"failed_reasons":  failedReasons,
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
			"period_range":           st.PeriodRange,
			"status":                 st.Status,
			"gross_cents":            st.GrossCents,
			"net_cents":              st.NetCents,
			"deducted_deposit_cents": st.DeductedDepositCents,
			"payable_cents":          st.PayableCents,
			"transaction_no":         st.TransactionNo,
			"payment_submitted_at":   st.PaymentSubmittedAt,
			"receipt_confirmed_at":   st.ReceiptConfirmedAt,
			"receipt_reject_reason":  st.ReceiptRejectReason,
			"created_at":             st.CreatedAt,
		})
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

// GET /admin/distributor-settlements/:id —— 结算单详情
func (s *Server) adminGetDistributorSettlement(c *gin.Context) {
	sid := parseUint(c.Param("id"))
	var st model.DistributorSettlement
	if err := s.db.First(&st, sid).Error; err != nil {
		response.NotFound(c, "结算单不存在")
		return
	}

	var dist model.Distributor
	s.db.First(&dist, st.DistributorID)

	response.OK(c, s.distributorSettlementDetailView(&st, &dist))
}

// distributorSettlementDetailView 构建结算单详情视图（管理端和发行商共用）
func (s *Server) distributorSettlementDetailView(st *model.DistributorSettlement, dist *model.Distributor) gin.H {
	v := gin.H{
		"id":                      st.ID,
		"settlement_no":           st.SettlementNo,
		"distributor_id":          st.DistributorID,
		"distributor_name":        distributorName(dist),
		"cycle_key":               st.CycleKey,
		"period_range":            st.PeriodRange,
		"status":                  st.Status,
		"gross_cents":             st.GrossCents,
		"platform_cents":          st.PlatformCents,
		"net_cents":               st.NetCents,
		"deducted_deposit_cents":  st.DeductedDepositCents,
		"payable_cents":           st.PayableCents,
		"transaction_no":          st.TransactionNo,
		"paid_at":                 st.PaidAt,
		"payment_proof_url":       s.contractPresignedURL(st.PaymentProofFileKey),
		"payment_remark":          st.PaymentRemark,
		"payment_submitted_at":    st.PaymentSubmittedAt,
		"receipt_confirmed_at":    st.ReceiptConfirmedAt,
		"receipt_confirmed_by":    st.ReceiptConfirmedBy,
		"receipt_reject_reason":   st.ReceiptRejectReason,
		"remark":                  st.Remark,
		"opened_at":               st.OpenedAt,
		"closed_at":               st.ClosedAt,
		"created_at":              st.CreatedAt,
	}
	return v
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
	if d.DepositFrozenCents > 0 && gross > 0 {
		deducted = d.DepositFrozenCents
		if deducted > gross {
			deducted = gross
		}
	}
	// payable_cents = gross - deducted_deposit（发行商应付平台）
	payable := gross - deducted

	now := time.Now()
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
		WithdrawableCents:     payable, // 兼容旧字段
		PayableCents:          payable,
		Status:                model.DistSettlementPendingPayment,
		OpenedAt:              &now,
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
		"settlement_no":          st.SettlementNo,
		"gross_cents":            st.GrossCents,
		"net_cents":              st.NetCents,
		"deducted_deposit_cents": st.DeductedDepositCents,
		"payable_cents":          st.PayableCents,
		"status":                 st.Status,
	})
}

// POST /admin/distributor-settlements/:id/confirm-receipt —— 确认到账 / 退回
func (s *Server) adminConfirmDistributorSettlement(c *gin.Context) {
	sid := parseUint(c.Param("id"))
	var req struct {
		Action string `json:"action" binding:"required"` // confirm / reject
		Remark string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "action 必填（confirm/reject）")
		return
	}

	now := time.Now()
	adminID := middleware.CurrentID(c)

	err := s.db.Transaction(func(tx *gorm.DB) error {
		var st model.DistributorSettlement
		if err := tx.First(&st, sid).Error; err != nil {
			return err
		}
		if st.Status != model.DistSettlementPaymentSubmitted {
			return fmt.Errorf("仅已打款待确认状态可操作，当前状态: %s", st.Status)
		}

		switch req.Action {
		case "confirm":
			return tx.Model(&st).Updates(map[string]interface{}{
				"status":               model.DistSettlementSettled,
				"receipt_confirmed_at": now,
				"receipt_confirmed_by": adminID,
				"closed_at":            now,
				"receipt_reject_reason": "",
			}).Error
		case "reject":
			if req.Remark == "" {
				return fmt.Errorf("退回时 remark 必填")
			}
			return tx.Model(&st).Updates(map[string]interface{}{
				"status":                model.DistSettlementPendingPayment,
				"receipt_reject_reason": req.Remark,
			}).Error
		default:
			return fmt.Errorf("action 只能是 confirm / reject")
		}
	})
	if err != nil {
		if isNotFound(err) {
			response.NotFound(c, "结算单不存在")
		} else {
			response.Conflict(c, err.Error())
		}
		return
	}

	// 查最新状态
	var st model.DistributorSettlement
	s.db.First(&st, sid)
	response.OK(c, gin.H{
		"id":    sid,
		"status": st.Status,
	})
}

// ============================================================
// Admin 提现管理（已废弃，保留只读）
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
