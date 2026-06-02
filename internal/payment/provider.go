package payment

import (
	"errors"
	"log"

	"ai-drama-platform/internal/config"
)

// PrepayParams 各支付渠道返回给前端唤起支付的参数；MVP 用通用 map，避免渠道 struct 污染上层。
type PrepayParams map[string]string

// PrepayInput 创建预支付订单需要的最小信息。
type PrepayInput struct {
	OrderNo     string
	AmountCents int64
	Subject     string
	UserID      uint64
	Scene       string // 支付宝多端：app（默认）/ wap；其它渠道忽略
}

// WebhookEvent 解析回调后得到的标准化事件。
type WebhookEvent struct {
	OrderNo         string
	Paid            bool
	PlatformTradeNo string
	AmountCents     int64
}

type Provider interface {
	Method() string
	Prepay(input PrepayInput) (PrepayParams, error)
	VerifyAndParse(headers map[string]string, body []byte) (*WebhookEvent, error)
}

var (
	ErrProviderUnavailable = errors.New("支付 provider 不可用")
	ErrVerifyFailed        = errors.New("支付回调验签失败")
	ErrUnsupportedMethod   = errors.New("不支持的支付方式")
)

// Registry 持有当前可用的 provider 集合。
// 调用方按 payment_method 取，找不到说明该渠道没接入。
type Registry struct {
	providers map[string]Provider
}

func NewRegistry(cfg config.Config) *Registry {
	reg := &Registry{providers: map[string]Provider{}}

	// 微信：dev 模式走 stub；生产缺配置时拒绝，避免无验签回调被伪造。
	if cfg.PaymentDevMode {
		reg.providers["wechat"] = &DevProvider{method: "wechat"}
	} else if cfg.WechatAppID == "" || cfg.WechatMchID == "" || cfg.WechatAPIKeyV3 == "" {
		reg.providers["wechat"] = &UnavailableProvider{method: "wechat"}
		log.Printf("[payment] PAYMENT_DEV_MODE=false 但微信支付配置不全，wechat 已禁用")
	} else {
		reg.providers["wechat"] = &WechatProvider{cfg: cfg}
	}

	// 支付宝：只要配齐 app_id+应用私钥+支付宝公钥，就启用真实 provider（沙箱或生产由
	// ALIPAY_SANDBOX 决定）——这样沙箱联调期填了密钥即走真实沙箱，而微信仍可走 dev stub。
	// 初始化失败（密钥格式错等）降级为 Unavailable，避免无验签兜底。
	alipayConfigured := cfg.AlipayAppID != "" && cfg.AlipayPrivateKey != "" && cfg.AlipayPublicKey != ""
	switch {
	case alipayConfigured:
		ap, err := NewAlipayProvider(cfg)
		if err != nil {
			log.Printf("[payment] 支付宝 provider 初始化失败，alipay 已禁用: %v", err)
			reg.providers["alipay"] = &UnavailableProvider{method: "alipay"}
		} else {
			mode := "生产"
			if cfg.AlipaySandbox {
				mode = "沙箱"
			}
			log.Printf("[payment] 支付宝 provider 已启用（%s 网关）", mode)
			reg.providers["alipay"] = ap
		}
	case cfg.PaymentDevMode:
		reg.providers["alipay"] = &DevProvider{method: "alipay"}
	default:
		reg.providers["alipay"] = &UnavailableProvider{method: "alipay"}
		log.Printf("[payment] PAYMENT_DEV_MODE=false 但支付宝配置不全，alipay 已禁用")
	}

	return reg
}

func (r *Registry) Get(method string) (Provider, error) {
	p, ok := r.providers[method]
	if !ok {
		return nil, ErrUnsupportedMethod
	}
	return p, nil
}
