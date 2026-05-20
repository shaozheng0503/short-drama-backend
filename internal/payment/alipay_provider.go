package payment

import (
	"log"

	"ai-drama-platform/internal/config"
)

// AlipayProvider 支付宝 OpenAPI 接入位（stub）。
//
// 真接入路径：
//  1. 用 github.com/smartwalle/alipay/v3 或自实现 RSA2 签名。
//  2. Prepay：调用 alipay.trade.app.pay 拿到 orderStr 字符串直接返给前端。
//  3. VerifyAndParse：用支付宝公钥 + RSA2 验签 sign 字段；trade_status 为 TRADE_SUCCESS 视为 paid。
type AlipayProvider struct {
	cfg config.Config
}

func (*AlipayProvider) Method() string { return "alipay" }

func (p *AlipayProvider) Prepay(_ PrepayInput) (PrepayParams, error) {
	log.Printf("[payment-alipay] (stub) prepay not implemented")
	return nil, ErrProviderUnavailable
}

func (p *AlipayProvider) VerifyAndParse(_ map[string]string, _ []byte) (*WebhookEvent, error) {
	log.Printf("[payment-alipay] (stub) verify not implemented")
	return nil, ErrProviderUnavailable
}
