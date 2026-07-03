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

	// DB 连接池：默认 unlimited open + idle=2（database/sql 默认）在高并发下会撑爆
	// Postgres max_connections(100) 导致 "too many clients"。显式封顶并回收空闲连接。
	DBMaxOpenConns    int
	DBMaxIdleConns    int
	DBConnMaxLifetime time.Duration
	DBConnMaxIdleTime time.Duration

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

	// 实名/企业认证核验（个人=银行卡三要素 faceid，企业=营业执照 OCR）
	// KYCDevMode=true 或腾讯云密钥不全 → 走 stub 直通（不做真实核验），默认 true 保持联调期行为。
	KYCDevMode      bool
	KYCFaceIDRegion string // 银行卡核验地域，默认 ap-guangzhou
	KYCOCRRegion    string // 营业执照 OCR 地域，默认 ap-guangzhou

	// 腾讯云 SMS 专用配置（命名对齐控制台的 SDK_APP_ID / SIGN_NAME / TEMPLATE_ID）
	SMSSDKAppID            string
	SMSSignName            string
	SMSTemplateLogin       string
	SMSTemplateLoginParams string // 占位符顺序，逗号分隔；可选项：code / ttl_minutes。例："code" 单参数；"code,ttl_minutes" 双参数
	SMSRegion              string

	// 钱相关（执行文档第七节）
	CreatorShareRate   float64
	MinWithdrawalCents int64
	OrderPendingTTL    time.Duration // 本地关单（销毁待支付）时长，必须 > PaymentExpire，防"已关单仍可支付"资损
	PaymentExpire      time.Duration // 传给微信/支付宝的第三方支付有效期，必须 < OrderPendingTTL

	// 敏感字段加密（AES-GCM）；32 字节 base64 编码（aes-256）
	DataEncryptionKeyB64 string

	// 支付 dev 模式：true 时所有 payment provider 返回 stub 参数，不真实联调
	PaymentDevMode bool

	// SeedMockData=true 时，启动期自动灌 mock 短剧 / 用户 / 订单等数据，便于前端联调。
	// 幂等：已存在的不会重复写。生产严禁开启。
	SeedMockData bool

	// 腾讯云 COS（图片上传：封面 / 头像）
	// CDNDomain 留空时，URL 拼为 https://{bucket}.cos.{region}.myqcloud.com
	COSBucket    string
	COSRegion    string
	COSAppID     string
	COSSecretID  string
	COSSecretKey string
	COSCDNDomain string

	// COS Referer 白名单：cmd/setup-cos-referer 调用 PutBucketReferer 时用。
	// 默认空 → cmd/setup-cos-referer 拒绝运行（防止误清空规则）。逗号分隔，支持通配符。
	COSRefererWhitelist string

	// 腾讯云 VOD（视频上传：剧集）
	// SubAppID 若填 0，则走主应用（生产建议建子应用做数据隔离）。
	// CallbackKey 为节点回调签名密钥；空 → 不验签（仅日志告警，生产必填）。
	VODSubAppID     uint64
	VODRegion       string
	VODSecretID     string
	VODSecretKey    string
	VODProcedure    string
	VODCallbackKey  string
	VODCDNDomain    string
	VODSignExpire   time.Duration // 客户端上传签名有效期，默认 1h
	COSSignExpire   time.Duration // COS PUT 预签名有效期，默认 15min

	// VOD Key 防盗链：appPlayEpisode 拼临时 token URL，挡 URL 泄露被白嫖。
	// 开通条件：腾讯 VOD 控制台 → 分发播放 → Key 防盗链「启用」+ 拿到 KEY。
	// 默认 false：不签 → 直返 video_url（联调期保持这个，避免 break Apifox）。
	VODPlaySignEnabled bool
	VODPlaySignKey     string        // 控制台里那个「Key 防盗链」字段的 32 位 KEY
	VODPlaySignExpire  time.Duration // URL 有效期，默认 1h
	VODPlaySignExper   int           // 试看时长（秒），0=不试看
	VODPlaySignRlimit  int           // 限制 IP 数（实测「试看」类业务不要乱填，默认 0）

	// 微信 / 支付宝商户号（生产时填齐才会真实下单）
	WechatAppID            string
	WechatMchID            string
	WechatAPIKeyV3         string
	WechatMchCertSerialNo  string // 商户证书序列号
	WechatMchPrivateKeyPath string // 商户 API 私钥文件路径 apiclient_key.pem
	WechatNotifyURL        string // 异步通知地址 https://<域名>/v1/webhooks/wechat/pay

	AlipayAppID            string
	AlipayPrivateKey       string // 兼容老写法：私钥 PEM 文本直接写在 .env
	AlipayPrivateKeyPath   string // 推荐：私钥 PEM 文件路径（避免密钥进 .env/commit/聊天，泄漏面更小）
	AlipayPublicKey        string
	AlipayPublicKeyPath    string
	AlipaySandbox          bool   // true=沙箱网关，false=生产网关（默认沙箱，更安全）
	AlipayNotifyURL        string // 异步通知地址，公网可达：https://<域名>/v1/webhooks/alipay/pay

	// === 平台公司抬头（创作者开票时需要照此填）===
	// 配置来源：海南琅智网络科技有限公司
	// 创作者对账单 PDF/Excel 上展示"请开给：xxx"，所有抬头字段从 .env 读，便于后期换公司。
	PlatformCompanyName string // 平台公司全称（默认：海南琅智网络科技有限公司）
	PlatformTaxNo       string // 纳税识别号（统一社会信用代码）
	PlatformBankName    string // 开户行（如"招商银行海口分行"）
	PlatformBankAccount string // 银行账号
	PlatformAddress     string // 注册地址
	PlatformPhone       string // 公司电话
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
		DBMaxOpenConns:    getEnvInt("DB_MAX_OPEN_CONNS", 20), // 单实例上限；两实例=40 < PG max_connections(100)，留余量给 psql/迁移/工具
		DBMaxIdleConns:    getEnvInt("DB_MAX_IDLE_CONNS", 10),
		DBConnMaxLifetime: time.Duration(getEnvInt("DB_CONN_MAX_LIFETIME_SECONDS", 1800)) * time.Second,
		DBConnMaxIdleTime: time.Duration(getEnvInt("DB_CONN_MAX_IDLE_SECONDS", 300)) * time.Second,

		RateLimitEnabled: getEnvBool("RATE_LIMIT_ENABLED", true), // 默认开：新部署忘配也有兜底抗滥用
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

		AdminInitUsername: getEnv("ADMIN_INIT_USERNAME", "郎志"), // 2026-07-02 会议：吴总要求 admin 账号改为郎志（默认；老 admin 账号保留不删）
		AdminInitPassword: getEnv("ADMIN_INIT_PASSWORD", "admin123"),

		BcryptCost: getEnvInt("BCRYPT_COST", 10),

		TencentcloudSecretID:  getEnv("TENCENTCLOUD_SECRET_ID", ""),
		TencentcloudSecretKey: getEnv("TENCENTCLOUD_SECRET_KEY", ""),

		SMSSDKAppID:            getEnv("SMS_SDK_APP_ID", ""),
		SMSSignName:            getEnv("SMS_SIGN_NAME", ""),
		SMSTemplateLogin:       getEnv("SMS_TEMPLATE_LOGIN", ""),
		SMSTemplateLoginParams: getEnv("SMS_TEMPLATE_LOGIN_PARAMS", "code"),
		SMSRegion:              getEnv("SMS_REGION", "ap-guangzhou"),

		KYCDevMode:      getEnvBool("KYC_DEV_MODE", true),
		KYCFaceIDRegion: getEnv("KYC_FACEID_REGION", "ap-guangzhou"),
		KYCOCRRegion:    getEnv("KYC_OCR_REGION", "ap-guangzhou"),

		CreatorShareRate:   getEnvFloat("CREATOR_SHARE_RATE", 0.5),
		MinWithdrawalCents: int64(getEnvInt("MIN_WITHDRAWAL_CENTS", 10000)),
		// 本地关单 45min > 第三方支付有效期 30min：先到第三方有效期付不了，我们才关单，杜绝"已关单仍可支付"窗口。
		OrderPendingTTL: time.Duration(getEnvInt("ORDER_PENDING_TTL_SECONDS", 2700)) * time.Second,
		PaymentExpire:   time.Duration(getEnvInt("PAYMENT_EXPIRE_SECONDS", 1800)) * time.Second,

		DataEncryptionKeyB64: getEnv("DATA_ENCRYPTION_KEY", ""),

		PaymentDevMode: getEnvBool("PAYMENT_DEV_MODE", true),
		SeedMockData:   getEnvBool("SEED_MOCK_DATA", false),

		COSBucket:    getEnv("COS_BUCKET", ""),
		COSRegion:    getEnv("COS_REGION", "ap-guangzhou"),
		COSAppID:     getEnv("COS_APP_ID", ""),
		COSSecretID:  getEnv("COS_SECRET_ID", ""),
		COSSecretKey: getEnv("COS_SECRET_KEY", ""),
		COSCDNDomain: getEnv("COS_CDN_DOMAIN", ""),

		COSRefererWhitelist: getEnv("COS_REFERER_WHITELIST", ""),
		COSSignExpire: time.Duration(getEnvInt("COS_SIGN_EXPIRE_SECONDS", 900)) * time.Second,

		VODSubAppID:    uint64(getEnvInt("VOD_SUB_APP_ID", 0)),
		VODRegion:      getEnv("VOD_REGION", "ap-chongqing"),
		VODSecretID:    getEnv("VOD_SECRET_ID", ""),
		VODSecretKey:   getEnv("VOD_SECRET_KEY", ""),
		VODProcedure:   getEnv("VOD_PROCEDURE_NAME", ""),
		VODCallbackKey: getEnv("VOD_CALLBACK_KEY", ""),
		VODCDNDomain:   getEnv("VOD_CDN_DOMAIN", ""),
		VODSignExpire:  time.Duration(getEnvInt("VOD_SIGN_EXPIRE_SECONDS", 3600)) * time.Second,

		VODPlaySignEnabled: getEnvBool("VOD_PLAY_SIGN_ENABLED", false),
		VODPlaySignKey:     getEnv("VOD_PLAY_SIGN_KEY", ""),
		VODPlaySignExpire:  time.Duration(getEnvInt("VOD_PLAY_SIGN_EXPIRE_SECONDS", 3600)) * time.Second,
		VODPlaySignExper:   getEnvInt("VOD_PLAY_SIGN_EXPER", 0),
		VODPlaySignRlimit:  getEnvInt("VOD_PLAY_SIGN_RLIMIT", 0),
		WechatAppID:             getEnv("WECHAT_APP_ID", ""),
		WechatMchID:             getEnv("WECHAT_MCH_ID", ""),
		WechatAPIKeyV3:          getEnv("WECHAT_API_KEY_V3", ""),
		WechatMchCertSerialNo:   getEnv("WECHAT_MCH_CERT_SERIAL", ""),
		WechatMchPrivateKeyPath: getEnv("WECHAT_MCH_PRIVATE_KEY_PATH", ""),
		WechatNotifyURL:         getEnv("WECHAT_NOTIFY_URL", ""),

		AlipayAppID:          getEnv("ALIPAY_APP_ID", ""),
		AlipayPrivateKey:     getEnv("ALIPAY_PRIVATE_KEY", ""),
		AlipayPrivateKeyPath: getEnv("ALIPAY_PRIVATE_KEY_PATH", ""),
		AlipayPublicKey:      getEnv("ALIPAY_PUBLIC_KEY", ""),
		AlipayPublicKeyPath:  getEnv("ALIPAY_PUBLIC_KEY_PATH", ""),
		AlipaySandbox:        getEnvBool("ALIPAY_SANDBOX", true),
		AlipayNotifyURL:      getEnv("ALIPAY_NOTIFY_URL", ""),

		// === 平台公司抬头 ===
		// 默认"海南琅智网络科技有限公司"——业务实际开票主体（黄少政 6/30 拍板）。
		// 沙箱 .env 可空着；生产 .env 必须填齐（PLATFORM_TAX_NO 等必填），
		// 否则创作者对账单上开票信息不全，财务对接会很麻烦。
		PlatformCompanyName: getEnv("PLATFORM_COMPANY_NAME", "海南琅智网络科技有限公司"),
		PlatformTaxNo:       getEnv("PLATFORM_TAX_NO", ""),
		PlatformBankName:    getEnv("PLATFORM_BANK_NAME", ""),
		PlatformBankAccount: getEnv("PLATFORM_BANK_ACCOUNT", ""),
		PlatformAddress:     getEnv("PLATFORM_ADDRESS", ""),
		PlatformPhone:       getEnv("PLATFORM_PHONE", ""),
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
