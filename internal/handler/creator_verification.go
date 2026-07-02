package handler

import (
	"errors"
	"log"
	"strings"
	"time"

	"ai-drama-platform/internal/kyc"
	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"
	"ai-drama-platform/internal/secure"
	"ai-drama-platform/internal/sms"

	"github.com/gin-gonic/gin"
)

var (
	errInvalidBankCard        = errors.New("bank_card_no 必须是 16-19 位数字")
	errInvalidPublicAccount   = errors.New("bank_card_no 企业对公账号必须是 9-30 位数字")
	errSMSRequiredForBankCard = errors.New("修改银行卡号需先调用 POST /creator/bank-card/send-sms 获取验证码")
	errSMSInvalidForBankCard  = errors.New("短信验证码错误或已过期")
)

type personalVerificationRequest struct {
	Name       string `json:"name" binding:"required"`
	IDCardNo   string `json:"id_card_no"` // 重新提交可不传：留空沿用库里已存值（首次提交必填）
	BankName   string `json:"bank_name" binding:"required"`
	BankBranch string `json:"bank_branch"`
	BankCardNo string `json:"bank_card_no"` // 重新提交可不传：留空沿用库里已存值（首次提交必填）
	SMSCode    string `json:"sms_code"`     // 已废弃于认证提交流程：短信门只在 verified 后改卡时生效，这里保留兼容旧前端
}

type enterpriseVerificationRequest struct {
	OrgName            string `json:"org_name" binding:"required"`
	OrgCreditCode      string `json:"org_credit_code" binding:"required"`
	OrgLegalPerson     string `json:"org_legal_person" binding:"required"` // 法定代表人姓名（四要素核验项）
	OrgLegalIDCard     string `json:"org_legal_id_card"`                   // 法人证件号；重新提交可不传：留空沿用库里已存值（首次提交必填）
	BusinessLicenseURL string `json:"business_license_url" binding:"required"`
	BankLicenseURL     string `json:"bank_license_url"`
	BankName           string `json:"bank_name" binding:"required"`
	BankBranch         string `json:"bank_branch"`
	BankCardNo         string `json:"bank_card_no"` // 对公账号；重新提交可不传：留空沿用库里已存值（首次提交必填）
	SMSCode            string `json:"sms_code"`     // 已废弃于认证提交流程，保留兼容
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
		"verify_reject_fields": splitRejectFields(creator.VerifyRejectFields),
		"verify_method":        creator.VerifyMethod,
		"verify_checked_at":    creator.VerifyCheckedAt,
		"verify_submitted_at":  creator.VerifySubmittedAt,
		"real_name_info":       creatorFullView(creator)["real_name_info"],
		"enterprise_info":      creatorFullView(creator)["enterprise_info"],
	})
}

