package handler

import (
	"encoding/json"
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
// 认领流程（Claim）
// ============================================================

type createClaimRequest struct {
	DramaID   uint64   `json:"drama_id" binding:"required"`
	Platforms []string `json:"platforms" binding:"required"`
}

// POST /v1/publisher/claims —— 创建认领申请
func (s *Server) publisherCreateClaim(c *gin.Context) {
	id := middleware.CurrentID(c)
	var req createClaimRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "drama_id 和 platforms 必填")
		return
	}
	if len(req.Platforms) == 0 {
		response.InvalidParam(c, "至少选择一个发行平台")
		return
	}
	// 校验平台有效性
	for _, p := range req.Platforms {
		switch p {
		case model.PlatformDouyin, model.PlatformKuaishou, model.PlatformWechatVideo, model.PlatformBilibili:
		default:
			response.InvalidParam(c, "无效的发行平台: "+p)
			return
		}
	}

	// 校验剧存在且已上架
	var drama model.Drama
	if err := s.db.First(&drama, req.DramaID).Error; err != nil {
		response.NotFound(c, "剧集不存在")
		return
	}
	if drama.Status != "published" {
		response.Conflict(c, "该剧尚未上架")
		return
	}

	// 校验未发行平台不重复
	var existing []model.DistributorDrama
	s.db.Where("drama_id = ? AND status IN ?", req.DramaID, []string{"authorized", "active"}).Find(&existing)
	releasedMap := map[string]bool{}
	for _, dd := range existing {
		for _, p := range parsePlatforms(dd.Platforms) {
			releasedMap[p] = true
		}
	}
	for _, p := range req.Platforms {
		if releasedMap[p] {
			response.Conflict(c, "平台 "+p+" 已被发行")
			return
		}
	}

	// 计算保证金
	depositAmount := s.calcDepositAmount(drama, req.Platforms)

	// 创建认领申请
	claim := model.DistributorApplication{
		ApplicationNo:      fmt.Sprintf("CL%06d", time.Now().UnixMilli()%1000000),
		DistributorID:      id,
		DramaID:            req.DramaID,
		Platforms:          platformsToJSON(req.Platforms),
		DepositAmountCents: depositAmount,
		DepositStatus:      model.ClaimDepositUnpaid,
		Status:             model.ClaimDepositPending,
	}
	if err := s.db.Create(&claim).Error; err != nil {
		response.ServerError(c, "创建认领申请失败")
		return
	}

	response.OK(c, s.claimView(claim))
}

// GET /v1/publisher/claims/:id —— 认领详情
func (s *Server) publisherGetClaim(c *gin.Context) {
	id := middleware.CurrentID(c)
	claimID := parseUint(c.Param("id"))
	var claim model.DistributorApplication
	if err := s.db.Where("id = ? AND distributor_id = ?", claimID, id).First(&claim).Error; err != nil {
		response.NotFound(c, "认领申请不存在")
		return
	}
	response.OK(c, s.claimView(claim))
}

