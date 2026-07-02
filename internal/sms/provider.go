package sms

import (
	"context"
	"errors"
	"log"

	"ai-drama-platform/internal/config"
)

// Provider 抽象短信网关：dev 模式打印日志，生产模式走腾讯云。
// scene 用于将来按场景分发不同模板，当前 MVP 全部使用登录模板。
type Provider interface {
	Send(ctx context.Context, phone, code, scene string) error
	Name() string
}

// ErrProviderUnavailable 由腾讯云 provider 在配置不全或 SDK 未接入时抛出。
var ErrProviderUnavailable = errors.New("短信 provider 不可用")

// === 2026-07-02 加：把腾讯云常见错误拆开，方便上层返回明确文案 ===
// 之前所有错误都包成 ErrProviderUnavailable，调用方只能统一回
// 「短信网关下发失败，请稍后重试」——歧义太大，让用户以为是网关挂了。
// 现在按腾讯云 errCode 分类，调用方针对不同场景返回不同文案。

// ErrPhoneDailyLimit 单手机号当日发送上限（LimitExceeded.PhoneNumberDailyLimit）
// 业务含义：该手机号今天已经收满 5 条短信了，明天 0 点自动重置
var ErrPhoneDailyLimit = errors.New("短信 provider 不可用: phone daily limit")

// ErrPhoneHourLimit 单手机号 1 小时发送上限（LimitExceeded.PhoneNumberOneHourLimit）
// 业务含义：1 小时内该手机号收短信太频繁
var ErrPhoneHourLimit = errors.New("短信 provider 不可用: phone hour limit")

// ErrAppDayLimit 整个 app id 当日发送上限（LimitExceeded.SmsDayLimit）
// 业务含义：平台的短信配额用完了，需要找腾讯云买量或升级套餐
var ErrAppDayLimit = errors.New("短信 provider 不可用: app day limit")

// ErrTemplateMissing 模板/签名未通过（FailedOperation.TemplateIncorrect / SignatureIncorrectOrUnapproved）
// 业务含义：腾讯云侧签名/模板还没审批通过
var ErrTemplateMissing = errors.New("短信 provider 不可用: template not approved")

// SelectProvider 根据配置选择具体实现。
// 关键点：默认 dev；只有 SMS_DEV_MODE=false 且腾讯云字段齐全才会用 TencentProvider。
func SelectProvider(cfg config.Config) Provider {
	if cfg.SMSDevMode {
		return &DevProvider{}
	}
	if !tencentReady(cfg) {
		log.Printf("[sms] SMS_DEV_MODE=false 但腾讯云配置不全，退回 DevProvider。" +
			"需要 TENCENTCLOUD_SECRET_ID/KEY + SMS_SDK_APP_ID/SIGN_NAME/TEMPLATE_LOGIN 全部填齐。")
		return &DevProvider{}
	}
	return &TencentProvider{cfg: cfg}
}

func tencentReady(cfg config.Config) bool {
	return cfg.TencentcloudSecretID != "" &&
		cfg.TencentcloudSecretKey != "" &&
		cfg.SMSSDKAppID != "" &&
		cfg.SMSSignName != "" &&
		cfg.SMSTemplateLogin != ""
}

// DevProvider 仅写日志，不调用任何外部网关。
type DevProvider struct{}

func (*DevProvider) Name() string { return "dev" }

func (*DevProvider) Send(_ context.Context, phone, code, scene string) error {
	log.Printf("[sms-dev] phone=%s scene=%s code=%s", phone, scene, code)
	return nil
}
