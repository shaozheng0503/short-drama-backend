package handler

import (
	"errors"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"
	"ai-drama-platform/internal/secure"
	"ai-drama-platform/internal/sms"

	"github.com/gin-gonic/gin"
)

var (
	errInvalidBankCard        = errors.New("bank_card_no 必须是 16-19 位数字")
	errSMSRequiredForBankCard = errors.New("修改银行卡号需先调用 POST /creator/bank-card/send-sms 获取验证码")
	errSMSInvalidForBankCard  = errors.New("短信验证码错误或已过期")
)

type personalVerificationRequest struct {
	Name       string `json:"name" binding:"required"`
	IDCardNo   string `json:"id_card_no" binding:"required"`
	BankName   string `json:"bank_name" binding:"required"`
	BankBranch string `json:"bank_branch"`
	BankCardNo string `json:"bank_card_no" binding:"required"`
	SMSCode    string `json:"sms_code"`
}

type enterpriseVerificationRequest struct {
	OrgName            string `json:"org_name" binding:"required"`
	OrgCreditCode      string `json:"org_credit_code" binding:"required"`
	BusinessLicenseURL string `json:"business_license_url" binding:"required"`
	BankLicenseURL     string `json:"bank_license_url"`
	BankName           string `json:"bank_name" binding:"required"`
	BankBranch         string `json:"bank_branch"`
	BankCardNo         string `json:"bank_card_no" binding:"required"`
	SMSCode            string `json:"sms_code"`
}

type bankCardChangeRequest struct {
	BankName   string `json:"bank_name" binding:"required"`
	BankBranch string `json:"bank_branch"`
	BankCardNo string `json:"bank_card_no" binding:"required"`
	SMSCode    string `json:"sms_code" binding:"required"`
}

func (s *Server) creatorGetVerification(c *gin.Context) {
	cid := middleware.CurrentID(c)
	var creator model.Creator
	if err := s.db.First(&creator, cid).Error; err != nil {
		response.ServerError(c, "查询认证信息失败")
		return
	}
	response.OK(c, gin.H{
		"creator_type":         creator.CreatorType,
		"verify_status":        creator.VerifyStatus,
		"verify_reject_reason": creator.VerifyRejectReason,
		"verify_submitted_at":  creator.VerifySubmittedAt,
		"real_name_info":       creatorFullView(creator)["real_name_info"],
		"enterprise_info":      creatorFullView(creator)["enterprise_info"],
	})
}

func (s *Server) creatorUpdatePersonalVerification(c *gin.Context) {
	cid := middleware.CurrentID(c)
	var req personalVerificationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "真实姓名、身份证号、开户银行、银行卡号必填")
		return
	}
	if !idCardRegex.MatchString(req.IDCardNo) {
		response.InvalidParam(c, "id_card_no 必须是 18 位身份证号（末位可为 X）")
		return
	}
	if err := s.validateBankCardChange(cid, req.BankCardNo, req.SMSCode); err != nil {
		response.InvalidParam(c, err.Error())
		return
	}
	encID, err := s.cryptor.Encrypt(req.IDCardNo)
	if err != nil {
		response.ServerError(c, "身份证加密失败")
		return
	}
	encBank, err := s.cryptor.Encrypt(req.BankCardNo)
	if err != nil {
		response.ServerError(c, "银行卡加密失败")
		return
	}
	updates := map[string]interface{}{
		"creator_type":         model.CreatorTypePersonal,
		"name":                 req.Name,
		"id_card_no_enc":       encID,
		"id_card_no_masked":    maskIDCard(req.IDCardNo),
		"bank_name":            req.BankName,
		"bank_branch":          req.BankBranch,
		"bank_card_no_enc":     encBank,
		"bank_card_last4":      secure.Last4(req.BankCardNo),
		"bank_card_no_masked":  maskBankCard(req.BankCardNo),
		"org_name":             "",
		"org_credit_code":      "",
		"business_license_url": "",
		"bank_license_url":     "",
		"verify_status":        model.CreatorVerifyPending,
		"verify_reject_reason": "",
		"verify_submitted_at":  nowTimePtr(),
	}
	if err := s.db.Model(&model.Creator{}).Where("id = ?", cid).Updates(updates).Error; err != nil {
		response.ServerError(c, "保存个人实名失败")
		return
	}
	s.creatorGetVerification(c)
}

