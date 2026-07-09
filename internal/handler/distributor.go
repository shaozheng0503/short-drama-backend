package handler

import (
	"errors"
	"fmt"
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"
	"ai-drama-platform/internal/sms"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ============================================================
// 发行商（Distributor）认证 & 个人信息
// ============================================================

type distributorLoginRequest struct {
	Phone string `json:"phone" binding:"required"`
	Code  string `json:"code" binding:"required"`
}

// POST /v1/distributor/auth/login
func (s *Server) distributorLogin(c *gin.Context) {
	var req distributorLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "phone 与 code 必填")
		return
	}
	if !sms.ValidPhone(req.Phone) {
		response.InvalidParam(c, "手机号格式不正确")
		return
	}
	if err := s.sms.Verify(req.Phone, model.SMSSceneDistributorLogin, req.Code); err != nil {
		switch {
		case errors.Is(err, sms.ErrCodeMismatch):
			response.InvalidParam(c, "验证码错误或已过期")
		case errors.Is(err, sms.ErrTooManyAttempts):
			response.Fail(c, response.CodeRateLimited, "验证码尝试次数过多，请稍后再试")
		default:
			response.ServerError(c, "校验验证码失败")
		}
		return
	}

	dist, err := s.findOrCreateDistributor(req.Phone)
	if err != nil {
		response.ServerError(c, "登录失败")
		return
	}
	if dist.Status == model.StatusBanned {
		response.Forbidden(c, "账号已被封禁")
		return
	}

	token, _, err := middleware.IssueToken(s.cfg, middleware.SubjectDistributor, dist.ID)
	if err != nil {
		response.ServerError(c, "签发 token 失败")
		return
	}

	response.OK(c, gin.H{
		"token":       token,
		"distributor": distributorBriefView(dist),
	})
}

func (s *Server) findOrCreateDistributor(phone string) (*model.Distributor, error) {
	var d model.Distributor
	err := s.db.Where("phone = ?", phone).First(&d).Error
	if err == nil {
		return &d, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	// 自动注册
	d = model.Distributor{
		Phone:        phone,
		Name:         "",
		VerifyStatus: model.DistributorVerifyUnverified,
		Status:       model.StatusActive,
	}
	if err := s.db.Create(&d).Error; err != nil {
		return nil, err
	}
	// 生成发行商编号 DN + 6位ID
	d.Name = fmt.Sprintf("发行商%d", d.ID)
	s.db.Model(&d).Update("name", d.Name)
	return &d, nil
}

// GET /v1/distributor/me
func (s *Server) distributorMe(c *gin.Context) {
	id := middleware.CurrentID(c)
	var d model.Distributor
	if err := s.db.First(&d, id).Error; err != nil {
		response.NotFound(c, "发行商不存在")
		return
	}
	response.OK(c, distributorDetailView(&d))
}

// PUT /v1/distributor/me
func (s *Server) distributorUpdateMe(c *gin.Context) {
	id := middleware.CurrentID(c)
	var d model.Distributor
	if err := s.db.First(&d, id).Error; err != nil {
		response.NotFound(c, "发行商不存在")
		return
	}
	var req struct {
		Name      *string `json:"name"`
		Nickname  *string `json:"nickname"`
		AvatarURL *string `json:"avatar_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "参数格式错误")
		return
	}
	updates := map[string]interface{}{}
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Nickname != nil {
		updates["nickname"] = *req.Nickname
	}
	if req.AvatarURL != nil {
		updates["avatar_url"] = *req.AvatarURL
	}
	if len(updates) > 0 {
		s.db.Model(&d).Updates(updates)
	}
	s.db.First(&d, id)
	response.OK(c, distributorDetailView(&d))
}

// ============================================================
// 视图函数
// ============================================================

func distributorBriefView(d *model.Distributor) gin.H {
	return gin.H{
		"id":           d.ID,
		"phone":        d.Phone,
		"name":         d.Name,
		"nickname":     d.Nickname,
		"avatar_url":   d.AvatarURL,
		"verify_status": d.VerifyStatus,
		"status":       d.Status,
	}
}

func distributorDetailView(d *model.Distributor) gin.H {
	return gin.H{
		"id":                     d.ID,
		"phone":                  d.Phone,
		"name":                   d.Name,
		"nickname":               d.Nickname,
		"avatar_url":             d.AvatarURL,
		"org_name":               d.OrgName,
		"org_credit_code":        d.OrgCreditCode,
		"org_legal_person":       d.OrgLegalPerson,
		"org_legal_id_card_masked": d.OrgLegalIDCardMasked,
		"business_license_url":   d.BusinessLicenseURL,
		"bank_license_url":       d.BankLicenseURL,
		"bank_name":              d.BankName,
		"bank_branch":            d.BankBranch,
		"bank_card_no_masked":    d.BankCardNoMasked,
		"verify_status":          d.VerifyStatus,
		"verify_reject_reason":   d.VerifyRejectReason,
		"verify_reject_fields":   d.VerifyRejectFields,
		"verify_submitted_at":    d.VerifySubmittedAt,
		"verify_checked_at":      d.VerifyCheckedAt,
		"deposit_available_cents": d.DepositAvailableCents,
		"deposit_frozen_cents":   d.DepositFrozenCents,
		"total_income_cents":     d.TotalIncomeCents,
		"balance_cents":          d.BalanceCents,
		"frozen_cents":           d.FrozenCents,
		"status":                 d.Status,
		"created_at":             d.CreatedAt,
	}
}

// ============================================================
// 保证金余额 & 流水
// ============================================================

// GET /v1/distributor/deposit/balance
func (s *Server) distributorDepositBalance(c *gin.Context) {
	id := middleware.CurrentID(c)
	var d model.Distributor
	if err := s.db.Select("deposit_available_cents, deposit_frozen_cents, balance_cents, frozen_cents, total_income_cents").First(&d, id).Error; err != nil {
		response.NotFound(c, "发行商不存在")
		return
	}
	response.OK(c, gin.H{
		"deposit_available_cents": d.DepositAvailableCents,
		"deposit_frozen_cents":    d.DepositFrozenCents,
		"balance_cents":           d.BalanceCents,
		"frozen_cents":            d.FrozenCents,
		"total_income_cents":      d.TotalIncomeCents,
		"updated_at":              time.Now(),
	})
}