func (s *Server) creatorUpdatePersonalVerification(c *gin.Context) {
	cid := middleware.CurrentID(c)
	var req personalVerificationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "真实姓名、开户银行必填")
		return
	}
	var creator model.Creator
	if err := s.db.First(&creator, cid).Error; err != nil {
		response.ServerError(c, "查询创作者失败")
		return
	}
	// ⑦ 状态护栏：已认证不允许整份重交（改打款卡走 /bank-card/change）。
	if creator.VerifyStatus == model.CreatorVerifyVerified {
		response.Conflict(c, "已通过实名认证，如需变更打款银行卡请走「修改银行卡」入口")
		return
	}

	// ② 身份证号：传了才校验+更新；不传沿用库里（重新提交常见）。核验需明文，缺失时解密库里值。
	idCard := strings.TrimSpace(req.IDCardNo)
	encID := creator.IDCardNoEnc
	if idCard != "" {
		if !idCardRegex.MatchString(idCard) {
			response.InvalidParam(c, "id_card_no 必须是 18 位身份证号（末位可为 X）")
			return
		}
		enc, err := s.cryptor.Encrypt(idCard)
		if err != nil {
			response.ServerError(c, "身份证加密失败")
			return
		}
		encID = enc
	} else {
		if creator.IDCardNoEnc == "" {
			response.InvalidParam(c, "首次提交必须填写身份证号")
			return
		}
		dec, err := s.cryptor.Decrypt(creator.IDCardNoEnc)
		if err != nil {
			response.ServerError(c, "读取身份证号失败")
			return
		}
		idCard = dec
	}

	// ② 银行卡号：传了才校验+更新；不传沿用库里。① 认证提交只校验格式，不走短信门。
	bankCard := strings.TrimSpace(req.BankCardNo)
	encBank, bankLast4, bankMasked := creator.BankCardNoEnc, creator.BankCardLast4, creator.BankCardNoMasked
	if bankCard != "" {
		if err := validateBankCardFormat(bankCard, false); err != nil {
			response.InvalidParam(c, err.Error())
			return
		}
		enc, err := s.cryptor.Encrypt(bankCard)
		if err != nil {
			response.ServerError(c, "银行卡加密失败")
			return
		}
		encBank, bankLast4, bankMasked = enc, secure.Last4(bankCard), maskBankCard(bankCard)
	} else {
		if creator.BankCardNoEnc == "" {
			response.InvalidParam(c, "首次提交必须填写银行卡号")
			return
		}
		dec, err := s.cryptor.Decrypt(creator.BankCardNoEnc)
		if err != nil {
			response.ServerError(c, "读取银行卡号失败")
			return
		}
		bankCard = dec
	}

	// ⑧ 姓名/身份证/银行卡都没变且上次已三要素通过 → 复用结果，跳过重核验省调用费。
	var verifyMethod, providerResult string
	var checkedAt *time.Time
	if req.Name == creator.Name && req.IDCardNo == "" && req.BankCardNo == "" && creator.VerifyMethod == "bankcard3" {
		verifyMethod, providerResult, checkedAt = creator.VerifyMethod, creator.VerifyProviderResult, creator.VerifyCheckedAt
	} else {
		// 银行卡三要素核验（姓名+身份证号+银行卡号，已隐含实名一致）。dev provider 跳过；第三方异常降级人工审核。
		var blockMsg string
		verifyMethod, providerResult, checkedAt, blockMsg = s.runBankCard3Verify(c, req.Name, idCard, bankCard)
		if blockMsg != "" {
			response.InvalidParam(c, blockMsg)
			return
		}
	}
	// 个人实名走 API 核验：真实核验通过即免人工复核直接 verified；
	// dev / 第三方异常降级（method=manual）仍走 pending 人工兜底，避免没真正核验就放行。
	verifyStatus := personalVerifyStatusFor(verifyMethod)
	updates := map[string]interface{}{
		"creator_type":             model.CreatorTypePersonal,
		"name":                     req.Name,
		"id_card_no_enc":           encID,
		"id_card_no_masked":        maskIDCard(idCard),
		"bank_name":                req.BankName,
		"bank_branch":              req.BankBranch,
		"bank_card_no_enc":         encBank,
		"bank_card_last4":          bankLast4,
		"bank_card_no_masked":      bankMasked,
		"org_name":                 "",
		"org_credit_code":          "",
		"org_legal_person":         "",
		"org_legal_id_card_enc":    "", // ⑨ 切换为个人时一并清空企业法人证件号残留
		"org_legal_id_card_masked": "",
		"business_license_url":     "",
		"bank_license_url":         "",
		"verify_status":            verifyStatus,
		"verify_reject_reason":     "",
		"verify_reject_fields":     "",
		"verify_submitted_at":      nowTimePtr(),
		"verify_method":            verifyMethod,
		"verify_provider_result":   providerResult,
		"verify_checked_at":        checkedAt,
	}
	if err := s.db.Model(&model.Creator{}).Where("id = ?", cid).Updates(updates).Error; err != nil {
		response.ServerError(c, "保存个人实名失败")
		return
	}
	s.creatorGetVerification(c)
}

