package payment

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"ai-drama-platform/internal/config"

	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/core/auth/verifiers"
	"github.com/wechatpay-apiv3/wechatpay-go/core/downloader"
	"github.com/wechatpay-apiv3/wechatpay-go/core/notify"
	"github.com/wechatpay-apiv3/wechatpay-go/core/option"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments/app"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments/h5"
	"github.com/wechatpay-apiv3/wechatpay-go/utils"
)

// WechatProvider 微信支付 V3 接入。金额单位为「分」，与订单一致，无需换算。
//
//   - Prepay 按 Scene 选产品：app → APP 支付（PrepayWithRequestPayment 直接返回客户端唤起所需
//     的二次签名参数）；wap/h5 → H5 支付（返回 h5_url）。
//   - VerifyAndParse 用 notify.Handler 验签（平台证书自动下载）+ AEAD 解密，
//     trade_state == SUCCESS 视为已支付。
//
// 说明：微信 V3 无可用沙箱，需真实商户资质（证书序列号 + 商户 API 私钥）+ 公网回调才能跑通；
// NewWechatProvider 初始化时会联网下载平台证书。
type WechatProvider struct {
	cfg     config.Config
	client  *core.Client
	handler *notify.Handler
}

func NewWechatProvider(cfg config.Config) (*WechatProvider, error) {
	privateKey, err := utils.LoadPrivateKeyWithPath(cfg.WechatMchPrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("加载微信商户私钥(%s): %w", cfg.WechatMchPrivateKeyPath, err)
	}
	ctx := context.Background()
	client, err := core.NewClient(ctx, option.WithWechatPayAutoAuthCipher(
		cfg.WechatMchID, cfg.WechatMchCertSerialNo, privateKey, cfg.WechatAPIKeyV3,
	))
	if err != nil {
		return nil, fmt.Errorf("微信客户端初始化: %w", err)
	}
	// 回调验签用的平台证书由上面的 AutoAuthCipher 注册的下载器提供。
	visitor := downloader.MgrInstance().GetCertificateVisitor(cfg.WechatMchID)
	handler, err := notify.NewRSANotifyHandler(cfg.WechatAPIKeyV3, verifiers.NewSHA256WithRSAVerifier(visitor))
	if err != nil {
		return nil, fmt.Errorf("微信回调处理器初始化: %w", err)
	}
	return &WechatProvider{cfg: cfg, client: client, handler: handler}, nil
}

func (*WechatProvider) Method() string { return "wechat" }

func (p *WechatProvider) Prepay(input PrepayInput) (PrepayParams, error) {
	ctx := context.Background()
	desc := input.Subject
	if desc == "" {
		desc = "剧集解锁"
	}

	switch strings.ToLower(input.Scene) {
	case "wap", "h5":
		ip := input.ClientIP
		if ip == "" {
			ip = "127.0.0.1"
		}
		svc := h5.H5ApiService{Client: p.client}
		resp, _, err := svc.Prepay(ctx, h5.PrepayRequest{
			Appid:       core.String(p.cfg.WechatAppID),
			Mchid:       core.String(p.cfg.WechatMchID),
			Description: core.String(desc),
			OutTradeNo:  core.String(input.OrderNo),
			NotifyUrl:   core.String(p.cfg.WechatNotifyURL),
			Amount:      &h5.Amount{Total: core.Int64(input.AmountCents), Currency: core.String("CNY")},
			SceneInfo: &h5.SceneInfo{
				PayerClientIp: core.String(ip),
				H5Info:        &h5.H5Info{Type: core.String("Wap")},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("wechat h5 prepay: %w", err)
		}
		return PrepayParams{
			"method":   "wechat",
			"scene":    "h5",
			"order_no": input.OrderNo,
			"h5_url":   strVal(resp.H5Url),
		}, nil
	default: // app（默认）
		svc := app.AppApiService{Client: p.client}
		resp, _, err := svc.PrepayWithRequestPayment(ctx, app.PrepayRequest{
			Appid:       core.String(p.cfg.WechatAppID),
			Mchid:       core.String(p.cfg.WechatMchID),
			Description: core.String(desc),
			OutTradeNo:  core.String(input.OrderNo),
			NotifyUrl:   core.String(p.cfg.WechatNotifyURL),
			Amount:      &app.Amount{Total: core.Int64(input.AmountCents), Currency: core.String("CNY")},
		})
		if err != nil {
			return nil, fmt.Errorf("wechat app prepay: %w", err)
		}
		// 这些就是客户端 SDK 唤起微信支付所需的全部参数（二次签名已由 SDK 完成）。
		return PrepayParams{
			"method":    "wechat",
			"scene":     "app",
			"order_no":  input.OrderNo,
			"appid":     p.cfg.WechatAppID,
			"partnerid": strVal(resp.PartnerId),
			"prepayid":  strVal(resp.PrepayId),
			"package":   strVal(resp.Package),
			"noncestr":  strVal(resp.NonceStr),
			"timestamp": strVal(resp.TimeStamp),
			"sign":      strVal(resp.Sign),
		}, nil
	}
}

func (p *WechatProvider) VerifyAndParse(headers map[string]string, body []byte) (*WebhookEvent, error) {
	// notify.Handler 需要一个 *http.Request 来读取 Wechatpay-* 头 + body 验签，这里据 headers+body 重建。
	req, err := http.NewRequest(http.MethodPost, "/v1/webhooks/wechat/pay", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	tx := new(payments.Transaction)
	if _, err := p.handler.ParseNotifyRequest(context.Background(), req, tx); err != nil {
		return nil, ErrVerifyFailed
	}

	paid := tx.TradeState != nil && *tx.TradeState == "SUCCESS"
	var amount int64
	if tx.Amount != nil && tx.Amount.Total != nil {
		amount = *tx.Amount.Total
	}
	return &WebhookEvent{
		OrderNo:         strVal(tx.OutTradeNo),
		Paid:            paid,
		PlatformTradeNo: strVal(tx.TransactionId),
		AmountCents:     amount,
	}, nil
}

func strVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
