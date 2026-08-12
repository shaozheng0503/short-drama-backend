package handler

import (
	"fmt"
	"strconv"
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
	// 事务外仅做存在性校验
	var d model.Distributor
	if err := s.db.First(&d, id).Error; err != nil {
		response.NotFound(c, "发行商不存在")
		return
	}

	now := time.Now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 行锁 distributor，防止并发重复审核（与 adminApproveClaim 对称）
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&d, id).Error; err != nil {
			return err
		}
		// 事务内重新校验状态
		if d.VerifyStatus != model.DistributorVerifyPending {
			return fmt.Errorf("仅待审核的发行商可审核通过（当前: %s）", d.VerifyStatus)
		}
		return tx.Model(&d).Updates(map[string]interface{}{
			"verify_status":        model.DistributorVerifyVerified,
			"verify_checked_at":    now,
			"verify_reject_reason": "",
			"verify_reject_fields": "",
		}).Error
	})
	if err != nil {
		if isNotFound(err) {
			response.NotFound(c, "发行商不存在")
		} else {
			response.Conflict(c, err.Error())
		}
		return
	}
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
	// 事务外仅做存在性校验
	var d model.Distributor
	if err := s.db.First(&d, id).Error; err != nil {
		response.NotFound(c, "发行商不存在")
		return
	}

	now := time.Now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 行锁 distributor，防止并发重复驳回/审核（与 adminApproveDistributorVerification 对称）
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&d, id).Error; err != nil {
			return err
		}
		// 事务内重新校验状态
		if d.VerifyStatus != model.DistributorVerifyPending {
			return fmt.Errorf("仅待审核的发行商可驳回（当前: %s）", d.VerifyStatus)
		}
		return tx.Model(&d).Updates(map[string]interface{}{
			"verify_status":        model.DistributorVerifyRejected,
			"verify_reject_reason": req.Reason,
			"verify_checked_at":    now,
		}).Error
	})
	if err != nil {
		if isNotFound(err) {
			response.NotFound(c, "发行商不存在")
		} else {
			response.Conflict(c, err.Error())
		}
		return
	}
	response.OK(c, gin.H{"id": id, "verify_status": model.DistributorVerifyRejected})
}

// POST /admin/distributors/:id/ban —— 封禁
func (s *Server) adminBanDistributor(c *gin.Context) {
	id := parseUint(c.Param("id"))
	res := s.db.Model(&model.Distributor{}).Where("id = ?", id).Update("status", model.StatusBanned)
	if res.Error != nil {
		response.ServerError(c, "封禁失败")
		return
	}
	if res.RowsAffected == 0 {
		response.NotFound(c, "发行商不存在")
		return
	}
	response.OK(c, gin.H{"id": id, "status": model.StatusBanned})
}

