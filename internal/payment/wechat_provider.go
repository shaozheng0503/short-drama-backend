package payment

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"ai-drama-platform/internal/config"

	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/core/auth/verifiers"
	"github.com/wechatpay-apiv3/wechatpay-go/core/downloader"
	"github.com/wechatpay-apiv3/wechatpay-go/core/notify"
	"github.com/wechatpay-apiv3/wechatpay-go/core/option"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments/app"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments/h5"
	"github.com/wechatpay-apiv3/wechatpay-go/services/refunddomestic"
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

// QueryOrder 调微信 /v3/pay/transactions/out-trade-no/{out_trade_no} 主动查单。
// trade_state 归一化:SUCCESS→paid, REFUND→refunded, CLOSED/REVOKED→closed,
// NOTPAY/USERPAYING→pending,其它(PAYERROR 等)按原值透出。
func (p *WechatProvider) QueryOrder(orderNo string) (*OrderState, error) {
	svc := app.AppApiService{Client: p.client}
	tx, _, err := svc.QueryOrderByOutTradeNo(context.Background(), app.QueryOrderByOutTradeNoRequest{
		OutTradeNo: core.String(orderNo),
		Mchid:      core.String(p.cfg.WechatMchID),
	})
	if err != nil {
		return nil, fmt.Errorf("wechat query order: %w", err)
	}
	state := &OrderState{
		OrderNo:         strVal(tx.OutTradeNo),
		PlatformTradeNo: strVal(tx.TransactionId),
	}
	if tx.Amount != nil && tx.Amount.Total != nil {
		state.AmountCents = *tx.Amount.Total
	}
	ts := strVal(tx.TradeState)
	switch ts {
	case "SUCCESS":
		state.Status = StatusPaid
		if tx.SuccessTime != nil && *tx.SuccessTime != "" {
			if t, perr := time.Parse(time.RFC3339, *tx.SuccessTime); perr == nil {
				state.PaidAt = &t
			}
		}
	case "REFUND":
		state.Status = StatusRefunded
	case "CLOSED", "REVOKED":
		state.Status = StatusClosed
	case "NOTPAY", "USERPAYING":
		state.Status = StatusPending
	default:
		state.Status = ts
	}
	return state, nil
}

// Refund 调微信 /v3/refund/domestic/refunds。同 OutRefundNo 重入由微信保证幂等。
// 微信退款是异步的:本接口返回 PROCESSING 也算成功受理,真正到账要等退款回调。
func (p *WechatProvider) Refund(input RefundInput) (*RefundResult, error) {
	if input.RefundNo == "" {
		return nil, fmt.Errorf("wechat refund: RefundNo 必填(微信侧 out_refund_no)")
	}
	if input.AmountCents <= 0 {
		return nil, fmt.Errorf("wechat refund: 金额必须 > 0")
	}
	// 微信退款需要传"原订单总金额",这里走主动查单兜底拿;调用方也可在 PrepayParams 里把
	// AmountCents 透出来,这里宁可多花一次 RTT 也保正确。
	state, err := p.QueryOrder(input.OrderNo)
	if err != nil {
		return nil, fmt.Errorf("wechat refund 查原单失败: %w", err)
	}
	if state.AmountCents <= 0 {
		return nil, fmt.Errorf("wechat refund: 原订单金额未知,无法构造请求")
	}

	svc := refunddomestic.RefundsApiService{Client: p.client}
	resp, _, err := svc.Create(context.Background(), refunddomestic.CreateRequest{
		OutTradeNo:  core.String(input.OrderNo),
		OutRefundNo: core.String(input.RefundNo),
		Reason:      core.String(input.Reason),
		Amount: &refunddomestic.AmountReq{
			Refund:   core.Int64(input.AmountCents),
			Total:    core.Int64(state.AmountCents),
			Currency: core.String("CNY"),
		},
	})
	if err != nil {
		return &RefundResult{
			Success:    false,
			RefundNo:   input.RefundNo,
			RawMessage: err.Error(),
		}, ErrRefundFailed
	}

	result := &RefundResult{
		Success:          true,
		RefundNo:         input.RefundNo,
		PlatformRefundNo: strVal(resp.RefundId),
		RefundedAt:       time.Now(),
	}
	// status: SUCCESS / PROCESSING / CLOSED / ABNORMAL
	if resp.Status != nil {
		s := string(*resp.Status)
		switch s {
		case "SUCCESS":
			if resp.SuccessTime != nil && !resp.SuccessTime.IsZero() {
				result.RefundedAt = *resp.SuccessTime
			}
		case "PROCESSING":
			// 微信退款异步,Success=true 但等回调最终确认;无需特殊处理。
		default: // CLOSED / ABNORMAL
			result.Success = false
			result.RawMessage = "wechat refund status=" + s
			return result, ErrRefundFailed
		}
	}
	return result, nil
}
