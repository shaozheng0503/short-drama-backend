package sms

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"ai-drama-platform/internal/config"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	tcerrors "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	smssdk "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/sms/v20210111"
)

// TencentProvider 腾讯云短信真实接入（v20210111）。
//
// 触发条件（见 SelectProvider）：
//   - SMS_DEV_MODE=false
//   - TENCENTCLOUD_SECRET_ID / _SECRET_KEY / SMS_SDK_APP_ID / SMS_SIGN_NAME / SMS_TEMPLATE_LOGIN 全部非空
//
// 任何一项缺失都会退回 DevProvider，不会走到这里。
//
// 上线前控制台前置条件：
//   - 签名审核通过（SMS_SIGN_NAME 文案与营业执照主体一致）
//   - 模板审核通过（类目=验证码，参数为 {1}=code、{2}=分钟）
//   - SMS_SDK_APP_ID 与签名 / 模板归属同一应用 + 同一地域
type TencentProvider struct {
	cfg    config.Config
	client *smssdk.Client
}

func newTencentClient(cfg config.Config) (*smssdk.Client, error) {
	credential := common.NewCredential(cfg.TencentcloudSecretID, cfg.TencentcloudSecretKey)
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = "sms.tencentcloudapi.com"
	cpf.HttpProfile.ReqTimeout = 10 // 短信发在登录请求路径上，收紧到 10s（SDK 默认 60s 太长会拖住登录）
	return smssdk.NewClient(credential, cfg.SMSRegion, cpf)
}

func (*TencentProvider) Name() string { return "tencent" }

// templateParams 按 SMS_TEMPLATE_LOGIN_PARAMS 配置渲染模板参数。
//   - "code"            → [验证码]
//   - "code,ttl_minutes" → [验证码, 分钟数]
//   - "ttl_minutes,code" → [分钟数, 验证码]
//
// 占位符顺序必须与腾讯云控制台审核通过的模板内容里 {1}/{2} 一致。
func (p *TencentProvider) templateParams(code string) []string {
	format := strings.TrimSpace(p.cfg.SMSTemplateLoginParams)
	if format == "" {
		format = "code"
	}
	ttlMinutes := int(p.cfg.SMSCodeTTL.Minutes())
	if ttlMinutes < 1 {
		ttlMinutes = 1
	}
	parts := strings.Split(format, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		switch strings.TrimSpace(part) {
		case "code":
			out = append(out, code)
		case "ttl_minutes":
			out = append(out, strconv.Itoa(ttlMinutes))
		default:
			// 未知标识当作字面值发，便于将来扩展静态参数
			out = append(out, strings.TrimSpace(part))
		}
	}
	return out
}

func (p *TencentProvider) Send(_ context.Context, phone, code, scene string) error {
	// 懒加载 client，避免启动阶段配置不全直接 panic
	if p.client == nil {
		c, err := newTencentClient(p.cfg)
		if err != nil {
			log.Printf("[sms-tencent] create client err=%v", err)
			return fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
		}
		p.client = c
	}

	req := smssdk.NewSendSmsRequest()
	req.SmsSdkAppId = common.StringPtr(p.cfg.SMSSDKAppID)
	req.SignName = common.StringPtr(p.cfg.SMSSignName)
	req.TemplateId = common.StringPtr(p.cfg.SMSTemplateLogin)
	req.PhoneNumberSet = common.StringPtrs([]string{"+86" + phone})
	req.TemplateParamSet = common.StringPtrs(p.templateParams(code))

	resp, err := p.client.SendSms(req)
	if err != nil {
		// 腾讯云 SDK 错误（鉴权 / 模板未审核 / 余额不足等）
		var tcErr *tcerrors.TencentCloudSDKError
		if errors.As(err, &tcErr) {
			log.Printf("[sms-tencent] phone=%s scene=%s SDK code=%s msg=%s requestId=%s",
				phone, scene, tcErr.Code, tcErr.Message, tcErr.RequestId)
			// === 2026-07-02 加：按 SDK errCode 分类，让上层返回明确文案 ===
			wrapped := classifyTencentSDKError(tcErr.Code, tcErr.Message)
			return fmt.Errorf("%w: %s %s", wrapped, tcErr.Code, tcErr.Message)
		}
		log.Printf("[sms-tencent] phone=%s scene=%s err=%v", phone, scene, err)
		return fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	if resp == nil || resp.Response == nil || len(resp.Response.SendStatusSet) == 0 {
		log.Printf("[sms-tencent] phone=%s scene=%s empty response", phone, scene)
		return ErrProviderUnavailable
	}
	status := resp.Response.SendStatusSet[0]
	if status.Code == nil || *status.Code != "Ok" {
		errCode := ""
		errMsg := ""
		if status.Code != nil {
			errCode = *status.Code
		}
		if status.Message != nil {
			errMsg = *status.Message
		}
		log.Printf("[sms-tencent] phone=%s scene=%s status code=%s msg=%s",
			phone, scene, errCode, errMsg)
		// === 2026-07-02 加：按 status errCode 分类 ===
		wrapped := classifyTencentStatusError(errCode, errMsg)
		return fmt.Errorf("%w: %s %s", wrapped, errCode, errMsg)
	}

	log.Printf("[sms-tencent] phone=%s scene=%s sent ok", phone, scene)
	return nil
}

// classifyTencentSDKError 把腾讯云 SDK 错误（鉴权/模板未审核/余额不足等）按 Code 分类。
// SDK 错误多发生在发短信之前（请求都没进队列），常见：
//   - LimitExceeded.SmsDayLimit → 整个 app id 配额用满
//   - FailedOperation.SignatureIncorrectOrUnapproved → 签名未通过
//   - FailedOperation.TemplateIncorrect / TemplateNotApproved → 模板未通过
//   - AuthFailure.* → 鉴权失败（SecretId/Key 错）
func classifyTencentSDKError(code, msg string) error {
	switch {
	case strings.HasPrefix(code, "LimitExceeded"):
		// SDK 阶段的 LimitExceeded 一般是 app id 级别的（PhoneNumber* 类的只在 status 里出现）
		return ErrAppDayLimit
	case strings.HasPrefix(code, "FailedOperation.Signature"),
		strings.HasPrefix(code, "FailedOperation.Template"):
		return ErrTemplateMissing
	default:
		return ErrProviderUnavailable
	}
}

// classifyTencentStatusError 把腾讯云 status 错误（实际发送后的回执）按 Code 分类。
// status 错误是真正发短信之后腾讯云返回的，常见：
//   - LimitExceeded.PhoneNumberDailyLimit → 单手机号当日额度用满
//   - LimitExceeded.PhoneNumberOneHourLimit → 单手机号 1 小时上限
//   - FailedOperation.* → 业务失败（频率/黑名单等）
func classifyTencentStatusError(code, msg string) error {
	switch code {
	case "LimitExceeded.PhoneNumberDailyLimit":
		return ErrPhoneDailyLimit
	case "LimitExceeded.PhoneNumberOneHourLimit":
		return ErrPhoneHourLimit
	case "LimitExceeded.SmsDayLimit":
		return ErrAppDayLimit
	}
	if strings.HasPrefix(code, "FailedOperation") {
		return ErrProviderUnavailable
	}
	return ErrProviderUnavailable
}
