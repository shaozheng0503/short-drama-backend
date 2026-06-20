package payment

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

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
//
// 私钥 / 公钥优先用文件路径（*_PATH），避免把 PEM 文本写进 .env/commit/聊天，泄漏面更小；
// 没填路径时回退读环境变量里的字符串。
func NewAlipayProvider(cfg config.Config) (*AlipayProvider, error) {
	privateKey, err := readAlipayMaterial(cfg.AlipayPrivateKeyPath, cfg.AlipayPrivateKey, "ALIPAY_PRIVATE_KEY")
	if err != nil {
		return nil, err
	}
	publicKey, err := readAlipayMaterial(cfg.AlipayPublicKeyPath, cfg.AlipayPublicKey, "ALIPAY_PUBLIC_KEY")
	if err != nil {
		return nil, err
	}

	client, err := alipay.New(cfg.AlipayAppID, privateKey, !cfg.AlipaySandbox)
	if err != nil {
		return nil, fmt.Errorf("alipay 客户端初始化: %w", err)
	}
	// smartwalle 默认用 http.DefaultClient（无超时）：网关查单/退款若 hang 会无限占住 goroutine。
	// 覆盖成带超时的 client（Prepay 是本地签名不走网络，受影响的是 QueryOrder/Refund）。
	client.Client = &http.Client{Timeout: 15 * time.Second}
	// 公钥模式：载入支付宝公钥用于回调验签。
	if err := client.LoadAliPayPublicKey(publicKey); err != nil {
		return nil, fmt.Errorf("加载支付宝公钥: %w", err)
	}
	return &AlipayProvider{cfg: cfg, client: client}, nil
}

// readAlipayMaterial 优先用文件路径；没有路径就回退到内联字符串；都没有就报错。
// 这样 .env 里既可以写文件路径（推荐，私钥不进 env），也可以保留旧的内联方式。
func readAlipayMaterial(path, inline, envName string) (string, error) {
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("读取 %s 文件(%s): %w", envName, path, err)
		}
		s := strings.TrimSpace(string(b))
		if s == "" {
			return "", fmt.Errorf("%s 文件(%s)为空", envName, path)
		}
		return s, nil
	}
	if inline != "" {
		return inline, nil
	}
	return "", fmt.Errorf("%s 未配置（请填 *_PATH 或内联字符串）", envName)
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
		if !input.ExpireAt.IsZero() {
			req.TimeExpire = input.ExpireAt.Format("2006-01-02 15:04:05")
		}
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
		if !input.ExpireAt.IsZero() {
			req.TimeExpire = input.ExpireAt.Format("2006-01-02 15:04:05")
		}
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

// CloseOrder 关闭支付宝侧未支付订单（alipay.trade.close），作废其支付链接。
// 订单不存在 / 已关闭等按幂等处理：不返回错误，让本地关单流程继续。
func (p *AlipayProvider) CloseOrder(orderNo string) error {
	rsp, err := p.client.TradeClose(context.Background(), alipay.TradeClose{OutTradeNo: orderNo})
	if err != nil {
		return fmt.Errorf("alipay trade close: %w", err)
	}
	if rsp.IsSuccess() {
		return nil
	}
	switch rsp.SubCode {
	// 不存在（从未上送支付）/ 已关闭 / 交易状态非法（如已支付）→ 视为无需再关，幂等返回。
	case "ACQ.TRADE_NOT_EXIST", "ACQ.TRADE_HAS_CLOSE", "ACQ.TRADE_STATUS_ERROR", "ACQ.REASON_TRADE_STATUS_INVALID":
		return nil
	}
	return fmt.Errorf("alipay trade close failed: code=%s sub=%s msg=%s", rsp.Code, rsp.SubCode, rsp.Msg)
}

func (p *AlipayProvider) VerifyAndParse(_ map[string]string, body []byte) (*WebhookEvent, error) {
	// 异步通知是 application/x-www-form-urlencoded。
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}
	// DecodeNotification 内部完成验签 + 解析;验签失败统一抛 ErrVerifyFailed。
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

