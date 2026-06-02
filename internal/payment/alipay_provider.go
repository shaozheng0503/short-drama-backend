package payment

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"ai-drama-platform/internal/config"

	"github.com/smartwalle/alipay/v3"
)

// AlipayProvider 支付宝支付接入（公钥模式：应用私钥签名 + 支付宝公钥验签）。
//
//   - Prepay 按 Scene 选产品：app → alipay.trade.app.pay（返回 order_string，客户端 SDK 唤起）；
//     wap → alipay.trade.wap.pay（返回 pay_url，H5 跳转）。两者都在本地完成签名，不调网络。
//   - VerifyAndParse 用 DecodeNotification 一步验签 + 解析异步通知，
//     trade_status 为 TRADE_SUCCESS / TRADE_FINISHED 视为已支付。
type AlipayProvider struct {
	cfg    config.Config
	client *alipay.Client
}

// NewAlipayProvider 初始化支付宝客户端。production = !sandbox：
// 沙箱走 openapi.alipaydev.com，生产走 openapi.alipay.com（SDK 内部按 flag 切网关）。
func NewAlipayProvider(cfg config.Config) (*AlipayProvider, error) {
	client, err := alipay.New(cfg.AlipayAppID, cfg.AlipayPrivateKey, !cfg.AlipaySandbox)
	if err != nil {
		return nil, fmt.Errorf("alipay 客户端初始化: %w", err)
	}
	// 公钥模式：载入支付宝公钥用于回调验签。
	if err := client.LoadAliPayPublicKey(cfg.AlipayPublicKey); err != nil {
		return nil, fmt.Errorf("加载支付宝公钥: %w", err)
	}
	return &AlipayProvider{cfg: cfg, client: client}, nil
}

func (*AlipayProvider) Method() string { return "alipay" }

func (p *AlipayProvider) Prepay(input PrepayInput) (PrepayParams, error) {
	subject := input.Subject
	if subject == "" {
		subject = "剧集解锁"
	}
	amount := centsToYuan(input.AmountCents)

	switch strings.ToLower(input.Scene) {
	case "wap", "h5":
		var req = alipay.TradeWapPay{}
		req.Subject = subject
		req.OutTradeNo = input.OrderNo
		req.TotalAmount = amount
		req.ProductCode = "QUICK_WAP_WAY"
		req.NotifyURL = p.cfg.AlipayNotifyURL
		u, err := p.client.TradeWapPay(req)
		if err != nil {
			return nil, fmt.Errorf("alipay wap prepay: %w", err)
		}
		return PrepayParams{
			"method":   "alipay",
			"scene":    "wap",
			"order_no": input.OrderNo,
			"pay_url":  u.String(),
		}, nil
	default: // app（默认）
		var req = alipay.TradeAppPay{}
		req.Subject = subject
		req.OutTradeNo = input.OrderNo
		req.TotalAmount = amount
		req.ProductCode = "QUICK_MSECURITY_PAY"
		req.NotifyURL = p.cfg.AlipayNotifyURL
		orderStr, err := p.client.TradeAppPay(req)
		if err != nil {
			return nil, fmt.Errorf("alipay app prepay: %w", err)
		}
		return PrepayParams{
			"method":       "alipay",
			"scene":        "app",
			"order_no":     input.OrderNo,
			"order_string": orderStr,
		}, nil
	}
}

func (p *AlipayProvider) VerifyAndParse(_ map[string]string, body []byte) (*WebhookEvent, error) {
	// 异步通知是 application/x-www-form-urlencoded。
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}
	// DecodeNotification 内部完成验签 + 解析；验签失败统一抛 ErrVerifyFailed。
	noti, err := p.client.DecodeNotification(context.Background(), values)
	if err != nil {
		return nil, ErrVerifyFailed
	}
	paid := noti.TradeStatus == alipay.TradeStatusSuccess || noti.TradeStatus == alipay.TradeStatusFinished
	return &WebhookEvent{
		OrderNo:         noti.OutTradeNo,
		Paid:            paid,
		PlatformTradeNo: noti.TradeNo,
		AmountCents:     yuanToCents(noti.TotalAmount),
	}, nil
}

// centsToYuan 把「分」转成支付宝要求的「元，两位小数」字符串。990 → "9.90"。
func centsToYuan(cents int64) string {
	if cents < 0 {
		cents = 0
	}
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

// yuanToCents 把回调里的「元」金额转回「分」。"9.90" → 990。
func yuanToCents(yuan string) int64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(yuan), 64)
	if err != nil {
		return 0
	}
	return int64(math.Round(f * 100))
}
