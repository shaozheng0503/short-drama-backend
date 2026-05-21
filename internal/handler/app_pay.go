package handler

import (
	"errors"
	"log"

	"ai-drama-platform/internal/billing"
	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/payment"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

func (s *Server) appListProducts(c *gin.Context) {
	var items []model.Product
	if err := s.db.Where("status = ?", model.StatusActive).Order("id asc").Find(&items).Error; err != nil {
		response.ServerError(c, "查询商品失败")
		return
	}
	list := make([]gin.H, 0, len(items))
	for _, p := range items {
		list = append(list, gin.H{
			"id":          p.ID,
			"name":        p.Name,
			"type":        p.Type,
			"price_cents": p.PriceCents,
		})
	}
	response.OK(c, gin.H{"list": list})
}

type createOrderRequest struct {
	DramaID       uint64  `json:"drama_id" binding:"required"`
	EpisodeID     uint64  `json:"episode_id" binding:"required"`
	ProductID     *uint64 `json:"product_id"`
	PaymentMethod string  `json:"payment_method" binding:"required"`
}

func (s *Server) appCreateOrder(c *gin.Context) {
	uid := middleware.CurrentID(c)
	var req createOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}
	if idem := c.GetHeader("Idempotency-Key"); idem != "" {
		// TODO: 上 Redis 后做强幂等；当前依赖"复用 pending + 已 paid 直返"业务幂等。
		log.Printf("[order] user=%d idem=%s drama=%d episode=%d", uid, idem, req.DramaID, req.EpisodeID)
	}

	outcome, err := s.billing.CreateOrReuseOrder(uid, req.DramaID, req.EpisodeID, req.ProductID, req.PaymentMethod)
	if err != nil {
		switch {
		case errors.Is(err, billing.ErrEpisodeNotFound):
			response.NotFound(c, "剧集不存在")
		case errors.Is(err, billing.ErrEpisodeNotReady):
			response.InvalidParam(c, "剧集尚未就绪，不能下单")
		case errors.Is(err, billing.ErrDramaNotFound):
			response.NotFound(c, "短剧不存在")
		case errors.Is(err, billing.ErrDramaNotAvailable):
			response.InvalidParam(c, "短剧未上架或已下架，不能下单")
		case errors.Is(err, billing.ErrOrderEpisodeMatch):
			response.InvalidParam(c, "drama_id 与 episode_id 不匹配")
		case errors.Is(err, billing.ErrEpisodeFree):
			response.InvalidParam(c, "该剧集为免费集，无需支付")
		case errors.Is(err, billing.ErrAmountInvalid):
			response.InvalidParam(c, "短剧未设置单集价格")
		case errors.Is(err, payment.ErrUnsupportedMethod):
			response.InvalidParam(c, "payment_method 仅支持 wechat / alipay")
		case errors.Is(err, payment.ErrProviderUnavailable):
			response.FailWithData(c, response.CodeThirdPartyError, "支付通道暂不可用", nil)
		default:
			log.Printf("[order] create err=%v", err)
			response.ServerError(c, "下单失败")
		}
		return
	}

	if outcome.AlreadyUnlocked {
		response.OK(c, gin.H{"already_unlocked": true})
		return
	}

	o := outcome.Order
	response.OK(c, gin.H{
		"order_no":       o.OrderNo,
		"amount_cents":   o.AmountCents,
		"payment_method": o.PaymentMethod,
		"pay_params":     outcome.PayParams,
		"expired_at":     o.ExpiredAt,
	})
}

func (s *Server) appGetOrder(c *gin.Context) {
	uid := middleware.CurrentID(c)
	orderNo := c.Param("order_no")
	if orderNo == "" {
		response.InvalidParam(c, "order_no 必填")
		return
	}
	var order model.Order
	if err := s.db.Where("order_no = ?", orderNo).First(&order).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "订单不存在")
			return
		}
		response.ServerError(c, "查询订单失败")
		return
	}
	if order.UserID != uid {
		response.Forbidden(c, "订单不属于当前用户")
		return
	}
	response.OK(c, gin.H{
		"order_no":          order.OrderNo,
		"amount_cents":      order.AmountCents,
		"drama_id":          order.DramaID,
		"episode_id":        order.EpisodeID,
		"payment_method":    order.PaymentMethod,
		"status":            order.Status,
		"platform_trade_no": order.PlatformTradeNo,
		"paid_at":           order.PaidAt,
		"expired_at":        order.ExpiredAt,
		"created_at":        order.CreatedAt,
	})
}

type unlockRequest struct {
	OrderNo string `json:"order_no" binding:"required"`
}

func (s *Server) appUnlockEpisode(c *gin.Context) {
	uid := middleware.CurrentID(c)
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var req unlockRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "order_no 必填")
		return
	}
	if err := s.billing.EnsureOrderUnlocked(uid, id, req.OrderNo); err != nil {
		switch {
		case errors.Is(err, billing.ErrOrderNotFound):
			response.NotFound(c, "订单不存在")
		case errors.Is(err, billing.ErrOrderNotOwned):
			response.Forbidden(c, "订单不属于当前用户")
		case errors.Is(err, billing.ErrOrderEpisodeMatch):
			response.Fail(c, response.CodeOrderUnusable, "订单与剧集不匹配")
		case errors.Is(err, billing.ErrOrderNotPaid):
			response.Fail(c, response.CodeOrderUnusable, "订单未支付或不可用于解锁")
		default:
			response.ServerError(c, "解锁失败")
		}
		return
	}
	response.OK(c, gin.H{"unlocked": true, "episode_id": id})
}
