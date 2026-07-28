package handler

import (
	"errors"
	"fmt"
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
	result, err := seed.Run(s.db, s.cfg)
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

// devMockRefundOrder 一键模拟退款成功，仅在 PAYMENT_DEV_MODE=true 时挂载。
// 前端联调路径：POST /v1/dev/orders/:order_no/refund
// 走的是和真实退款同一条 billing.DevRefundOrder 链路（行锁+状态/金额/幂等校验+分账回退），
// 唯一区别是跳过渠道侧退款调用。解决沙箱配了真实支付宝凭证时退款走真渠道被拒的可测性缺口。
func (s *Server) devMockRefundOrder(c *gin.Context) {
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

	var body struct {
		AmountCents int64  `json:"amount_cents" binding:"required,gt=0"`
		Reason      string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.InvalidParam(c, "amount_cents 必填且必须大于 0")
		return
	}
	if body.AmountCents > order.AmountCents {
		response.InvalidParam(c, "退款金额不能超过订单金额")
		return
	}

	refundNo := fmt.Sprintf("DEV-RF-%s-%d", orderNo, time.Now().UnixNano())
	log.Printf("[payment-dev] mock-refund endpoint order_no=%s refund_no=%s amount=%d",
		orderNo, refundNo, body.AmountCents)

	refunded, err := s.billing.DevRefundOrder(orderNo, refundNo, body.AmountCents, body.Reason)
	if err != nil {
		switch {
		case errors.Is(err, billing.ErrOrderNotFound):
			response.NotFound(c, "订单不存在")
		case errors.Is(err, billing.ErrRefundNotAllowed):
			response.Conflict(c, "订单状态不允许退款")
		case errors.Is(err, billing.ErrRefundAmountInvalid):
			response.InvalidParam(c, "退款金额无效")
		case errors.Is(err, billing.ErrRefundNoRequired):
			response.InvalidParam(c, "refund_no 必填")
		default:
			response.ServerError(c, "mock 退款失败")
		}
		return
	}

	response.OK(c, gin.H{
		"order_no":           orderNo,
		"status":             refunded.Status,
		"refund_amount_cents": refunded.RefundAmountCents,
		"refund_no":           refunded.RefundNo,
		"platform_refund_no": refunded.PlatformRefundNo,
		"mock":               true,
	})
}

// devMockPayRecharge 一键模拟押金充值到账，仅在 PAYMENT_DEV_MODE=true 时挂载。
// 前端联调路径：POST /v1/dev/recharges/:recharge_no/pay
// 走的是和真实 webhook 同一条 billing.MarkRechargePaid 链路，会触发加余额 + 写流水。
func (s *Server) devMockPayRecharge(c *gin.Context) {
	rechargeNo := c.Param("recharge_no")
	if rechargeNo == "" {
		response.InvalidParam(c, "recharge_no 必填")
		return
	}
	var rc model.DistributorRecharge
	if err := s.db.Where("recharge_no = ?", rechargeNo).First(&rc).Error; err != nil {
		response.NotFound(c, "充值单不存在")
		return
	}

	paidAt := time.Now()
	tradeNo := "DEV-MOCK-" + rechargeNo
	log.Printf("[payment-dev] mock-pay-recharge recharge_no=%s amount=%d method=%s",
		rechargeNo, rc.AmountCents, rc.PaymentMethod)

	if err := s.billing.MarkRechargePaid(rechargeNo, tradeNo, rc.PaymentMethod, rc.AmountCents, paidAt); err != nil {
		switch {
		case errors.Is(err, billing.ErrRechargeNotFound):
			response.NotFound(c, "充值单不存在")
		case errors.Is(err, billing.ErrRechargeAmountMismatch):
			response.Conflict(c, "充值金额不一致")
		default:
			response.ServerError(c, "mock 充值到账失败")
		}
		return
	}

	response.OK(c, gin.H{
		"recharge_no":       rechargeNo,
		"status":            "paid",
		"paid_at":           paidAt,
		"platform_trade_no": tradeNo,
		"mock":              true,
	})
}
