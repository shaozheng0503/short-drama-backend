package configcheck

import (
	"fmt"
	"strings"

	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/secure"
)

type Severity string

const (
	SeverityError Severity = "error"
	SeverityWarn  Severity = "warn"
)

type Issue struct {
	Severity Severity
	Code     string
	Message  string
}

type Options struct {
	Prod bool
}

type Report struct {
	Prod   bool
	Issues []Issue
}

func (r Report) HasErrors() bool {
	for _, issue := range r.Issues {
		if issue.Severity == SeverityError {
			return true
		}
	}
	return false
}

func Run(cfg config.Config, opts Options) Report {
	report := Report{Prod: opts.Prod}

	if strings.TrimSpace(cfg.DSN) == "" {
		report.add(SeverityError, "database_dsn_missing", "DATABASE_DSN 不能为空")
	}
	if cfg.JWTSecret == "" || cfg.JWTSecret == "dev-secret-change-me" || cfg.JWTSecret == "dev-secret" {
		severity := SeverityWarn
		if opts.Prod {
			severity = SeverityError
		}
		report.add(severity, "jwt_secret_default", "JWT_SECRET 仍是默认/开发值")
	}
	if cfg.JWTExpires <= 0 {
		report.add(SeverityError, "jwt_expires_invalid", "JWT_EXPIRES_HOURS 必须大于 0")
	}
	if cfg.ShutdownTimeout <= 0 {
		report.add(SeverityError, "shutdown_timeout_invalid", "APP_SHUTDOWN_TIMEOUT_SECONDS 必须大于 0")
	}
	if len(cfg.CORSAllowedOrigins) == 0 {
		report.add(SeverityWarn, "cors_origins_empty", "CORS_ALLOWED_ORIGINS 为空，浏览器跨域联调会失败")
	}
	for _, origin := range cfg.CORSAllowedOrigins {
		if strings.TrimSpace(origin) == "*" && opts.Prod {
			report.add(SeverityError, "cors_origin_wildcard", "生产环境 CORS_ALLOWED_ORIGINS 不允许使用 *")
		}
	}
	if cfg.BcryptCost < 10 {
		report.add(SeverityWarn, "bcrypt_cost_low", fmt.Sprintf("BCRYPT_COST=%d 偏低，建议生产不低于 10", cfg.BcryptCost))
	}
	if cfg.CreatorShareRate < 0 || cfg.CreatorShareRate > 1 {
		report.add(SeverityError, "creator_share_rate_invalid", fmt.Sprintf("CREATOR_SHARE_RATE=%.4f 非法，必须在 0~1 之间", cfg.CreatorShareRate))
	}
	if cfg.MinWithdrawalCents <= 0 {
		report.add(SeverityError, "min_withdrawal_invalid", "MIN_WITHDRAWAL_CENTS 必须大于 0")
	}
	if cfg.OrderPendingTTL <= 0 {
		report.add(SeverityError, "order_pending_ttl_invalid", "ORDER_PENDING_TTL_SECONDS 必须大于 0")
	}
	if cfg.IdempotencyTTL <= 0 {
		report.add(SeverityError, "idempotency_ttl_invalid", "IDEMPOTENCY_TTL_SECONDS 必须大于 0")
	}
	if strings.TrimSpace(cfg.RedisAddr) == "" {
		severity := SeverityWarn
		if opts.Prod {
			severity = SeverityError
		}
		report.add(severity, "redis_addr_missing", "REDIS_ADDR 未配置，钱相关接口无法启用 Redis 强幂等")
	}
	if !cfg.RateLimitEnabled {
		severity := SeverityWarn
		if opts.Prod {
			severity = SeverityError
		}
		report.add(severity, "rate_limit_disabled", "RATE_LIMIT_ENABLED=false，全局 API 限流未启用")
	}
	if cfg.RateLimitEnabled && cfg.RateLimitRPS <= 0 {
		report.add(SeverityError, "rate_limit_rps_invalid", "RATE_LIMIT_RPS 必须大于 0")
	}
	if cfg.RateLimitEnabled && cfg.RateLimitBurst <= 0 {
		report.add(SeverityError, "rate_limit_burst_invalid", "RATE_LIMIT_BURST 必须大于 0")
	}
	if cfg.AlertEnabled && strings.TrimSpace(cfg.AlertWebhookURL) == "" {
		report.add(SeverityError, "alert_webhook_missing", "ALERT_ENABLED=true 时 ALERT_WEBHOOK_URL 必填")
	}
	if cfg.AlertEnabled && cfg.AlertTimeout <= 0 {
		report.add(SeverityError, "alert_timeout_invalid", "ALERT_TIMEOUT_SECONDS 必须大于 0")
	}

	if _, err := secure.New(cfg.DataEncryptionKeyB64); err != nil {
		severity := SeverityWarn
		if opts.Prod {
			severity = SeverityError
		}
		report.add(severity, "data_encryption_key_invalid", fmt.Sprintf("DATA_ENCRYPTION_KEY 不可用：%v", err))
	}

	if cfg.AdminInitUsername == "admin" && cfg.AdminInitPassword == "admin123" {
		severity := SeverityWarn
		if opts.Prod {
			severity = SeverityError
		}
		report.add(severity, "admin_default_credentials", "ADMIN_INIT_USERNAME/ADMIN_INIT_PASSWORD 仍是默认账号密码")
	}

	checkSMS(cfg, opts, &report)
	checkPayment(cfg, opts, &report)

	return report
}

