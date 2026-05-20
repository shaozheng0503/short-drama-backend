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

	// 微信：dev 模式或缺配置时走 stub
	if cfg.PaymentDevMode || cfg.WechatAppID == "" || cfg.WechatMchID == "" || cfg.WechatAPIKeyV3 == "" {
		reg.providers["wechat"] = &DevProvider{method: "wechat"}
		if !cfg.PaymentDevMode {
			log.Printf("[payment] PAYMENT_DEV_MODE=false 但微信支付配置不全，wechat 退回 DevProvider")
		}
	} else {
		reg.providers["wechat"] = &WechatProvider{cfg: cfg}
	}

	// 支付宝：同上
	if cfg.PaymentDevMode || cfg.AlipayAppID == "" || cfg.AlipayPrivateKey == "" || cfg.AlipayPublicKey == "" {
		reg.providers["alipay"] = &DevProvider{method: "alipay"}
		if !cfg.PaymentDevMode {
			log.Printf("[payment] PAYMENT_DEV_MODE=false 但支付宝配置不全，alipay 退回 DevProvider")
		}
	} else {
		reg.providers["alipay"] = &AlipayProvider{cfg: cfg}
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