// POST /v1/publisher/claims/:id/deposit —— 发起押金支付
func (s *Server) publisherPayDeposit(c *gin.Context) {
	id := middleware.CurrentID(c)
	claimID := parseUint(c.Param("id"))
	var claim model.DistributorApplication
	if err := s.db.Where("id = ? AND distributor_id = ?", claimID, id).First(&claim).Error; err != nil {
		response.NotFound(c, "认领申请不存在")
		return
	}
	if claim.Status != model.ClaimDepositPending {
		response.Conflict(c, "当前状态不可支付押金")
		return
	}

	var req struct {
		PaymentMethod string `json:"payment_method"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.PaymentMethod == "" {
		req.PaymentMethod = "wechat"
	}

	// 校验余额是否足够
	var d model.Distributor
	if err := s.db.First(&d, id).Error; err != nil {
		response.ServerError(c, "查询发行商失败")
		return
	}
	if d.DepositAvailableCents < claim.DepositAmountCents {
		response.Conflict(c, fmt.Sprintf("押金余额不足，需要 %d 分，当前可用 %d 分", claim.DepositAmountCents, d.DepositAvailableCents))
		return
	}

	// 事务：冻结押金 + 更新认领状态
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 行锁 distributor
		var dist model.Distributor
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&dist, id).Error; err != nil {
			return err
		}
		if dist.DepositAvailableCents < claim.DepositAmountCents {
			return fmt.Errorf("押金余额不足")
		}
		dist.DepositAvailableCents -= claim.DepositAmountCents
		dist.DepositFrozenCents += claim.DepositAmountCents
		if err := tx.Save(&dist).Error; err != nil {
			return err
		}
		// 记录流水
		s.recordDepositTx(tx, id, model.DepositTxFreeze, -claim.DepositAmountCents, dist.DepositAvailableCents, "claim", claim.ApplicationNo, "认领剧集冻结押金")
		// 更新认领状态
		now := time.Now()
		return tx.Model(&claim).Updates(map[string]interface{}{
			"deposit_status": model.ClaimDepositPaid,
			"status":         model.ClaimAuthPending,
			"updated_at":     now,
		}).Error
	})
	if err != nil {
		response.Conflict(c, err.Error())
		return
	}

	s.db.First(&claim, claimID)
	response.OK(c, s.claimView(claim))
}

// POST /v1/publisher/claims/:id/submit —— 确认已完成第三方授权后提交审核
func (s *Server) publisherSubmitClaim(c *gin.Context) {
	id := middleware.CurrentID(c)
	claimID := parseUint(c.Param("id"))
	var claim model.DistributorApplication
	if err := s.db.Where("id = ? AND distributor_id = ?", claimID, id).First(&claim).Error; err != nil {
		response.NotFound(c, "认领申请不存在")
		return
	}
	if claim.Status != model.ClaimAuthPending {
		response.Conflict(c, "当前状态不可提交审核")
		return
	}

	var req struct {
		AuthorizationConfirmed bool `json:"authorization_confirmed"`
	}
	_ = c.ShouldBindJSON(&req)
	if !req.AuthorizationConfirmed {
		response.InvalidParam(c, "必须勾选确认已完成第三方平台授权")
		return
	}

	now := time.Now()
	s.db.Model(&claim).Updates(map[string]interface{}{
		"authorization_confirmed": true,
		"status":                  model.ClaimReviewPending,
		"updated_at":              now,
	})

	s.db.First(&claim, claimID)
	response.OK(c, s.claimView(claim))
}

// claimView 认领申请视图
func (s *Server) claimView(claim model.DistributorApplication) gin.H {
	v := gin.H{
		"claim_id":                claim.ID,
		"application_no":          claim.ApplicationNo,
		"drama_id":                claim.DramaID,
		"platforms":               parsePlatforms(claim.Platforms),
		"deposit_amount_cents":    claim.DepositAmountCents,
		"deposit_status":          claim.DepositStatus,
		"authorization_confirmed": claim.AuthorizationConfirmed,
		"review_status":           claim.Status,
		"contract_status":         claim.ContractStatus,
		"contract_file_url":       claim.ContractFileURL,
		"reject_reason":           claim.RejectReason,
		"completed_at":            claim.CompletedAt,
		"created_at":              claim.CreatedAt,
	}
	// 剧名
	var drama model.Drama
	if err := s.db.Select("title, cover_url").First(&drama, claim.DramaID).Error; err == nil {
		v["drama_title"] = drama.Title
		v["drama_cover_url"] = drama.CoverURL
	}
	return v
}

// GET /v1/publisher/claims —— 认领申请列表
func (s *Server) publisherListClaims(c *gin.Context) {
	id := middleware.CurrentID(c)
	page, pageSize := paginate(c)
	q := s.db.Model(&model.DistributorApplication{}).Where("distributor_id = ?", id)
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	var total int64
	q.Count(&total)
	var claims []model.DistributorApplication
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&claims)

	list := make([]gin.H, 0, len(claims))
	for _, cl := range claims {
		list = append(list, s.claimView(cl))
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

// 确保 encoding/json 被使用
var _ = json.Marshal
