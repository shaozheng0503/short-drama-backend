package handler

import (
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"ai-drama-platform/internal/alert"
	"ai-drama-platform/internal/billing"
	"ai-drama-platform/internal/payment"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

// ackPayWebhook 回执：支付宝异步通知要求 HTTP body 恰好为纯文本 "success"，否则会持续重试；
// 其它渠道（微信/dev）沿用 JSON ack。需重试/失败的场景仍走 response.WebhookRetry（非 200）。
func ackPayWebhook(c *gin.Context, method string, body gin.H) {
	switch method {
	case "alipay":
		// 支付宝要求 body 恰好为纯文本 "success"。
		c.String(http.StatusOK, "success")
	case "wechat":
		// 微信 V3 要求 200 + {"code":"SUCCESS","message":"成功"}，否则会重试。
		c.JSON(http.StatusOK, gin.H{"code": "SUCCESS", "message": "成功"})
	default:
		response.OK(c, body)
	}
}

func (s *Server) webhookWechatPay(c *gin.Context) {
	s.handlePayWebhook(c, "wechat")
}

func (s *Server) webhookAlipayPay(c *gin.Context) {
	s.handlePayWebhook(c, "alipay")
}

func (s *Server) handlePayWebhook(c *gin.Context, method string) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		response.WebhookRetry(c, "读取请求体失败")
		return
	}
	headers := map[string]string{}
	for k, v := range c.Request.Header {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}

	provider, err := s.payments.Get(method)
	if err != nil {
		response.WebhookRetry(c, "支付渠道不可用")
		return
	}
	event, err := provider.VerifyAndParse(headers, body)
	if err != nil {
		if errors.Is(err, payment.ErrVerifyFailed) {
			log.Printf("[webhook] %s verify failed", method)
			response.WebhookUnauthorized(c, "验签失败")
			return
		}
		log.Printf("[webhook] %s parse err=%v", method, err)
		response.WebhookRetry(c, "回调解析失败")
		return
	}

	if event == nil || event.OrderNo == "" {
		response.WebhookRetry(c, "order_no 缺失")
		return
	}

	if !event.Paid {
		log.Printf("[webhook] %s order=%s non-paid event ignored", method, event.OrderNo)
		ackPayWebhook(c, method, gin.H{"ack": true})
		return
	}

	paidAt := time.Now()
	if err := s.billing.MarkOrderPaid(event.OrderNo, event.PlatformTradeNo, method, event.AmountCents, paidAt); err != nil {
		switch {
		case errors.Is(err, billing.ErrOrderNotFound):
			// 可能是押金充值单（RC 开头），尝试 MarkRechargePaid
			if err2 := s.billing.MarkRechargePaid(event.OrderNo, event.PlatformTradeNo, method, event.AmountCents, paidAt); err2 != nil {
				log.Printf("[webhook] %s order/recharge=%s not found or failed: order_err=%v recharge_err=%v", method, event.OrderNo, err, err2)
				response.WebhookRetry(c, "订单/充值单不存在")
			} else {
				log.Printf("[webhook] %s recharge=%s marked paid", method, event.OrderNo)
				ackPayWebhook(c, method, gin.H{"ack": true})
			}
		case errors.Is(err, billing.ErrOrderNotPaid):
			log.Printf("[webhook] %s order=%s invalid status", method, event.OrderNo)
			s.alerts.SendAsync(alert.Event{
				Level:   "error",
				Type:    "payment_webhook_failed",
				Message: "支付回调订单状态非法",
				Fields: map[string]interface{}{
					"method":   method,
					"order_no": event.OrderNo,
					"error":    err.Error(),
				},
			})
			response.WebhookRetry(c, "订单状态非法，无法标记已支付")
		case errors.Is(err, billing.ErrOrderAmountMismatch):
			log.Printf("[webhook] %s order=%s amount mismatch", method, event.OrderNo)
			s.alerts.SendAsync(alert.Event{
				Level:   "error",
				Type:    "payment_webhook_failed",
				Message: "支付回调金额不一致",
				Fields: map[string]interface{}{
					"method":   method,
					"order_no": event.OrderNo,
					"error":    err.Error(),
				},
			})
			response.WebhookRetry(c, "支付金额与订单金额不一致")
		case errors.Is(err, billing.ErrOrderExpired):
			log.Printf("[webhook] %s order=%s expired, refused to mark paid", method, event.OrderNo)
			s.alerts.SendAsync(alert.Event{
				Level:   "error",
				Type:    "payment_webhook_late",
				Message: "支付回调到达时订单已过期，已拒绝并 ack；需人工核对是否退款",
				Fields: map[string]interface{}{
					"method":            method,
					"order_no":          event.OrderNo,
					"platform_trade_no": event.PlatformTradeNo,
					"amount_cents":      event.AmountCents,
				},
			})
			// ack 200：渠道无需重试，但 ops 必须人工跟进退款
			ackPayWebhook(c, method, gin.H{"ack": true, "ignored": "order_expired"})
			return
		case errors.Is(err, billing.ErrPaymentMethodMismatch):
			log.Printf("[webhook] %s order=%s method mismatch", method, event.OrderNo)
			s.alerts.SendAsync(alert.Event{
				Level:   "error",
				Type:    "payment_webhook_failed",
				Message: "支付回调渠道不一致",
				Fields: map[string]interface{}{
					"method":   method,
					"order_no": event.OrderNo,
					"error":    err.Error(),
				},
			})
			response.WebhookRetry(c, "支付渠道与订单不一致")
		default:
			log.Printf("[webhook] %s mark paid err=%v", method, err)
			s.alerts.SendAsync(alert.Event{
				Level:   "error",
				Type:    "payment_webhook_failed",
				Message: "支付回调处理失败",
				Fields: map[string]interface{}{
					"method":   method,
					"order_no": event.OrderNo,
					"error":    err.Error(),
				},
			})
			response.WebhookRetry(c, "处理失败")
		}
		return
	}
	log.Printf("[webhook] %s order=%s marked paid", method, event.OrderNo)
	ackPayWebhook(c, method, gin.H{"ack": true})
}
