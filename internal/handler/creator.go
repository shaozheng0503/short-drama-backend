package handler

import (
	"errors"
	"strings"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"
	"ai-drama-platform/internal/sms"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type creatorLoginRequest struct {
	Phone string `json:"phone" binding:"required"`
	Code  string `json:"code" binding:"required"`
}

func (s *Server) creatorLogin(c *gin.Context) {
	var req creatorLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "phone 与 code 必填")
		return
	}
	if !sms.ValidPhone(req.Phone) {
		response.InvalidParam(c, "手机号格式不正确")
		return
	}
	if err := s.sms.Verify(req.Phone, model.SMSSceneCreatorLogin, req.Code); err != nil {
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

	creator, err := s.findOrCreateCreator(req.Phone)
	if err != nil {
		response.ServerError(c, "登录失败")
		return
	}
	if creator.Status == model.StatusBanned {
		response.Forbidden(c, "账号已被封禁")
		return
	}

	token, _, err := middleware.IssueToken(s.cfg, middleware.SubjectCreator, creator.ID)
	if err != nil {
		response.ServerError(c, "签发 token 失败")
		return
	}

	response.OK(c, gin.H{
		"token":   token,
		"creator": creatorBriefView(creator),
	})
}

func (s *Server) creatorMe(c *gin.Context) {
	cid := middleware.CurrentID(c)
	var creator model.Creator
	if err := s.db.First(&creator, cid).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "创作者不存在")
			return
		}
		response.ServerError(c, "获取创作者失败")
		return
	}
	response.OK(c, creatorDetailView(creator))
}

func (s *Server) findOrCreateCreator(phone string) (model.Creator, error) {
	var creator model.Creator
	err := s.db.Where("phone = ?", phone).First(&creator).Error
	if err == nil {
		return creator, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return creator, err
	}
	creator = model.Creator{
		Phone:        phone,
		CreatorType:  model.CreatorTypePersonal,
		VerifyStatus: model.CreatorVerifyUnverified,
		Status:       model.StatusActive,
	}
	applyCreatorDefaults(&creator)
	if err := s.db.Create(&creator).Error; err != nil {
		return creator, err
	}
	return creator, nil
}

func creatorBriefView(cr model.Creator) gin.H {
	return gin.H{
		"id":                   cr.ID,
		"phone":                sms.MaskPhone(cr.Phone),
		"login_phone":          sms.MaskPhone(cr.Phone),
		"nickname":             cr.Nickname,
		"avatar_url":           creatorAvatarURL(cr),
		"account_uid":          cr.AccountUID,
		"region":               cr.Region,
		"identity_mid":         cr.IdentityMID,
		"identity_role":        cr.IdentityRole,
		"creator_type":         cr.CreatorType,
		"org_name":             cr.OrgName,
		"org_credit_code":      cr.OrgCreditCode,
		"business_license_url": cr.BusinessLicenseURL,
		"verify_status":        cr.VerifyStatus,
		"id_card_no_masked":    cr.IDCardNoMasked,
		"bank_card_no_masked":  cr.BankCardNoMasked,
	}
}

func creatorDetailView(cr model.Creator) gin.H {
	maskedBank := cr.BankCardNoMasked
	if maskedBank == "" && cr.BankCardLast4 != "" {
		maskedBank = "***" + cr.BankCardLast4
	}
	return gin.H{
		"id":                   cr.ID,
		"phone":                sms.MaskPhone(cr.Phone),
		"login_phone":          sms.MaskPhone(cr.Phone),
		"name":                 cr.Name,
		"nickname":             cr.Nickname,
		"avatar_url":           creatorAvatarURL(cr),
		"account_uid":          cr.AccountUID,
		"region":               cr.Region,
		"creator_type":         cr.CreatorType,
		"org_name":             cr.OrgName,
		"org_credit_code":      cr.OrgCreditCode,
		"business_license_url": cr.BusinessLicenseURL,
		"identity_mid":         cr.IdentityMID,
		"identity_role":        cr.IdentityRole,
		"bank_name":            cr.BankName,
		"id_card_no_masked":    cr.IDCardNoMasked,
		"bank_card_no_masked":  cr.BankCardNoMasked,
		"verify_status":        cr.VerifyStatus,
		"total_income_cents":   cr.TotalIncomeCents,
		"balance_cents":        cr.BalanceCents,
		"frozen_cents":         cr.FrozenCents,
		"status":               cr.Status,
		"account_info": gin.H{
			"avatar_url":  creatorAvatarURL(cr),
			"nickname":    cr.Nickname,
			"account_uid": cr.AccountUID,
			"login_phone": sms.MaskPhone(cr.Phone),
			"region":      cr.Region,
		},
		"real_name_info": gin.H{
			"real_name":           cr.Name,
			"id_card_no_masked":   cr.IDCardNoMasked,
			"bank_name":           cr.BankName,
			"bank_card_no_masked": maskedBank,
		},
		"enterprise_info": gin.H{
			"org_name":             cr.OrgName,
			"org_credit_code":      cr.OrgCreditCode,
			"business_license_url": cr.BusinessLicenseURL,
		},
		"identity_info": gin.H{
			"identity_mid":  cr.IdentityMID,
			"identity_role": cr.IdentityRole,
		},
	}
}

const defaultCreatorAvatarURL = "https://api.dicebear.com/7.x/initials/svg?seed=creator"

func defaultCreatorNickname(phone string) string {
	if len(phone) >= 4 {
		return "创作者" + phone[len(phone)-4:]
	}
	return "创作者"
}

func creatorDisplayName(cr model.Creator) string {
	if nickname := strings.TrimSpace(cr.Nickname); nickname != "" {
		return nickname
	}
	if name := strings.TrimSpace(cr.Name); name != "" {
		return name
	}
	return defaultCreatorNickname(cr.Phone)
}

func creatorAvatarURL(cr model.Creator) string {
	if cr.AvatarURL != "" {
		return cr.AvatarURL
	}
	return defaultCreatorAvatarURL
}

func defaultCreatorUID(phone string) string {
	if phone == "" {
		return ""
	}
	return "MID" + phone
}

// applyCreatorDefaults 补齐创作者注册时的账号 UID / 昵称等默认值。
func applyCreatorDefaults(cr *model.Creator) {
	if cr.Nickname == "" {
		cr.Nickname = defaultCreatorNickname(cr.Phone)
	}
	if cr.AvatarURL == "" {
		cr.AvatarURL = defaultCreatorAvatarURL
	}
	if cr.AccountUID == "" {
		cr.AccountUID = defaultCreatorUID(cr.Phone)
	}
	if cr.IdentityMID == "" {
		cr.IdentityMID = defaultCreatorUID(cr.Phone)
	}
	if cr.IdentityRole == "" {
		cr.IdentityRole = "版权人"
	}
	if cr.CreatorType == "" {
		cr.CreatorType = model.CreatorTypePersonal
	}
}

func creatorDisplayUID(cr model.Creator) string {
	if cr.IdentityMID != "" {
		return cr.IdentityMID
	}
	if cr.AccountUID != "" {
		return cr.AccountUID
	}
	return defaultCreatorUID(cr.Phone)
}