// QueryOrder 主动查渠道侧订单。
// 支付宝 trade_status 归一化:WAIT_BUYER_PAY→pending,TRADE_CLOSED→closed,
// TRADE_SUCCESS→paid,TRADE_FINISHED→refunded(全额退款后渠道侧自动落该状态)。
// 订单根本未创建/已被清理时,SDK 返回 ACQ.TRADE_NOT_EXIST(Error.Code != "10000"),
// 这里映射为 ErrOrderNotFound 由上层判断;其余错误透传。
func (p *AlipayProvider) QueryOrder(orderNo string) (*OrderState, error) {
	rsp, err := p.client.TradeQuery(context.Background(), alipay.TradeQuery{OutTradeNo: orderNo})
	if err != nil {
		return nil, fmt.Errorf("alipay trade query: %w", err)
	}
	if !rsp.IsSuccess() {
		// 业务码 40004 + 子码 ACQ.TRADE_NOT_EXIST = 订单不存在(尚未上送过支付)
		if rsp.SubCode == "ACQ.TRADE_NOT_EXIST" {
			return nil, errors.New("alipay trade not exist")
		}
		return nil, fmt.Errorf("alipay trade query failed: code=%s sub=%s msg=%s", rsp.Code, rsp.SubCode, rsp.Msg)
	}
	state := &OrderState{
		OrderNo:         rsp.OutTradeNo,
		PlatformTradeNo: rsp.TradeNo,
		AmountCents:     yuanToCents(rsp.TotalAmount),
	}
	switch rsp.TradeStatus {
	case alipay.TradeStatusWaitBuyerPay:
		state.Status = StatusPending
	case alipay.TradeStatusClosed:
		state.Status = StatusClosed
	case alipay.TradeStatusSuccess:
		state.Status = StatusPaid
		if t := parseAlipayTime(rsp.SendPayDate); !t.IsZero() {
			state.PaidAt = &t
		}
	case alipay.TradeStatusFinished:
		state.Status = StatusRefunded
		if t := parseAlipayTime(rsp.SendPayDate); !t.IsZero() {
			state.PaidAt = &t
		}
	default:
		state.Status = string(rsp.TradeStatus) // 兜底:把渠道原状态透出
	}
	return state, nil
}

// Refund 调 alipay.trade.refund;同 OutRequestNo 重入由支付宝保证幂等。
// 失败统一返回 ErrRefundFailed,RawMessage 透出原始 sub_msg 便于排障。
func (p *AlipayProvider) Refund(input RefundInput) (*RefundResult, error) {
	if input.RefundNo == "" {
		return nil, fmt.Errorf("alipay refund: RefundNo 必填(支付宝侧 out_request_no)")
	}
	if input.AmountCents <= 0 {
		return nil, fmt.Errorf("alipay refund: 金额必须 > 0")
	}
	req := alipay.TradeRefund{
		OutTradeNo:   input.OrderNo,
		TradeNo:      input.PlatformTradeNo,
		RefundAmount: centsToYuan(input.AmountCents),
		RefundReason: input.Reason,
		OutRequestNo: input.RefundNo,
	}
	rsp, err := p.client.TradeRefund(context.Background(), req)
	if err != nil {
		return nil, fmt.Errorf("alipay trade refund: %w", err)
	}
	if !rsp.IsSuccess() {
		return &RefundResult{
			Success:    false,
			RefundNo:   input.RefundNo,
			RawMessage: fmt.Sprintf("code=%s sub=%s msg=%s sub_msg=%s", rsp.Code, rsp.SubCode, rsp.Msg, rsp.SubMsg),
		}, ErrRefundFailed
	}
	return &RefundResult{
		Success:          true,
		RefundNo:         input.RefundNo,
		PlatformRefundNo: rsp.TradeNo, // 支付宝退款无独立流水号,沿用 trade_no
		RefundedAt:       time.Now(),
	}, nil
}

// parseAlipayTime 解析支付宝时间字符串("2006-01-02 15:04:05",本地时区即 +08:00)。
// 无法解析返回零值,调用方按 IsZero 判断。
func parseAlipayTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
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
