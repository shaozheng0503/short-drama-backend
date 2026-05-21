package payment

import (
	"log"

	"ai-drama-platform/internal/config"
)

// WechatProvider 微信支付 V3 的接入位（stub）。
//
// 真接入路径：
//  1. 用 github.com/wechatpay-apiv3/wechatpay-go 或自实现签名。
//  2. Prepay：调用 v3/pay/transactions/app（或 jsapi/native），把 prepay_id 等返回。
//  3. VerifyAndParse：用平台证书校验 Wechatpay-Signature，AEAD_AES_256_GCM 解密 resource.ciphertext。
//
// 上线前还要：
//   - 商户证书 + 商户号 + APP_ID + V3 APIv3 key 全部齐
//   - 在微信商户后台配置回调域名为 https 的 /v1/webhooks/wechat/pay
type WechatProvider struct {
	cfg config.Config
}

func (*WechatProvider) Method() string { return "wechat" }

func (p *WechatProvider) Prepay(_ PrepayInput) (PrepayParams, error) {
	log.Printf("[payment-wechat] (stub) prepay not implemented")
	return nil, ErrProviderUnavailable
}

func (p *WechatProvider) VerifyAndParse(_ map[string]string, _ []byte) (*WebhookEvent, error) {
	log.Printf("[payment-wechat] (stub) verify not implemented")
	return nil, ErrProviderUnavailable
}