// POST /admin/distributors/:id/unban —— 解封
func (s *Server) adminUnbanDistributor(c *gin.Context) {
	id := parseUint(c.Param("id"))
	res := s.db.Model(&model.Distributor{}).Where("id = ?", id).Update("status", model.StatusActive)
	if res.Error != nil {
		response.ServerError(c, "解封失败")
		return
	}
	if res.RowsAffected == 0 {
		response.NotFound(c, "发行商不存在")
		return
	}
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
	// 事务外仅做存在性校验，状态 + 押金校验移入事务内加锁后执行（防止并发重复审核）

	now := time.Now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 行锁 claim，防止并发重复审核（与 adminRejectClaim / adminUploadContract 对称）
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&claim, claimID).Error; err != nil {
			return err
		}
		// 事务内重新校验状态：并发场景下另一个请求可能已经把它改成 contract_pending
		if claim.Status != model.ClaimReviewPending {
			return fmt.Errorf("仅待审核状态可审核通过（当前: %s）", claim.Status)
		}
		// 押金状态校验：审核通过前必须确认押金已冻结（deposit_status=paid）。
		// 防止手动跳过 publisherPayDeposit 步骤导致押金未扣。
		if claim.DepositStatus != model.ClaimDepositPaid {
			return fmt.Errorf("押金未冻结（当前状态: %s），无法审核通过。请确保发行商已完成押金支付流程", claim.DepositStatus)
		}
		// 兜底校验：检查是否已有这笔认领的冻结流水记录。
		// 如果 deposit_status=paid 但实际没有冻结流水，事务内自动补充冻结。
		var freezeTxCount int64
		tx.Model(&model.DistributorDepositTransaction{}).
			Where("distributor_id = ? AND type = ? AND related_business_no = ?",
				claim.DistributorID, model.DepositTxFreeze, claim.ApplicationNo).
			Count(&freezeTxCount)
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
			if err := s.recordDepositTx(tx, claim.DistributorID, model.DepositTxFreeze,
				-claim.DepositAmountCents, d.DepositAvailableCents,
				"claim", claim.ApplicationNo, "认领审核兜底冻结押金", claim.DramaID); err != nil {
				return err
			}
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
			ContractNo:         generateBusinessNo("CT-DIST"),
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
		if isNotFound(err) {
			response.NotFound(c, "认领申请不存在")
		} else {
			response.Conflict(c, err.Error())
		}
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
	// 事务外仅做存在性校验，状态校验移入事务内加锁后执行

	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 行锁 claim，防止并发重复解冻
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&claim, claimID).Error; err != nil {
			return err
		}
		if claim.Status != model.ClaimReviewPending {
			return fmt.Errorf("仅待审核状态可驳回（当前: %s）", claim.Status)
		}
		now := time.Now()
		// 如果已付押金，释放回可用
		if claim.DepositStatus == model.ClaimDepositPaid && claim.DepositAmountCents > 0 {
			var dist model.Distributor
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&dist, claim.DistributorID).Error; err != nil {
				return err
			}
			// 冻结余额下限校验：防止数据异常导致负数
			if dist.DepositFrozenCents < claim.DepositAmountCents {
				return fmt.Errorf("发行商冻结押金余额异常（需要 %.2f 元，冻结 %.2f 元），无法解冻，请先对账",
					float64(claim.DepositAmountCents)/100, float64(dist.DepositFrozenCents)/100)
			}
			dist.DepositFrozenCents -= claim.DepositAmountCents
			dist.DepositAvailableCents += claim.DepositAmountCents
			if err := tx.Save(&dist).Error; err != nil {
				return err
			}
			if err := s.recordDepositTx(tx, claim.DistributorID, model.DepositTxUnfreeze, claim.DepositAmountCents, dist.DepositAvailableCents, "claim", claim.ApplicationNo, "认领驳回释放押金", claim.DramaID); err != nil {
				return err
			}
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
		if isNotFound(err) {
			response.NotFound(c, "认领申请不存在")
		} else {
			response.Conflict(c, err.Error())
		}
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
	// 事务外仅做存在性校验，状态校验移入事务内加锁后执行（防止并发重复上传合同）

	// 存 COS key（私有桶），返回时动态生成 presigned GET
	contractFileKey := req.ContractFileKey

	now := time.Now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 行锁 claim，防止并发重复上传合同（与 adminApproveClaim 对称）
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&claim, claimID).Error; err != nil {
			return err
		}
		// 事务内重新校验状态：并发场景下另一个请求可能已经把它改成 completed
		if claim.Status != model.ClaimContractPending {
			return fmt.Errorf("仅待签署合同状态可上传合同（当前: %s）", claim.Status)
		}
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
		if isNotFound(err) {
			response.NotFound(c, "认领申请不存在")
		} else {
			response.Conflict(c, err.Error())
		}
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
// Admin 收益导入（已废弃，2026-07-28 邱嘉诚要求删除，前端已移除）
// ============================================================

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

