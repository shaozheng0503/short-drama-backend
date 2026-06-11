package payment

import (
	"encoding/json"
	"log"
	"time"
)

// DevProvider 同时充当 wechat / alipay 的 stub:
// - Prepay 返回固定占位参数 + 调试用的 `dev_pay_url`,前端在 dev 模式下可以直接 POST 到该地址完成"模拟支付"。
// - VerifyAndParse 不强校验签名,要求 body 是合法 JSON {order_no, amount_cents, platform_trade_no}。
// - QueryOrder / Refund 返回固定占位,用于联调阶段在不接真实渠道的情况下走完整链路。
//
// dev 模式下走完整链路:APP 拿 prepay → 调用模拟回调 → 后端走 markOrderPaid。
type DevProvider struct {
	method string
}

func (p *DevProvider) Method() string { return p.method }

func (p *DevProvider) Prepay(input PrepayInput) (PrepayParams, error) {
	return PrepayParams{
		"method":       p.method,
		"order_no":     input.OrderNo,
		"amount_cents": itoa(input.AmountCents),
		"dev":          "true",
		"dev_callback": "POST /v1/webhooks/" + p.method + "/pay  body: {\"order_no\":\"" + input.OrderNo + "\",\"amount_cents\":" + itoa(input.AmountCents) + ",\"platform_trade_no\":\"DEV-...\",\"paid\":true}",
		"app_id":       "DEV-APP-ID",
		"prepay_id":    "DEV-PREPAY-" + input.OrderNo,
		"timestamp":    "0",
		"nonce_str":    "dev",
		"sign":         "dev",
	}, nil
}

func (p *DevProvider) VerifyAndParse(_ map[string]string, body []byte) (*WebhookEvent, error) {
	var raw struct {
		OrderNo         string `json:"order_no"`
		AmountCents     int64  `json:"amount_cents"`
		PlatformTradeNo string `json:"platform_trade_no"`
		Paid            *bool  `json:"paid"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	paid := true
	if raw.Paid != nil {
		paid = *raw.Paid
	}
	log.Printf("[payment-dev] webhook method=%s order_no=%s amount=%d paid=%v",
		p.method, raw.OrderNo, raw.AmountCents, paid)
	return &WebhookEvent{
		OrderNo:         raw.OrderNo,
		AmountCents:     raw.AmountCents,
		PlatformTradeNo: raw.PlatformTradeNo,
		Paid:            paid,
	}, nil
}

// QueryOrder dev 模式下永远报"已支付",方便联调时验证 SyncOrderStatus 链路。
// 真实订单状态以本地 DB 为准,这里只是 Provider 层契约的占位实现。
func (p *DevProvider) QueryOrder(orderNo string) (*OrderState, error) {
	now := time.Now()
	return &OrderState{
		OrderNo:         orderNo,
		Status:          StatusPaid,
		PlatformTradeNo: "DEV-TRADE-" + orderNo,
		PaidAt:          &now,
	}, nil
}

// CloseOrder dev 模式无真实渠道订单，直接幂等返回成功。
func (p *DevProvider) CloseOrder(orderNo string) error {
	log.Printf("[payment-dev] close order method=%s order_no=%s", p.method, orderNo)
	return nil
}

// Refund dev 模式直接返回成功,RefundedAt=now;不联网。
func (p *DevProvider) Refund(input RefundInput) (*RefundResult, error) {
	log.Printf("[payment-dev] refund method=%s order_no=%s refund_no=%s amount=%d reason=%q",
		p.method, input.OrderNo, input.RefundNo, input.AmountCents, input.Reason)
	return &RefundResult{
		Success:          true,
		RefundNo:         input.RefundNo,
		PlatformRefundNo: "DEV-REFUND-" + input.RefundNo,
		RefundedAt:       time.Now(),
	}, nil
}

func itoa(n int64) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = digits[n%10]
		n /= 10
	}
	if negative {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
