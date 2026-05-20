package handler

import (
	"errors"
	"io"
	"log"
	"time"

	"ai-drama-platform/internal/billing"
	"ai-drama-platform/internal/payment"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

func (s *Server) webhookWechatPay(c *gin.Context) {
	s.handlePayWebhook(c, "wechat")
}

func (s *Server) webhookAlipayPay(c *gin.Context) {
	s.handlePayWebhook(c, "alipay")
}

func (s *Server) handlePayWebhook(c *gin.Context, method string) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		response.ServerError(c, "读取请求体失败")
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
		response.InvalidParam(c, "method 非法")
		return
	}
	event, err := provider.VerifyAndParse(headers, body)
	if err != nil {
		if errors.Is(err, payment.ErrVerifyFailed) {
			// 微信 / 支付宝在验签失败时希望返回非 success
			log.Printf("[webhook] %s verify failed", method)
			response.Fail(c, response.CodeThirdPartyError, "验签失败")
			return
		}
		log.Printf("[webhook] %s parse err=%v", method, err)
		response.InvalidParam(c, "回调解析失败")
		return
	}

	if event == nil || event.OrderNo == "" {
		response.InvalidParam(c, "order_no 缺失")
		return
	}

	if !event.Paid {
		// 支付失败 / 关闭 / 待确认：MVP 不处理，仅回 ack
		log.Printf("[webhook] %s order=%s non-paid event ignored", method, event.OrderNo)
		response.OK(c, gin.H{"ack": true})
		return
	}

	paidAt := time.Now()
	if err := s.billing.MarkOrderPaid(event.OrderNo, event.PlatformTradeNo, paidAt); err != nil {
		switch {
		case errors.Is(err, billing.ErrOrderNotFound):
			response.NotFound(c, "订单不存在")
		case errors.Is(err, billing.ErrOrderNotPaid):
			response.Fail(c, response.CodeConflict, "订单状态非法，无法标记已支付")
		default:
			log.Printf("[webhook] %s mark paid err=%v", method, err)
			// 让支付平台重试
			response.ServerError(c, "处理失败")
		}
		return
	}
	log.Printf("[webhook] %s order=%s marked paid", method, event.OrderNo)
	response.OK(c, gin.H{"ack": true})
}
