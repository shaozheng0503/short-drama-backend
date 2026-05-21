package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr               string
	DSN                string
	JWTSecret          string
	JWTExpires         time.Duration
	ShutdownTimeout    time.Duration
	CORSAllowedOrigins []string

	RedisAddr      string
	RedisPassword  string
	RedisDB        int
	IdempotencyTTL time.Duration

	RateLimitEnabled bool
	RateLimitRPS     float64
	RateLimitBurst   int

	AlertEnabled    bool
	AlertWebhookURL string
	AlertTimeout    time.Duration

	SMSDevMode           bool
	SMSCodeTTL           time.Duration
	SMSResendWindow      time.Duration
	SMSMaxVerifyAttempts int
	SMSVerifyLockWindow  time.Duration
	SMSSendIPRPS         float64
	SMSSendIPBurst       int

	AdminInitUsername string
	AdminInitPassword string

	BcryptCost int

	// 腾讯云通用密钥（命名对齐腾讯云官方 SDK：TENCENTCLOUD_SECRET_ID / _SECRET_KEY）
	TencentcloudSecretID  string
	TencentcloudSecretKey string

	// 腾讯云 SMS 专用配置（命名对齐控制台的 SDK_APP_ID / SIGN_NAME / TEMPLATE_ID）
	SMSSDKAppID            string
	SMSSignName            string
	SMSTemplateLogin       string
	SMSTemplateLoginParams string // 占位符顺序，逗号分隔；可选项：code / ttl_minutes。例："code" 单参数；"code,ttl_minutes" 双参数
	SMSRegion              string

	// 钱相关（执行文档第七节）
	CreatorShareRate   float64
	MinWithdrawalCents int64
	OrderPendingTTL    time.Duration

	// 敏感字段加密（AES-GCM）；32 字节 base64 编码（aes-256）
	DataEncryptionKeyB64 string

	// 支付 dev 模式：true 时所有 payment provider 返回 stub 参数，不真实联调
	PaymentDevMode bool

	// 微信 / 支付宝商户号（生产时填齐才会真实下单）
	WechatAppID    string
	WechatMchID    string
	WechatAPIKeyV3 string

	AlipayAppID      string
	AlipayPrivateKey string
	AlipayPublicKey  string
}

func Load() Config {
	return Config{
		Addr:            getEnv("APP_ADDR", ":8080"),
		DSN:             getEnv("DATABASE_DSN", "host=localhost user=postgres password=postgres dbname=ai_drama port=5432 sslmode=disable TimeZone=Asia/Shanghai"),
		JWTSecret:       getEnv("JWT_SECRET", "dev-secret-change-me"),
		JWTExpires:      time.Duration(getEnvInt("JWT_EXPIRES_HOURS", 168)) * time.Hour,
		ShutdownTimeout: time.Duration(getEnvInt("APP_SHUTDOWN_TIMEOUT_SECONDS", 10)) * time.Second,
		CORSAllowedOrigins: getEnvList("CORS_ALLOWED_ORIGINS", []string{
			"http://localhost:3000",
			"http://localhost:5173",
			"http://127.0.0.1:3000",
			"http://127.0.0.1:5173",
		}),
		RedisAddr:        getEnv("REDIS_ADDR", ""),
		RedisPassword:    getEnv("REDIS_PASSWORD", ""),
		RedisDB:          getEnvInt("REDIS_DB", 0),
		IdempotencyTTL:   time.Duration(getEnvInt("IDEMPOTENCY_TTL_SECONDS", 1800)) * time.Second,
		RateLimitEnabled: getEnvBool("RATE_LIMIT_ENABLED", false),
		RateLimitRPS:     getEnvFloat("RATE_LIMIT_RPS", 20),
		RateLimitBurst:   getEnvInt("RATE_LIMIT_BURST", 40),
		AlertEnabled:     getEnvBool("ALERT_ENABLED", false),
		AlertWebhookURL:  getEnv("ALERT_WEBHOOK_URL", ""),
		AlertTimeout:     time.Duration(getEnvInt("ALERT_TIMEOUT_SECONDS", 3)) * time.Second,

		SMSDevMode:           getEnvBool("SMS_DEV_MODE", true),
		SMSCodeTTL:           time.Duration(getEnvInt("SMS_CODE_TTL_SECONDS", 300)) * time.Second,
		SMSResendWindow:      time.Duration(getEnvInt("SMS_RESEND_WINDOW_SECONDS", 60)) * time.Second,
		SMSMaxVerifyAttempts: getEnvInt("SMS_MAX_VERIFY_ATTEMPTS", 5),
		SMSVerifyLockWindow:  time.Duration(getEnvInt("SMS_VERIFY_LOCK_SECONDS", 900)) * time.Second,
		SMSSendIPRPS:         getEnvFloat("SMS_SEND_IP_RPS", 0.2),
		SMSSendIPBurst:       getEnvInt("SMS_SEND_IP_BURST", 3),

		AdminInitUsername: getEnv("ADMIN_INIT_USERNAME", "admin"),
		AdminInitPassword: getEnv("ADMIN_INIT_PASSWORD", "admin123"),

		BcryptCost: getEnvInt("BCRYPT_COST", 10),

		TencentcloudSecretID:  getEnv("TENCENTCLOUD_SECRET_ID", ""),
		TencentcloudSecretKey: getEnv("TENCENTCLOUD_SECRET_KEY", ""),

		SMSSDKAppID:            getEnv("SMS_SDK_APP_ID", ""),
		SMSSignName:            getEnv("SMS_SIGN_NAME", ""),
		SMSTemplateLogin:       getEnv("SMS_TEMPLATE_LOGIN", ""),
		SMSTemplateLoginParams: getEnv("SMS_TEMPLATE_LOGIN_PARAMS", "code"),
		SMSRegion:              getEnv("SMS_REGION", "ap-guangzhou"),

		CreatorShareRate:   getEnvFloat("CREATOR_SHARE_RATE", 0.5),
		MinWithdrawalCents: int64(getEnvInt("MIN_WITHDRAWAL_CENTS", 10000)),
		OrderPendingTTL:    time.Duration(getEnvInt("ORDER_PENDING_TTL_SECONDS", 1800)) * time.Second,

		DataEncryptionKeyB64: getEnv("DATA_ENCRYPTION_KEY", ""),

		PaymentDevMode: getEnvBool("PAYMENT_DEV_MODE", true),
		WechatAppID:    getEnv("WECHAT_APP_ID", ""),
		WechatMchID:    getEnv("WECHAT_MCH_ID", ""),
		WechatAPIKeyV3: getEnv("WECHAT_API_KEY_V3", ""),

		AlipayAppID:      getEnv("ALIPAY_APP_ID", ""),
		AlipayPrivateKey: getEnv("ALIPAY_PRIVATE_KEY", ""),
		AlipayPublicKey:  getEnv("ALIPAY_PUBLIC_KEY", ""),
	}
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}

func getEnvBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	b, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return b
}

func getEnvFloat(key string, fallback float64) float64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}
	return f
}

func getEnvList(key string, fallback []string) []string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parts := strings.Split(value, ",")
	list := make([]string, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item != "" {
			list = append(list, item)
		}
	}
	return list
}
