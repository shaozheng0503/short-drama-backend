package payment

import (
	"errors"
	"log"
	"time"

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
	Scene       string    // 多端：app（默认）/ wap(h5)；微信 / 支付宝按此选产品
	ClientIP    string    // 微信 H5 支付必填 payer_client_ip；其它场景可空
	ExpireAt    time.Time // 第三方支付有效期（绝对时间）；零值表示不显式设置走渠道默认。必须早于本地关单时间，防"已关单仍可支付"资损。
}

// WebhookEvent 解析回调后得到的标准化事件。
type WebhookEvent struct {
	OrderNo         string
	Paid            bool
	PlatformTradeNo string
	AmountCents     int64
}

// OrderState 主动查单返回的渠道侧订单状态;用于 webhook 丢失时由后台兜底同步。
//
//	Status 沿用本地 OrderStatus 词表(pending / paid / closed / refunded),由 Provider 把渠道侧
//	的状态归一化到这套词表;PaidAt 没有时为 nil(例如未支付订单)。
type OrderState struct {
	OrderNo         string
	Status          string
	PlatformTradeNo string
	AmountCents     int64
	PaidAt          *time.Time
}

// RefundInput 发起退款的最小参数。
//
//	RefundNo 是商户侧退款单号,要保证同一订单"同号同语义";支付宝/微信都用它做幂等键,
//	部分退款必传;同一单退款额可以等于剩余可退金额(全退)或更小(部分退)。
type RefundInput struct {
	OrderNo         string // 商户订单号
	PlatformTradeNo string // 渠道交易号(可空,SDK 会按订单号查)
	RefundNo        string // 商户退款单号(幂等键)
	AmountCents     int64  // 本次退款金额
	Reason          string // 退款原因(可空)
}

// RefundResult Provider 退款返回。
//
//	Success=true 表示渠道侧已受理(同步退款) 或 已退款成功;
//	PlatformRefundNo 是渠道侧的退款流水号,有就回填,没有就空。
//	RefundedAt 没有就取本地 time.Now()(在 billing 层处理),Provider 不强制返回。
type RefundResult struct {
	Success          bool
	RefundNo         string
	PlatformRefundNo string
	RefundedAt       time.Time
	RawMessage       string // 渠道侧原始 message,失败时透出便于排障
}

type Provider interface {
	Method() string
	Prepay(input PrepayInput) (PrepayParams, error)
	VerifyAndParse(headers map[string]string, body []byte) (*WebhookEvent, error)
	// QueryOrder 主动查单。返回的 Status 已按本地词表归一化。
	QueryOrder(orderNo string) (*OrderState, error)
	// Refund 发起退款。同 RefundNo 重入要保持幂等。
	Refund(input RefundInput) (*RefundResult, error)
	// CloseOrder 关闭渠道侧未支付订单，作废其支付链接，防"本地已关单但渠道仍可支付"资损。
	// 渠道侧订单不存在 / 已关闭 / 已支付等情况按幂等处理（不返回错误）。
	CloseOrder(orderNo string) error
}

var (
	ErrProviderUnavailable = errors.New("支付 provider 不可用")
	ErrVerifyFailed        = errors.New("支付回调验签失败")
	ErrUnsupportedMethod   = errors.New("不支持的支付方式")
	ErrRefundFailed        = errors.New("渠道侧退款失败")
)

// OrderState.Status 归一化词表,与 model.OrderStatus* 对齐(此处不 import model 防循环依赖)。
const (
	StatusPending  = "pending"
	StatusPaid     = "paid"
	StatusClosed   = "closed"
	StatusRefunded = "refunded"
)

// Registry 持有当前可用的 provider 集合。
// 调用方按 payment_method 取，找不到说明该渠道没接入。
type Registry struct {
	providers map[string]Provider
}

func NewRegistry(cfg config.Config) *Registry {
	reg := &Registry{providers: map[string]Provider{}}

	// 微信：配齐 app_id+mch_id+APIv3 密钥+证书序列号+商户私钥文件，就启用真实 provider；
	// 初始化失败（如平台证书下载失败）降级 Unavailable，避免无验签兜底。否则按 dev 模式走 stub。
	// 注意：微信 V3 没有可用沙箱，真实 provider 需真实商户资质 + 公网才能跑通。
	wechatConfigured := cfg.WechatAppID != "" && cfg.WechatMchID != "" && cfg.WechatAPIKeyV3 != "" &&
		cfg.WechatMchCertSerialNo != "" && cfg.WechatMchPrivateKeyPath != ""
	switch {
	case wechatConfigured:
		wp, err := NewWechatProvider(cfg)
		if err != nil {
			log.Printf("[payment] 微信 provider 初始化失败，wechat 已禁用: %v", err)
			reg.providers["wechat"] = &UnavailableProvider{method: "wechat"}
		} else {
			log.Printf("[payment] 微信 provider 已启用")
			reg.providers["wechat"] = wp
		}
	case cfg.PaymentDevMode:
		reg.providers["wechat"] = &DevProvider{method: "wechat"}
	default:
		reg.providers["wechat"] = &UnavailableProvider{method: "wechat"}
		log.Printf("[payment] PAYMENT_DEV_MODE=false 但微信支付配置不全，wechat 已禁用")
	}

	// 支付宝：只要配齐 app_id+应用私钥+支付宝公钥，就启用真实 provider（沙箱或生产由
	// ALIPAY_SANDBOX 决定）——这样沙箱联调期填了密钥即走真实沙箱，而微信仍可走 dev stub。
	// 初始化失败（密钥格式错等）降级为 Unavailable，避免无验签兜底。
	// 密钥可走内联字符串(*_KEY)或文件路径(*_KEY_PATH)任一——与 NewAlipayProvider/readAlipayMaterial
	// 的读取优先级一致；只检查内联会让路径配置被误判为未配置而降级 Unavailable。
	alipayConfigured := cfg.AlipayAppID != "" &&
		(cfg.AlipayPrivateKey != "" || cfg.AlipayPrivateKeyPath != "") &&
		(cfg.AlipayPublicKey != "" || cfg.AlipayPublicKeyPath != "")
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
