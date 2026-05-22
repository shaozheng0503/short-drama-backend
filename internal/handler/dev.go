package handler

import (
	"errors"
	"log"
	"time"

	"ai-drama-platform/internal/billing"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"
	"ai-drama-platform/internal/seed"

	"github.com/gin-gonic/gin"
)

// devSeed 一键灌 mock 短剧 / 剧集 / 用户 / 订单数据，幂等：已存在的会跳过。
// 仅在 PAYMENT_DEV_MODE=true 时挂载，路径：POST /v1/dev/seed
func (s *Server) devSeed(c *gin.Context) {
	result, err := seed.Run(s.db)
	if err != nil {
		log.Printf("[dev] seed err=%v", err)
		response.ServerError(c, "seed 失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"mock": true, "seeded": result})
}

// devMockPayOrder 一键模拟支付成功，仅在 PAYMENT_DEV_MODE=true 时挂载。
// 前端联调路径：POST /v1/app/orders 拿到 order_no → POST /v1/dev/orders/:order_no/pay
// 走的是和真实 webhook 同一条 billing.MarkOrderPaid 链路，会触发解锁 + 分账。
func (s *Server) devMockPayOrder(c *gin.Context) {
	orderNo := c.Param("order_no")
	if orderNo == "" {
		response.InvalidParam(c, "order_no 必填")
		return
	}
	var order model.Order
	if err := s.db.Where("order_no = ?", orderNo).First(&order).Error; err != nil {
		response.NotFound(c, "订单不存在")
		return
	}

	paidAt := time.Now()
	tradeNo := "DEV-MOCK-" + orderNo
	log.Printf("[payment-dev] mock-pay endpoint order_no=%s amount=%d method=%s",
		orderNo, order.AmountCents, order.PaymentMethod)

	if err := s.billing.MarkOrderPaid(orderNo, tradeNo, order.PaymentMethod, order.AmountCents, paidAt); err != nil {
		switch {
		case errors.Is(err, billing.ErrOrderNotFound):
			response.NotFound(c, "订单不存在")
		case errors.Is(err, billing.ErrOrderExpired):
			response.Conflict(c, "订单已过期，不能再标记已支付")
		case errors.Is(err, billing.ErrOrderNotPaid):
			response.Conflict(c, "订单状态非法（已关闭或已退款）")
		default:
			response.ServerError(c, "mock 支付失败")
		}
		return
	}

	response.OK(c, gin.H{
		"order_no":          orderNo,
		"status":            "paid",
		"paid_at":           paidAt,
		"platform_trade_no": tradeNo,
		"mock":              true,
	})
}