func checkSMS(cfg config.Config, opts Options, report *Report) {
	if cfg.SMSDevMode {
		severity := SeverityWarn
		if opts.Prod {
			severity = SeverityError
		}
		report.add(severity, "sms_dev_mode_enabled", "SMS_DEV_MODE=true，短信会走 dev provider")
		return
	}

	required := map[string]string{
		"TENCENTCLOUD_SECRET_ID":  cfg.TencentcloudSecretID,
		"TENCENTCLOUD_SECRET_KEY": cfg.TencentcloudSecretKey,
		"SMS_SDK_APP_ID":          cfg.SMSSDKAppID,
		"SMS_SIGN_NAME":           cfg.SMSSignName,
		"SMS_TEMPLATE_LOGIN":      cfg.SMSTemplateLogin,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			report.add(SeverityError, "sms_config_missing", fmt.Sprintf("SMS_DEV_MODE=false 时 %s 必填", name))
		}
	}
}

func checkPayment(cfg config.Config, opts Options, report *Report) {
	// 支付宝只要配齐密钥就会启用真实 provider（不论 PAYMENT_DEV_MODE），此时缺 notify_url 收不到回调，
	// 故这条检查放在 dev 模式早退之前。
	alipayConfigured := strings.TrimSpace(cfg.AlipayAppID) != "" &&
		strings.TrimSpace(cfg.AlipayPrivateKey) != "" &&
		strings.TrimSpace(cfg.AlipayPublicKey) != ""
	if alipayConfigured && strings.TrimSpace(cfg.AlipayNotifyURL) == "" {
		report.add(SeverityWarn, "alipay_notify_url_missing", "支付宝已配密钥但 ALIPAY_NOTIFY_URL 为空，收不到异步支付结果通知")
	}

	if cfg.PaymentDevMode {
		severity := SeverityWarn
		if opts.Prod {
			severity = SeverityError
		}
		report.add(severity, "payment_dev_mode_enabled", "PAYMENT_DEV_MODE=true，支付会走 dev provider")
		return
	}

	wechatRequired := map[string]string{
		"WECHAT_APP_ID":     cfg.WechatAppID,
		"WECHAT_MCH_ID":     cfg.WechatMchID,
		"WECHAT_API_KEY_V3": cfg.WechatAPIKeyV3,
	}
	for name, value := range wechatRequired {
		if strings.TrimSpace(value) == "" {
			report.add(SeverityError, "wechat_config_missing", fmt.Sprintf("PAYMENT_DEV_MODE=false 时 %s 必填", name))
		}
	}

	alipayRequired := map[string]string{
		"ALIPAY_APP_ID":      cfg.AlipayAppID,
		"ALIPAY_PRIVATE_KEY": cfg.AlipayPrivateKey,
		"ALIPAY_PUBLIC_KEY":  cfg.AlipayPublicKey,
	}
	for name, value := range alipayRequired {
		if strings.TrimSpace(value) == "" {
			report.add(SeverityError, "alipay_config_missing", fmt.Sprintf("PAYMENT_DEV_MODE=false 时 %s 必填", name))
		}
	}
}

func (r *Report) add(severity Severity, code, message string) {
	r.Issues = append(r.Issues, Issue{Severity: severity, Code: code, Message: message})
}
