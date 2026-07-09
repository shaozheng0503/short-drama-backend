package handler

import (
	"log"
	"strings"
	"time"

	"ai-drama-platform/internal/kyc"
	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 发行商企业认证（复用创作者企业认证流程 + KYC provider）
// ============================================================

type distributorEnterpriseVerificationRequest struct {
	OrgName            string `json:"org_name" binding:"required"`
	OrgCreditCode      string `json:"org_credit_code" binding:"required"`
	OrgLegalPerson     string `json:"org_legal_person" binding:"required"`
	OrgLegalIDCard     string `json:"org_legal_id_card"` // 首次必填，重交可空（沿用库值）
	BusinessLicenseURL string `json:"business_license_url" binding:"required"`
	BankLicenseURL     string `json:"bank_license_url" binding:"required"`
	BankName           string `json:"bank_name" binding:"required"`
	BankCardNo         string `json:"bank_card_no"` // 对公账号，首次必填
}

// PUT /v1/distributor/verification/enterprise
func (s *Server) distributorUpdateEnterpriseVerification(c *gin.Context) {
	id := middleware.CurrentID(c)
	var d model.Distributor
	if err := s.db.First(&d, id).Error; err != nil {
		response.NotFound(c, "发行商不存在")
		return
	}

	// 已 verified 拒绝重交
	if d.VerifyStatus == model.DistributorVerifyVerified {
		response.Conflict(c, "已通过企业认证，如需修改信息请联系客服")
		return
	}

	var req distributorEnterpriseVerificationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "参数不完整："+err.Error())
		return
	}

	// 法人身份证号：不传则沿用库值（重新提交场景）
	orgLegalIDCard := strings.TrimSpace(req.OrgLegalIDCard)
	if orgLegalIDCard == "" && d.OrgLegalIDCardEnc != "" {
		if decrypted, err := s.cryptor.Decrypt(d.OrgLegalIDCardEnc); err == nil {
			orgLegalIDCard = decrypted
		}
	}
	if orgLegalIDCard == "" {
		response.InvalidParam(c, "法人身份证号必填")
		return
	}

	// 对公账号：不传则沿用库值
	bankCardNo := strings.TrimSpace(req.BankCardNo)
	if bankCardNo == "" && d.BankCardNoEnc != "" {
		if decrypted, err := s.cryptor.Decrypt(d.BankCardNoEnc); err == nil {
			bankCardNo = decrypted
		}
	}
	if bankCardNo == "" {
		response.InvalidParam(c, "对公银行账号必填")
		return
	}

	// 企业四要素核验（复用 KYC provider）
	verifyMethod := "manual"
	providerResult := ""
	if s.kyc != nil {
		res, err := s.kyc.VerifyBizLicense4(c.Request.Context(), kyc.BizLicense4Input{
			EntName:    req.OrgName,
			CreditCode: req.OrgCreditCode,
			LegalName:  req.OrgLegalPerson,
			LegalIDNum: orgLegalIDCard,
		})
		if err != nil || !res.Available {
			log.Printf("[verify-dist] 企业四要素核验异常/不可用，降级人工审核 err=%v", err)
		} else if !res.Passed {
			// 四要素不匹配 → 拒绝提交
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
			response.Conflict(c, "企业四要素核验不通过："+strings.Join(bad, "、")+"不匹配")
			return
		} else {
			verifyMethod = "biz_4e"
			providerResult = res.Raw
		}
	}

	// 加密敏感字段
	orgLegalIDCardEnc, _ := s.cryptor.Encrypt(orgLegalIDCard)
	bankCardNoEnc, _ := s.cryptor.Encrypt(bankCardNo)

	now := time.Now()
	updates := map[string]interface{}{
		"org_name":                 req.OrgName,
		"org_credit_code":          req.OrgCreditCode,
		"org_legal_person":         req.OrgLegalPerson,
		"org_legal_id_card_enc":    orgLegalIDCardEnc,
		"org_legal_id_card_masked": maskIDCard(orgLegalIDCard),
		"business_license_url":     req.BusinessLicenseURL,
		"bank_license_url":         req.BankLicenseURL,
		"bank_name":                req.BankName,
		"bank_card_no_enc":         bankCardNoEnc,
		"bank_card_no_masked":      maskBankCard(bankCardNo),
		"bank_card_last4":          lastN(bankCardNo, 4),
		"verify_status":            model.DistributorVerifyPending,
		"verify_submitted_at":      now,
		"verify_method":            verifyMethod,
		"verify_provider_result":   providerResult,
		"verify_reject_reason":     "",
		"verify_reject_fields":     "",
	}
	if err := s.db.Model(&d).Updates(updates).Error; err != nil {
		response.ServerError(c, "提交认证失败")
		return
	}

	s.db.First(&d, id)
	response.OK(c, distributorDetailView(&d))
}

// POST /v1/distributor/verification/biz-license/ocr
func (s *Server) distributorBizLicenseOCR(c *gin.Context) {
	var req struct {
		ImageURL string `json:"image_url" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "image_url 必填")
		return
	}

	if s.kyc == nil {
		response.Fail(c, response.CodeServerError, "OCR 服务未配置")
		return
	}

	res, err := s.kyc.RecognizeBizLicense(c.Request.Context(), kyc.BizLicenseInput{ImageURL: req.ImageURL})
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

// GET /v1/distributor/verification/status
func (s *Server) distributorVerificationStatus(c *gin.Context) {
	id := middleware.CurrentID(c)
	var d model.Distributor
	if err := s.db.Select("verify_status, verify_reject_reason, verify_reject_fields, verify_submitted_at, verify_checked_at").First(&d, id).Error; err != nil {
		response.NotFound(c, "发行商不存在")
		return
	}
	response.OK(c, gin.H{
		"verify_status":        d.VerifyStatus,
		"verify_reject_reason": d.VerifyRejectReason,
		"verify_reject_fields": d.VerifyRejectFields,
		"verify_submitted_at":  d.VerifySubmittedAt,
		"verify_checked_at":    d.VerifyCheckedAt,
	})
}

// ============================================================
// 辅助函数 maskIDCard / maskBankCard 复用 creator_data.go 中已有定义
// ============================================================

// lastN 取末尾 n 位
func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