// personalVerifyStatusFor 个人实名核验后状态：银行卡三要素真实核验通过 → 直接 verified（免人工复核）；
// 其余（dev / 第三方异常降级，method=manual）→ pending 走 Admin 人工兜底。
func personalVerifyStatusFor(verifyMethod string) string {
	if verifyMethod == "bankcard3" {
		return model.CreatorVerifyVerified
	}
	return model.CreatorVerifyPending
}

// runBankCard3Verify 调银行卡三要素核验。返回 (verify_method, 存档结果, 核验时间, 拦截消息)。
// dev provider 跳过（method=manual，不拦截）；第三方异常降级人工审核（method=manual，不拦截）；
// 核验未通过返回非空 blockMsg，由调用方拒绝。
func (s *Server) runBankCard3Verify(c *gin.Context, name, idCard, bankCard string) (method, result string, checkedAt *time.Time, blockMsg string) {
	if s.kyc.Name() == "dev" {
		return "manual", "", nil, ""
	}
	res, err := s.kyc.VerifyBankCard3(c.Request.Context(), kyc.BankCard3Input{Name: name, IDCard: idCard, BankCard: bankCard})
	if err != nil {
		log.Printf("[verify] 银行卡三要素核验异常，降级人工审核 err=%v", err)
		return "manual", "", nil, ""
	}
	if !res.Passed {
		msg := res.Description
		if msg == "" {
			msg = "姓名、身份证号、银行卡号信息不一致"
		}
		return "", "", nil, "实名核验未通过：" + msg
	}
	now := time.Now()
	return "bankcard3", res.Description, &now, ""
}