// adminGenerateDistributorSettlements —— POST /v1/admin/distributor-settlements/generate
// 2026-08-12 恢复：停 cron 后改为手动触发，与创作者结算单生成对称。
// 请求体：
//   {"cycle_key": "2026-08-H1"}
//   {"period": "2026-08", "half": "H1"}
func (s *Server) adminGenerateDistributorSettlements(c *gin.Context) {
	var req struct {
		CycleKey string `json:"cycle_key"`
		Period   string `json:"period"`
		Half     string `json:"half"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请提供 cycle_key 或 period+half")
		return
	}

	cycleKey := req.CycleKey
	if cycleKey == "" {
		if req.Period == "" || req.Half == "" {
			response.InvalidParam(c, "请提供 cycle_key 或 period+half")
			return
		}
		if req.Half != "H1" && req.Half != "H2" {
			response.InvalidParam(c, "half 只能是 H1 或 H2")
			return
		}
		cycleKey = req.Period + "-" + req.Half
	}

	if len(cycleKey) < 8 || cycleKey[4] != '-' || cycleKey[7] != '-' {
		response.InvalidParam(c, "cycle_key 格式不合法，应为 YYYY-MM-H1/H2")
		return
	}
	year, err := strconv.Atoi(cycleKey[:4])
	if err != nil {
		response.InvalidParam(c, "cycle_key 格式不合法")
		return
	}
	month, err := strconv.Atoi(cycleKey[5:7])
	if err != nil || month < 1 || month > 12 {
		response.InvalidParam(c, "cycle_key 月份不合法")
		return
	}
	halfStr := cycleKey[8:]
	if halfStr != "H1" && halfStr != "H2" {
		response.InvalidParam(c, "cycle_key 半月标记不合法，应为 H1 或 H2")
		return
	}

	firstOfMonth := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	var startDate, endDate time.Time
	if halfStr == "H1" {
		startDate = firstOfMonth
		endDate = firstOfMonth.AddDate(0, 0, 14)
	} else {
		startDate = firstOfMonth.AddDate(0, 0, 15)
		endDate = firstOfMonth.AddDate(0, 1, -1)
	}
	startStr := startDate.Format("2006-01-02")
	endStr := endDate.AddDate(0, 0, 1).Format("2006-01-02")
	periodRange := startStr + " ~ " + endDate.Format("2006-01-02")

	// 汇总各发行商收益
	type distAgg struct {
		DistributorID uint64
		GrossCents    int64
		IncomeCents   int64
	}
	var aggs []distAgg
	s.db.Table("distributor_income_daily").
		Select("distributor_id, COALESCE(SUM(gross_cents),0) AS gross_cents, COALESCE(SUM(income_cents),0) AS income_cents").
		Where("stat_date >= ? AND stat_date < ?", startStr, endStr).
		Group("distributor_id").Scan(&aggs)

	if len(aggs) == 0 {
		response.OK(c, gin.H{
			"cycle_key":    cycleKey,
			"period_range": periodRange,
			"created":      0,
			"message":      "该周期无发行商收益数据",
		})
		return
	}

	created := 0
	skipped := 0
	now := time.Now()

	for _, a := range aggs {
		if a.GrossCents <= 0 {
			skipped++
			continue
		}

		// 查重
		var existCount int64
		s.db.Model(&model.DistributorSettlement{}).
			Where("distributor_id = ? AND cycle_key = ?", a.DistributorID, cycleKey).Count(&existCount)
		if existCount > 0 {
			skipped++
			continue
		}

		gross := a.GrossCents
		platformCents := gross * 45 / 100
		netCents := gross * 55 / 100

		st := model.DistributorSettlement{
			SettlementNo:  generateBusinessNo("ST-DIST"),
			DistributorID: a.DistributorID,
			Period:        cycleKey[:7],
			CycleKey:      cycleKey,
			PeriodRange:   periodRange,
			GrossCents:    gross,
			PlatformCents: platformCents,
			NetCents:      netCents,
			Status:        model.DistSettlementPendingPayment,
			OpenedAt:      &now,
		}

		err := s.db.Transaction(func(tx *gorm.DB) error {
			// 事务内重新查重
			var existCount int64
			tx.Model(&model.DistributorSettlement{}).
				Where("distributor_id = ? AND cycle_key = ?", a.DistributorID, cycleKey).Count(&existCount)
			if existCount > 0 {
				return nil
			}

			if err := tx.Create(&st).Error; err != nil {
				if isUniqueViolation(err) {
					return nil
				}
				return err
			}

			// 押金抵扣：行锁内读取冻结余额
			var dist model.Distributor
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&dist, a.DistributorID).Error; err != nil {
				return err
			}
			deducted := int64(0)
			if dist.DepositFrozenCents > 0 && gross > 0 {
				deducted = dist.DepositFrozenCents
				if deducted > gross {
					deducted = gross
				}
			}
			if deducted > 0 {
				dist.DepositFrozenCents -= deducted
				dist.DepositDeductedCents += deducted
				if err := tx.Save(&dist).Error; err != nil {
					return err
				}
				if err := s.recordDepositTx(tx, a.DistributorID, model.DepositTxDeduct, -deducted, dist.DepositAvailableCents, "settlement", st.SettlementNo, "收益抵扣押金", 0); err != nil {
					return err
				}
			}

			payable := gross - deducted
			return tx.Model(&st).Updates(map[string]interface{}{
				"deducted_deposit_cents": deducted,
				"withdrawable_cents":     payable,
				"payable_cents":          payable,
			}).Error
		})
		if err != nil {
			continue
		}
		created++
	}

	response.OK(c, gin.H{
		"cycle_key":    cycleKey,
		"period_range": periodRange,
		"created":      created,
		"skipped":      skipped,
		"message":      fmt.Sprintf("成功生成 %d 笔发行商结算单（跳过 %d 笔已存在）", created, skipped),
	})
}

// POST /admin/distributor-settlements/:id/confirm-receipt —— 确认到账 / 退回

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
		// 行锁结算单
		var st model.DistributorSettlement
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&st, sid).Error; err != nil {
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
			// 回滚押金抵扣：reject 退回时，把已抵扣的押金从 deducted 退回 frozen
			if st.DeductedDepositCents > 0 {
				var dist model.Distributor
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&dist, st.DistributorID).Error; err != nil {
					return err
				}
				// 已抵扣余额下限校验：防止数据异常导致负数
				if dist.DepositDeductedCents < st.DeductedDepositCents {
					return fmt.Errorf("发行商已抵扣押金余额异常（需要 %.2f 元，已抵扣 %.2f 元），无法回滚，请先对账",
						float64(st.DeductedDepositCents)/100, float64(dist.DepositDeductedCents)/100)
				}
				dist.DepositDeductedCents -= st.DeductedDepositCents
				dist.DepositFrozenCents += st.DeductedDepositCents
				if err := tx.Save(&dist).Error; err != nil {
					return err
				}
				if err := s.recordDepositTx(tx, st.DistributorID, model.DepositTxUnfreeze, st.DeductedDepositCents, dist.DepositAvailableCents, "settlement", st.SettlementNo, "结算单退回回滚押金抵扣", 0); err != nil {
					return err
				}
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
	if w.Status != model.WithdrawalStatusPending {
		response.Conflict(c, "仅待处理状态可审核")
		return
	}

	reviewerID := middleware.CurrentID(c)
	now := time.Now()

	if req.Action == "approve" {
		// approve 也走事务并检查错误，保持与 reject 一致
		err := s.db.Transaction(func(tx *gorm.DB) error {
			// 行锁提现单，防止并发审核
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&w, id).Error; err != nil {
				return err
			}
			if w.Status != model.WithdrawalStatusPending {
				return fmt.Errorf("提现已被处理（当前状态: %s）", w.Status)
			}
			// 余额 → 冻结（提现冻结），与 markPaid 时的 FrozenCents -= 对称
			var dist model.Distributor
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&dist, w.DistributorID).Error; err != nil {
				return err
			}
			if dist.BalanceCents < w.AmountCents {
				return fmt.Errorf("发行商余额不足，无法审核通过")
			}
			dist.BalanceCents -= w.AmountCents
			dist.FrozenCents += w.AmountCents
			if err := tx.Save(&dist).Error; err != nil {
				return err
			}
			return tx.Model(&w).Updates(map[string]interface{}{
				"status":      model.WithdrawalStatusApproved,
				"reviewed_by": reviewerID,
				"reviewed_at": now,
				"remark":      req.Remark,
			}).Error
		})
		if err != nil {
			response.Conflict(c, err.Error())
			return
		}
		response.OK(c, gin.H{"id": id, "status": model.WithdrawalStatusApproved})
	} else if req.Action == "reject" {
		// 驳回：退回余额
		err := s.db.Transaction(func(tx *gorm.DB) error {
			// 行锁提现单，防止并发审核
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&w, id).Error; err != nil {
				return err
			}
			if w.Status != model.WithdrawalStatusPending {
				return fmt.Errorf("提现已被处理（当前状态: %s）", w.Status)
			}
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
				"status":      model.WithdrawalStatusRejected,
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
	// 事务外仅做存在性校验，状态校验移入事务内加锁后执行（与创作者提现 markPaid 对称）

	now := time.Now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 行锁提现单，防止并发重复打款（双重扣减 FrozenCents）
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&w, id).Error; err != nil {
			return err
		}
		// 事务内重新校验状态：并发场景下另一个请求可能已经把它改成 paid
		if w.Status != model.WithdrawalStatusApproved {
			return fmt.Errorf("提现已被处理（当前状态: %s），仅打款中状态可标记已打款", w.Status)
		}
		// 行锁 distributor
		var dist model.Distributor
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&dist, w.DistributorID).Error; err != nil {
			return err
		}
		// 余额校验：防 FrozenCents 不足导致负数
		if dist.FrozenCents < w.AmountCents {
			return fmt.Errorf("发行商冻结余额不足（需要 %.2f 元，冻结 %.2f 元），账目异常，请先对账",
				float64(w.AmountCents)/100, float64(dist.FrozenCents)/100)
		}
		dist.FrozenCents -= w.AmountCents
		if err := tx.Save(&dist).Error; err != nil {
			return err
		}
		if err := tx.Model(&w).Updates(map[string]interface{}{
			"status":         model.WithdrawalStatusPaid,
			"paid_at":        now,
			"transaction_no": req.TransactionNo,
		}).Error; err != nil {
			return err
		}
		// 结算单状态移入事务，用正确常量 settled（而非非法裸字符串 "paid"）
		if w.SettlementID > 0 {
			return tx.Model(&model.DistributorSettlement{}).Where("id = ?", w.SettlementID).
				Update("status", model.DistSettlementSettled).Error
		}
		return nil
	})
	if err != nil {
		response.Conflict(c, err.Error())
		return
	}

	response.OK(c, gin.H{"id": id, "status": model.WithdrawalStatusPaid})
}
