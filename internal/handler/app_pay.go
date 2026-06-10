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
	PayScene      string  `json:"pay_scene"` // 支付宝多端：app（默认）/ wap；微信忽略
}

func (s *Server) appCreateOrder(c *gin.Context) {
	uid := middleware.CurrentID(c)
	var req createOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}
	if idem := c.GetHeader("Idempotency-Key"); idem != "" {
		// Redis 强幂等已由 idempotencyMiddleware 处理（响应缓存 + 同 key 不同 body 拒绝）；这里只是审计日志。
		log.Printf("[order] user=%d idem=%s drama=%d episode=%d", uid, idem, req.DramaID, req.EpisodeID)
	}

	outcome, err := s.billing.CreateOrReuseOrder(uid, req.DramaID, req.EpisodeID, req.ProductID, req.PaymentMethod, req.PayScene, c.ClientIP())
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

// respondBillingOrderError 把 billing 下单错误统一映射为 HTTP 响应（批量/单集共用）。
func respondBillingOrderError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, billing.ErrEpisodeNotFound):
		response.NotFound(c, "剧集不存在")
	case errors.Is(err, billing.ErrEpisodeNotReady):
		response.InvalidParam(c, "存在尚未就绪的剧集，不能下单")
	case errors.Is(err, billing.ErrDramaNotFound):
		response.NotFound(c, "短剧不存在")
	case errors.Is(err, billing.ErrDramaNotAvailable):
		response.InvalidParam(c, "短剧未上架或已下架，不能下单")
	case errors.Is(err, billing.ErrOrderEpisodeMatch):
		response.InvalidParam(c, "存在不属于该短剧的剧集")
	case errors.Is(err, billing.ErrEpisodeFree):
		response.InvalidParam(c, "包含免费集，无需购买")
	case errors.Is(err, billing.ErrAmountInvalid):
		response.InvalidParam(c, "短剧未设置单集价格")
	case errors.Is(err, payment.ErrUnsupportedMethod):
		response.InvalidParam(c, "payment_method 仅支持 wechat / alipay")
	case errors.Is(err, payment.ErrProviderUnavailable):
		response.FailWithData(c, response.CodeThirdPartyError, "支付通道暂不可用", nil)
	default:
		log.Printf("[order] err=%v", err)
		response.ServerError(c, "下单失败")
	}
}

type createBatchOrderRequest struct {
	DramaID       uint64   `json:"drama_id" binding:"required"`
	EpisodeIDs    []uint64 `json:"episode_ids" binding:"required"`
	PaymentMethod string   `json:"payment_method" binding:"required"`
	PayScene      string   `json:"pay_scene"` // 支付宝多端：app（默认）/ wap
}

// appCreateBatchOrder —— 选集购买：一笔订单买多集。POST /v1/app/orders/batch
// 自动剔除免费 / 已解锁集，金额 = 可购买集数 × 单价；可购买为空 → {already_unlocked:true}。
func (s *Server) appCreateBatchOrder(c *gin.Context) {
	uid := middleware.CurrentID(c)
	var req createBatchOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.EpisodeIDs) == 0 {
		response.InvalidParam(c, "drama_id / episode_ids / payment_method 必填")
		return
	}
	outcome, err := s.billing.CreateBatchOrder(uid, req.DramaID, req.EpisodeIDs, req.PaymentMethod, req.PayScene, c.ClientIP())
	if err != nil {
		respondBillingOrderError(c, err)
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
		"episode_count":  len(o.EpisodeIDs),
		"episode_ids":    o.EpisodeIDs,
		"payment_method": o.PaymentMethod,
		"pay_params":     outcome.PayParams,
		"expired_at":     o.ExpiredAt,
	})
}

type batchPreviewRequest struct {
	DramaID    uint64   `json:"drama_id" binding:"required"`
	EpisodeIDs []uint64 `json:"episode_ids" binding:"required"`
}

// appBatchOrderPreview —— 选集购买试算（只读，不下单）。POST /v1/app/orders/batch/preview
func (s *Server) appBatchOrderPreview(c *gin.Context) {
	uid := middleware.CurrentID(c)
	var req batchPreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.EpisodeIDs) == 0 {
		response.InvalidParam(c, "drama_id / episode_ids 必填")
		return
	}
	q, err := s.billing.QuoteBatch(uid, req.DramaID, req.EpisodeIDs)
	if err != nil {
		respondBillingOrderError(c, err)
		return
	}
	response.OK(c, gin.H{
		"unit_price_cents":             q.UnitPriceCents,
		"buyable_episode_ids":          q.BuyableEpisodeIDs,
		"buyable_count":                len(q.BuyableEpisodeIDs),
		"already_unlocked_episode_ids": q.AlreadyUnlocked,
		"amount_cents":                 q.AmountCents,
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