func (s *Server) creatorUpdateEnterpriseVerification(c *gin.Context) {
	cid := middleware.CurrentID(c)
	var req enterpriseVerificationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "企业名称、统一社会信用代码、法人姓名、营业执照、开户银行必填")
		return
	}
	var creator model.Creator
	if err := s.db.First(&creator, cid).Error; err != nil {
		response.ServerError(c, "查询创作者失败")
		return
	}
	// ⑦ 状态护栏：已认证不允许整份重交（改打款账户走 /bank-card/change）。
	if creator.VerifyStatus == model.CreatorVerifyVerified {
		response.Conflict(c, "已通过企业认证，如需变更对公打款账户请走「修改银行卡」入口")
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

	// ② 法人证件号：传了才校验+更新；不传沿用库里。四要素核验需明文，缺失时解密库里值。
	legalIDNum := strings.TrimSpace(req.OrgLegalIDCard)
	encLegalID := creator.OrgLegalIDCardEnc
	if legalIDNum != "" {
		if !idCardRegex.MatchString(legalIDNum) {
			response.InvalidParam(c, "org_legal_id_card 必须是 18 位法人身份证号（末位可为 X）")
			return
		}
		enc, err := s.cryptor.Encrypt(legalIDNum)
		if err != nil {
			response.ServerError(c, "法人身份证加密失败")
			return
		}
		encLegalID = enc
	} else {
		if creator.OrgLegalIDCardEnc == "" {
			response.InvalidParam(c, "首次提交必须填写法人身份证号")
			return
		}
		dec, err := s.cryptor.Decrypt(creator.OrgLegalIDCardEnc)
		if err != nil {
			response.ServerError(c, "读取法人身份证号失败")
			return
		}
		legalIDNum = dec
	}

	// ② 对公账号：传了才校验+更新；不传沿用库里。① 认证提交只校验格式，不走短信门。
	bankAccount := strings.TrimSpace(req.BankCardNo)
	encBank, bankLast4, bankMasked := creator.BankCardNoEnc, creator.BankCardLast4, creator.BankCardNoMasked
	if bankAccount != "" {
		if err := validateBankCardFormat(bankAccount, true); err != nil {
			response.InvalidParam(c, err.Error())
			return
		}
		enc, err := s.cryptor.Encrypt(bankAccount)
		if err != nil {
			response.ServerError(c, "银行卡加密失败")
			return
		}
		encBank, bankLast4, bankMasked = enc, secure.Last4(bankAccount), maskBankCard(bankAccount)
	} else {
		if creator.BankCardNoEnc == "" {
			response.InvalidParam(c, "首次提交必须填写对公账号")
			return
		}
	}

	// ⑧ 工商四项（企业名/信用代码/法人姓名/法人证件号）都没变且上次已 biz_4e 通过
	// → 复用核验结果，跳过重核验，省第三方调用费（也少一次失败/降级机会）。
	var verifyMethod, providerResult string
	var checkedAt *time.Time
	orgUnchanged := req.OrgLegalIDCard == "" &&
		req.OrgName == creator.OrgName &&
		req.OrgCreditCode == creator.OrgCreditCode &&
		req.OrgLegalPerson == creator.OrgLegalPerson
	if orgUnchanged && creator.VerifyMethod == "biz_4e" {
		verifyMethod, providerResult, checkedAt = creator.VerifyMethod, creator.VerifyProviderResult, creator.VerifyCheckedAt
	} else {
		// 企业四要素核验（企业名+统一社会信用代码+法人姓名+法人证件号）。dev 跳过；第三方异常降级人工。
		var blockMsg string
		verifyMethod, providerResult, checkedAt, blockMsg = s.runBizLicense4Verify(c, req.OrgName, req.OrgCreditCode, req.OrgLegalPerson, legalIDNum)
		if blockMsg != "" {
			response.InvalidParam(c, blockMsg)
			return
		}
	}

	// 企业认证保留人工复核：对公账户靠 Admin 人工核对，四要素核验通过仍进 pending。
	updates := map[string]interface{}{
		"creator_type":             model.CreatorTypeOrganization,
		"name":                     "",
		"id_card_no_enc":           "",
		"id_card_no_masked":        "",
		"org_name":                 req.OrgName,
		"org_credit_code":          req.OrgCreditCode,
		"org_legal_person":         req.OrgLegalPerson,
		"org_legal_id_card_enc":    encLegalID,
		"org_legal_id_card_masked": maskIDCard(legalIDNum),
		"business_license_url":     req.BusinessLicenseURL,
		"bank_license_url":         req.BankLicenseURL,
		"bank_name":                req.BankName,
		"bank_branch":              req.BankBranch,
		"bank_card_no_enc":         encBank,
		"bank_card_last4":          bankLast4,
		"bank_card_no_masked":      bankMasked,
		"verify_status":            model.CreatorVerifyPending,
		"verify_reject_reason":     "",
		"verify_reject_fields":     "",
		"verify_submitted_at":      nowTimePtr(),
		"verify_method":            verifyMethod,
		"verify_provider_result":   providerResult,
		"verify_checked_at":        checkedAt,
	}
	if err := s.db.Model(&model.Creator{}).Where("id = ?", cid).Updates(updates).Error; err != nil {
		response.ServerError(c, "保存企业认证失败")
		return
	}
	s.creatorGetVerification(c)
}

