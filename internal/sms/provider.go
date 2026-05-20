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
