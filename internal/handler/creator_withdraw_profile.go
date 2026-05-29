package handler

import (
	"fmt"
	"strings"

	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

type withdrawProfileCheck struct {
	OK            bool
	Hint          string
	MissingFields []string
}

func checkCreatorWithdrawProfile(cr model.Creator) withdrawProfileCheck {
	if cr.VerifyStatus != model.CreatorVerifyVerified {
		return withdrawProfileCheck{Hint: verifyStatusWithdrawHint(cr)}
	}
	if cr.Status != model.StatusActive {
		return withdrawProfileCheck{Hint: "账号已被封禁，无法提现"}
	}
	fields, labels := creatorPayoutProfileMissing(cr)
	if len(fields) > 0 {
		return withdrawProfileCheck{
			Hint:          fmt.Sprintf("认证已通过但资料不完整，请前往「实名认证」补充：%s", strings.Join(labels, "、")),
			MissingFields: fields,
		}
	}
	return withdrawProfileCheck{OK: true}
}

func checkCreatorVerificationApprovable(cr model.Creator) withdrawProfileCheck {
	fields, labels := creatorPayoutProfileMissing(cr)
	if len(fields) > 0 {
		return withdrawProfileCheck{
			Hint:          fmt.Sprintf("资料不完整，无法通过审核，缺少：%s", strings.Join(labels, "、")),
			MissingFields: fields,
		}
	}
	return withdrawProfileCheck{OK: true}
}

func verifyStatusWithdrawHint(cr model.Creator) string {
	switch cr.VerifyStatus {
	case model.CreatorVerifyPending:
		return "实名认证审核中，请等待通过后再提现"
	case model.CreatorVerifyRejected:
		if cr.VerifyRejectReason != "" {
			return fmt.Sprintf("实名认证被驳回：%s，请修改后重新提交", cr.VerifyRejectReason)
		}
		return "实名认证被驳回，请修改后重新提交"
	case model.CreatorVerifyUnverified:
		return "请先完成实名认证（填写姓名、身份证、开户银行、银行卡号）"
	default:
		return "请先完成实名认证并通过审核"
	}
}

func creatorPayoutProfileMissing(cr model.Creator) (fields []string, labels []string) {
	if cr.CreatorType == model.CreatorTypeOrganization {
		if cr.OrgName == "" {
			fields = append(fields, "org_name")
			labels = append(labels, "企业名称")
		}
		if cr.OrgCreditCode == "" {
			fields = append(fields, "org_credit_code")
			labels = append(labels, "统一社会信用代码")
		}
		if cr.BusinessLicenseURL == "" {
			fields = append(fields, "business_license_url")
			labels = append(labels, "营业执照")
		}
	} else {
		if cr.Name == "" {
			fields = append(fields, "real_name")
			labels = append(labels, "真实姓名")
		}
		if cr.IDCardNoEnc == "" {
			fields = append(fields, "id_card_no")
			labels = append(labels, "身份证号")
		}
	}
	if cr.BankName == "" {
		fields = append(fields, "bank_name")
		labels = append(labels, "开户银行")
	}
	if cr.BankCardLast4 == "" {
		fields = append(fields, "bank_card_no")
		labels = append(labels, "银行卡号")
	}
	return fields, labels
}

func respondWithdrawProfileBlock(c *gin.Context, check withdrawProfileCheck, useForbidden bool) {
	data := gin.H{}
	if len(check.MissingFields) > 0 {
		data["missing_fields"] = check.MissingFields
	}
	code := response.CodeInvalidParam
	if useForbidden {
		code = response.CodeForbidden
	}
	if len(data) > 0 {
		response.FailWithData(c, code, check.Hint, data)
		return
	}
	if useForbidden {
		response.Forbidden(c, check.Hint)
		return
	}
	response.InvalidParam(c, check.Hint)
}