// runBizLicense4Verify 企业四要素核验（企业名+信用代码+法人姓名+法人证件号）。返回 (verify_method, 存档结果, 核验时间, 拦截消息)。
// dev 跳过（method=manual，不拦截）；第三方异常 / 系统不可用 → 降级人工审核（manual，不拦截）；
// 四要素不完全匹配 → 返回非空 blockMsg 列出不一致项，拒绝。
func (s *Server) runBizLicense4Verify(c *gin.Context, orgName, creditCode, legalName, legalIDNum string) (method, result string, checkedAt *time.Time, blockMsg string) {
	if s.kyc.Name() == "dev" {
		return "manual", "", nil, ""
	}
	res, err := s.kyc.VerifyBizLicense4(c.Request.Context(), kyc.BizLicense4Input{
		EntName: orgName, CreditCode: creditCode, LegalName: legalName, LegalIDNum: legalIDNum,
	})
	if err != nil || !res.Available {
		log.Printf("[verify] 企业四要素核验异常/不可用，降级人工审核 err=%v", err)
		return "manual", "", nil, ""
	}
	if !res.Passed {
		var bad []string
		if !res.EntNameOK {
			bad = append(bad, "企业名称")
		}
		if !res.CreditCodeOK {
			bad = append(bad, "统一社会信用代码")
		}
		if !res.LegalNameOK {
			bad = append(bad, "法人姓名")
		}
		if !res.LegalIDNumOK {
			bad = append(bad, "法人证件号")
		}
		msg := "企业四要素核验未通过"
		if len(bad) > 0 {
			msg += "：" + strings.Join(bad, "、") + " 与工商登记不一致"
		}
		return "", "", nil, msg
	}
	now := time.Now()
	return "biz_4e", res.Raw, &now, ""
}

// creatorBizLicenseOCR 营业执照 OCR 识别，供前端上传执照后自动回填企业名/信用代码/法人，减少手填。
// 仅识别不核验真伪；真伪由提交时的四要素核验把关。POST /v1/creator/verification/biz-license/ocr
func (s *Server) creatorBizLicenseOCR(c *gin.Context) {
	var req struct {
		BusinessLicenseURL string `json:"business_license_url" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.BusinessLicenseURL) == "" {
		response.InvalidParam(c, "business_license_url 必填")
		return
	}
	res, err := s.kyc.RecognizeBizLicense(c.Request.Context(), kyc.BizLicenseInput{ImageURL: req.BusinessLicenseURL})
	if err != nil {
		response.Fail(c, response.CodeThirdPartyError, "营业执照识别失败，请手动填写")
		return
	}
	response.OK(c, gin.H{
		"org_name":         res.Name,
		"org_credit_code":  res.CreditCode,
		"org_legal_person": res.LegalPerson,
		"address":          res.Address,
		"business":         res.Business,
	})
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
		// === 2026-07-02 加：拆开错误类型，返回更准的文案 ===
		case errors.Is(err, sms.ErrPhoneDailyLimit):
			response.Conflict(c, "该手机号今日短信已达上限，请明天再试或换其他手机号")
		case errors.Is(err, sms.ErrPhoneHourLimit):
			response.Conflict(c, "该手机号 1 小时内发送太频繁，请稍后再试")
		case errors.Is(err, sms.ErrAppDayLimit):
			response.Fail(c, response.CodeThirdPartyError, "平台今日短信配额已用完，请联系客服")
		case errors.Is(err, sms.ErrTemplateMissing):
			response.Fail(c, response.CodeThirdPartyError, "短信签名/模板未审核通过，请联系平台运营")
		case errors.Is(err, sms.ErrProviderFail):
			response.Fail(c, response.CodeThirdPartyError, "短信下发失败，请稍后重试或联系平台")
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

// validateBankCardFormat 仅校验银行卡号 / 对公账号格式（认证提交期专用，不走短信门）。
// ① 短信门只在「已认证后变更打款账户」(creatorChangeBankCard) 生效，避免驳回重提被误拦。
// enterprise=true 走对公账号规则（9~30 位数字，各行长度差异大），否则走个人银行卡规则。
func validateBankCardFormat(accountNo string, enterprise bool) error {
	if enterprise {
		if !enterpriseBankAccountRegex.MatchString(accountNo) {
			return errInvalidPublicAccount
		}
		return nil
	}
	if !bankCardRegex.MatchString(accountNo) {
		return errInvalidBankCard
	}
	return nil
}

// splitRejectFields 把逗号分隔的字段级驳回标记拆成切片；空值返回空切片（非 nil，前端拿到 []）。
func splitRejectFields(s string) []string {
	out := []string{}
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
