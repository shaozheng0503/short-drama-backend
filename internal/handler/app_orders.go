package handler

import (
	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

// appListOrders —— APP「我的订单」列表。
// 一集一单：每条订单对应一集购买。支持 status=all/pending/paid 过滤 + 分页，
// 顶部三态计数（全部 / 待支付 / 已支付）始终返回全量统计，不随当前过滤变化。
func (s *Server) appListOrders(c *gin.Context) {
	uid := middleware.CurrentID(c)
	page, pageSize := paginate(c)

	q := s.db.Model(&model.Order{}).Where("user_id = ?", uid)
	switch c.Query("status") {
	case "", "all":
		q = q.Where("status IN ?", orderVisibleStatuses)
	case model.OrderStatusPending:
		q = q.Where("status = ?", model.OrderStatusPending)
	case model.OrderStatusPaid:
		q = q.Where("status IN ?", orderPaidStatuses)
	default:
		response.InvalidParam(c, "status 只能是 all/pending/paid")
		return
	}

	var total int64
	q.Count(&total)

	var orders []model.Order
	if err := q.Order("created_at desc").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Find(&orders).Error; err != nil {
		response.ServerError(c, "查询订单失败")
		return
	}

	// 批量补齐：剧（标题/封面/共集数）、集（集号/标题）、该剧累计已购集数（已购 X / 共 Y）。
	dramaIDs := make([]uint64, 0, len(orders))
	episodeIDs := make([]uint64, 0, len(orders))
	seen := map[uint64]bool{}
	for _, o := range orders {
		if !seen[o.DramaID] {
			seen[o.DramaID] = true
			dramaIDs = append(dramaIDs, o.DramaID)
		}
		episodeIDs = append(episodeIDs, o.EpisodeID)
	}
	dramas := s.loadDramaBrief(dramaIDs)
	episodes := s.loadEpisodeBrief(episodeIDs)
	purchased := s.purchasedEpisodeCounts(uid, dramaIDs)

	list := make([]gin.H, 0, len(orders))
	for _, o := range orders {
		list = append(list, gin.H{
			"order_no":            o.OrderNo,
			"status":              o.Status,
			"amount_cents":        o.AmountCents,
			"refund_amount_cents": o.RefundAmountCents,
			"created_at":          o.CreatedAt, // 下单时间
			"paid_at":             o.PaidAt,    // 支付时间（未支付为 null）
			"drama":               dramas[o.DramaID],
			"episode":             episodes[o.EpisodeID],
			"purchased_episodes":  purchased[o.DramaID], // 该剧累计已购集数（已购 X）
		})
	}

	resp := pageResp(list, page, pageSize, total)
	resp["counts"] = s.orderStatusCounts(uid)
	response.OK(c, resp)
}

// 「我的订单」只展示有意义的订单：待支付 + 已支付（退款本质是付过，归入已支付）。
// closed（过期未付）/ failed（支付失败）属废单，不展示也不计入，保证 全部 = 待支付 + 已支付。
var orderPaidStatuses = []string{model.OrderStatusPaid, model.OrderStatusRefunded, model.OrderStatusPartialRefunded}
var orderVisibleStatuses = append([]string{model.OrderStatusPending}, orderPaidStatuses...)

// orderStatusCounts 返回三态计数：pending（待支付）、paid（已支付，含退款）、all=两者之和。
func (s *Server) orderStatusCounts(uid uint64) gin.H {
	type row struct {
		Status string
		Cnt    int64
	}
	var rows []row
	s.db.Model(&model.Order{}).
		Select("status, count(*) as cnt").
		Where("user_id = ?", uid).
		Group("status").Scan(&rows)
	counts := make(map[string]int64, len(rows))
	for _, r := range rows {
		counts[r.Status] = r.Cnt
	}
	pending := counts[model.OrderStatusPending]
	paid := counts[model.OrderStatusPaid] + counts[model.OrderStatusRefunded] + counts[model.OrderStatusPartialRefunded]
	return gin.H{"all": pending + paid, "pending": pending, "paid": paid}
}

func (s *Server) loadDramaBrief(ids []uint64) map[uint64]gin.H {
	out := make(map[uint64]gin.H, len(ids))
	if len(ids) == 0 {
		return out
	}
	var ds []model.Drama
	s.db.Select("id", "title", "cover_url", "total_episodes").Where("id IN ?", ids).Find(&ds)
	for _, d := range ds {
		out[d.ID] = gin.H{
			"id":             d.ID,
			"title":          d.Title,
			"cover_url":      d.CoverURL,
			"total_episodes": d.TotalEpisodes,
		}
	}
	return out
}

func (s *Server) loadEpisodeBrief(ids []uint64) map[uint64]gin.H {
	out := make(map[uint64]gin.H, len(ids))
	if len(ids) == 0 {
		return out
	}
	var eps []model.Episode
	s.db.Select("id", "episode_no", "title").Where("id IN ?", ids).Find(&eps)
	for _, e := range eps {
		out[e.ID] = gin.H{
			"id":         e.ID,
			"episode_no": e.EpisodeNo,
			"title":      e.Title,
		}
	}
	return out
}

// purchasedEpisodeCounts 统计用户在指定各剧下已解锁（已购）的集数。
func (s *Server) purchasedEpisodeCounts(uid uint64, dramaIDs []uint64) map[uint64]int64 {
	out := make(map[uint64]int64, len(dramaIDs))
	if len(dramaIDs) == 0 {
		return out
	}
	type row struct {
		DramaID uint64
		Cnt     int64
	}
	var rows []row
	s.db.Model(&model.EpisodeUnlock{}).
		Select("drama_id, count(*) as cnt").
		Where("user_id = ? AND drama_id IN ?", uid, dramaIDs).
		Group("drama_id").Scan(&rows)
	for _, r := range rows {
		out[r.DramaID] = r.Cnt
	}
	return out
}
