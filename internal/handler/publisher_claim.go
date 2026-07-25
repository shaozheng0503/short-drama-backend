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

// platformLabel 返回平台的中文显示名
func platformLabel(p string) string {
	switch p {
	case model.PlatformDouyin:
		return "抖音"
	case model.PlatformKuaishou:
		return "快手"
	case model.PlatformWechatVideo:
		return "微信视频号"
	case model.PlatformBilibili:
		return "哔哩哔哩"
	default:
		return p
	}
}

type createClaimRequest struct {
	DramaID   uint64   `json:"drama_id" binding:"required"`
	Platforms []string `json:"platforms" binding:"required"`
}

// POST /v1/publisher/claims —— 创建认领申请
func (s *Server) publisherCreateClaim(c *gin.Context) {
	id := middleware.CurrentID(c)

	// 业务规则：未认证用户不可认领剧集
	if !s.isDistributorVerified(id) {
		response.Forbidden(c, "未认证用户不可认领剧集，请先完成企业认证")
		return
	}

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
		case model.PlatformDouyin, model.PlatformKuaishou, model.PlatformWechatVideo:
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

	// 校验平台未被占用（含已发行 + 审核中的认领）
	occupied := s.getOccupiedPlatforms(req.DramaID)
	for _, p := range req.Platforms {
		if occupied[p] {
			response.Conflict(c, "平台 "+platformLabel(p)+" 已被其他发行商认领或发行")
			return
		}
	}

	// 判断是否为追加认领：同一发行商对该剧是否已有有效认领/授权
	var existingDDs []model.DistributorDrama
	s.db.Where("distributor_id = ? AND drama_id = ? AND status IN ?", id, req.DramaID,
		[]string{model.DistDramaAuthorized, model.DistDramaActive}).Find(&existingDDs)
	hasExisting := len(existingDDs) > 0
	// 也检查自己是否有审核中的认领
	var existingApps int64
	s.db.Model(&model.DistributorApplication{}).Where("distributor_id = ? AND drama_id = ? AND status IN ?",
		id, req.DramaID, []string{
			model.ClaimDepositPending, model.ClaimAuthPending,
			model.ClaimReviewPending, model.ClaimContractPending,
		}).Count(&existingApps)
	hasExisting = hasExisting || existingApps > 0

	// 计算保证金
	var depositAmount int64
	if hasExisting {
		// 追加认领：只收新增平台 × 加价比例，不重新收基础押金
		depositAmount = s.calcAppendDepositAmount(drama, len(req.Platforms))
	} else {
		// 首单：基础押金 + 平台加价
		depositAmount = s.calcDepositAmount(drama, req.Platforms)
	}

	// 创建认领申请
	claim := model.DistributorApplication{
		ApplicationNo:      generateBusinessNo("CL"),
		DistributorID:      id,
		DramaID:            req.DramaID,
		Platforms:          platformsToJSON(req.Platforms),
		DepositAmountCents: depositAmount,
		DepositStatus:      model.ClaimDepositUnpaid,
		Status:             model.ClaimDepositPending,
		ContractStatus:     model.ClaimContractNone,
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

	// 事务外仅做存在性校验，状态校验移入事务内加锁后执行
	var claim model.DistributorApplication
	if err := s.db.Where("id = ? AND distributor_id = ?", claimID, id).First(&claim).Error; err != nil {
		response.NotFound(c, "认领申请不存在")
		return
	}

	var req struct {
		PaymentMethod string `json:"payment_method"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.PaymentMethod == "" {
		req.PaymentMethod = "wechat"
	}

	// 事务：行锁 claim + 行锁 distributor → 状态校验 → 余额校验 → 冻结 → 流水 → 状态更新
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 行锁 claim，防止并发重复冻结
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND distributor_id = ?", claimID, id).First(&claim).Error; err != nil {
			return err
		}
		if claim.Status != model.ClaimDepositPending {
			return fmt.Errorf("当前状态不可支付押金（%s）", claim.Status)
		}
		if claim.DepositAmountCents <= 0 {
			return fmt.Errorf("押金金额异常，请联系管理员")
		}
		// 行锁 distributor
		var dist model.Distributor
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&dist, id).Error; err != nil {
			return err
		}
		if dist.DepositAvailableCents < claim.DepositAmountCents {
			return fmt.Errorf("押金余额不足，需要 %.2f 元，当前可用 %.2f 元", float64(claim.DepositAmountCents)/100, float64(dist.DepositAvailableCents)/100)
		}
		dist.DepositAvailableCents -= claim.DepositAmountCents
		dist.DepositFrozenCents += claim.DepositAmountCents
		if err := tx.Save(&dist).Error; err != nil {
			return err
		}
		// 记录流水
		if err := s.recordDepositTx(tx, id, model.DepositTxFreeze, -claim.DepositAmountCents, dist.DepositAvailableCents, "claim", claim.ApplicationNo, "认领剧集冻结押金"); err != nil {
			return err
		}
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
		"authorized_at":           now,
		"status":                  model.ClaimReviewPending,
		"updated_at":              now,
	})

	s.db.First(&claim, claimID)
	response.OK(c, s.claimView(claim))
}

// claimView 认领申请视图
func (s *Server) claimView(claim model.DistributorApplication) gin.H {
	platforms := parsePlatforms(claim.Platforms)
	v := gin.H{
		"claim_id":                claim.ID,
		"application_no":          claim.ApplicationNo,
		"drama_id":                claim.DramaID,
		"platforms":               platforms,
		"platform":                platforms, // 文档字段名，与 platforms 同义
		"deposit_amount_cents":    claim.DepositAmountCents,
		"deposit_status":          claim.DepositStatus,
		"authorization_confirmed": claim.AuthorizationConfirmed,
		"review_status":           claim.Status,
		"contract_status":         claim.ContractStatus,
		"contract_file_url":       s.contractPresignedURL(claim.ContractFileKey),
		"reject_reason":           claim.RejectReason,
		"completed_at":            claim.CompletedAt,
		"created_at":              claim.CreatedAt,
		"reviewed_at":             claim.ReviewedAt,
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