func (s *Server) creatorUpdateEnterpriseVerification(c *gin.Context) {
	cid := middleware.CurrentID(c)
	var req enterpriseVerificationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "企业名称、统一社会信用代码、营业执照、开户银行、银行卡号必填")
		return
	}
	if !creditCodeRegex.MatchString(req.OrgCreditCode) {
		response.InvalidParam(c, "org_credit_code 必须是 18 位大写字母/数字统一社会信用代码")
		return
	}
	if len(req.BusinessLicenseURL) > 512 {
		response.InvalidParam(c, "business_license_url 过长")
		return
	}
	if len(req.BankLicenseURL) > 512 {
		response.InvalidParam(c, "bank_license_url 过长")
		return
	}
	if err := s.validateBankCardChange(cid, req.BankCardNo, req.SMSCode); err != nil {
		response.InvalidParam(c, err.Error())
		return
	}
	encBank, err := s.cryptor.Encrypt(req.BankCardNo)
	if err != nil {
		response.ServerError(c, "银行卡加密失败")
		return
	}
	updates := map[string]interface{}{
		"creator_type":         model.CreatorTypeOrganization,
		"name":                 "",
		"id_card_no_enc":       "",
		"id_card_no_masked":    "",
		"org_name":             req.OrgName,
		"org_credit_code":      req.OrgCreditCode,
		"business_license_url": req.BusinessLicenseURL,
		"bank_license_url":     req.BankLicenseURL,
		"bank_name":            req.BankName,
		"bank_branch":          req.BankBranch,
		"bank_card_no_enc":     encBank,
		"bank_card_last4":      secure.Last4(req.BankCardNo),
		"bank_card_no_masked":  maskBankCard(req.BankCardNo),
		"verify_status":        model.CreatorVerifyPending,
		"verify_reject_reason": "",
		"verify_submitted_at":  nowTimePtr(),
	}
	if err := s.db.Model(&model.Creator{}).Where("id = ?", cid).Updates(updates).Error; err != nil {
		response.ServerError(c, "保存企业认证失败")
		return
	}
	s.creatorGetVerification(c)
}

func (s *Server) creatorSendBankCardSMS(c *gin.Context) {
	cid := middleware.CurrentID(c)
	var creator model.Creator
	if err := s.db.First(&creator, cid).Error; err != nil {
		response.ServerError(c, "查询创作者失败")
		return
	}
	if !s.sms.AllowSendByIP(c.ClientIP()) {
		response.Fail(c, response.CodeRateLimited, "发送过于频繁，请稍后再试")
		return
	}
	code, err := s.sms.Send(creator.Phone, model.SMSSceneBankCardChange)
	if err != nil {
		switch {
		case errors.Is(err, sms.ErrTooFrequent):
			response.Conflict(c, "发送过于频繁，请 60 秒后重试")
		case errors.Is(err, sms.ErrProviderFail):
			response.Fail(c, response.CodeThirdPartyError, "短信网关下发失败，请稍后重试")
		default:
			response.ServerError(c, "短信发送失败")
		}
		return
	}
	data := gin.H{
		"expire_seconds": int(s.cfg.SMSCodeTTL.Seconds()),
		"phone_masked":   sms.MaskPhone(creator.Phone),
	}
	if s.cfg.SMSDevMode {
		data["dev_code"] = code
	}
	response.OK(c, data)
}

func (s *Server) creatorChangeBankCard(c *gin.Context) {
	cid := middleware.CurrentID(c)
	var req bankCardChangeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "开户银行、银行卡号、短信验证码必填")
		return
	}
	if err := s.validateBankCardChange(cid, req.BankCardNo, req.SMSCode); err != nil {
		response.InvalidParam(c, err.Error())
		return
	}
	enc, err := s.cryptor.Encrypt(req.BankCardNo)
	if err != nil {
		response.ServerError(c, "银行卡加密失败")
		return
	}
	if err := s.db.Model(&model.Creator{}).Where("id = ?", cid).Updates(map[string]interface{}{
		"bank_name":           req.BankName,
		"bank_branch":         req.BankBranch,
		"bank_card_no_enc":    enc,
		"bank_card_last4":     secure.Last4(req.BankCardNo),
		"bank_card_no_masked": maskBankCard(req.BankCardNo),
	}).Error; err != nil {
		response.ServerError(c, "修改银行卡失败")
		return
	}
	s.creatorGetVerification(c)
}

func (s *Server) validateBankCardChange(creatorID uint64, bankCardNo, smsCode string) error {
	if !bankCardRegex.MatchString(bankCardNo) {
		return errInvalidBankCard
	}
	var creator model.Creator
	if err := s.db.First(&creator, creatorID).Error; err != nil {
		return errInvalidBankCard
	}
	if creator.BankCardNoEnc != "" && creator.BankCardNoMasked != "" && maskBankCard(bankCardNo) != creator.BankCardNoMasked {
		if smsCode == "" {
			return errSMSRequiredForBankCard
		}
		if err := s.sms.Verify(creator.Phone, model.SMSSceneBankCardChange, smsCode); err != nil {
			return errSMSInvalidForBankCard
		}
	}
	return nil
}
