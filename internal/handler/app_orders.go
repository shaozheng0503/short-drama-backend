package handler

import (
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

// orderStatusFilterList 把 status 查询参数（all/pending/paid）解析成要纳入的订单状态列表。
// 非法值写 400 并返回 ok=false。flat 列表与分组列表共用。
func orderStatusFilterList(c *gin.Context) ([]string, bool) {
	switch c.Query("status") {
	case "", "all":
		return orderVisibleStatuses, true
	case model.OrderStatusPending:
		return []string{model.OrderStatusPending}, true
	case model.OrderStatusPaid:
		return orderPaidStatuses, true
	default:
		response.InvalidParam(c, "status 只能是 all/pending/paid")
		return nil, false
	}
}

// appListOrders —— APP「我的订单」列表。
// 一集一单：每条订单对应一集购买。支持 status=all/pending/paid 过滤 + 分页，
// 顶部三态计数（全部 / 待支付 / 已支付）始终返回全量统计，不随当前过滤变化。
func (s *Server) appListOrders(c *gin.Context) {
	uid := middleware.CurrentID(c)
	page, pageSize := paginate(c)

	statuses, ok := orderStatusFilterList(c)
	if !ok {
		return
	}
	q := s.db.Model(&model.Order{}).Where("user_id = ? AND status IN ?", uid, statuses)
	// drama_id 过滤：分组视图点开某剧时拉该剧明细用。
	if v := c.Query("drama_id"); v != "" {
		if id := parseUint(v); id > 0 {
			q = q.Where("drama_id = ?", id)
		}
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
		// 选集购买：批量单 episode_count=N、episode 为 null；单集单 episode_count=1、episode 带集号。
		episodeCount := 1
		var episodeView gin.H
		if len(o.EpisodeIDs) > 0 {
			episodeCount = len(o.EpisodeIDs)
		} else {
			episodeView = episodes[o.EpisodeID]
		}
		list = append(list, gin.H{
			"order_no":            o.OrderNo,
			"status":              o.Status,
			"amount_cents":        o.AmountCents,
			"refund_amount_cents": o.RefundAmountCents,
			"created_at":          o.CreatedAt, // 下单时间
			"paid_at":             o.PaidAt,    // 支付时间（未支付为 null）
			"drama":               dramas[o.DramaID],
			"episode":             episodeView,  // 批量单为 null
			"episode_count":       episodeCount, // 本单集数（单集=1，批量=N）
			"purchased_episodes":  purchased[o.DramaID], // 该剧累计已购集数（已购 X）
		})
	}

	resp := pageResp(list, page, pageSize, total)
	resp["counts"] = s.orderStatusCounts(uid)
	response.OK(c, resp)
}

// appListOrdersGrouped —— 「我的订单」按短剧折叠视图。
// 同一短剧的订单聚合成一组：折叠态显示短剧 + 总额(total_amount_cents) + 已购集数 + 笔数；
// 组内 orders[] 是展开态明细（每单买了哪些集）。按短剧分页（page/page_size = 组数），
// 组按该剧最近下单时间倒序。status=all/pending/paid 过滤；三态计数仍是订单级全量。
func (s *Server) appListOrdersGrouped(c *gin.Context) {
	uid := middleware.CurrentID(c)
	page, pageSize := paginate(c)
	statuses, ok := orderStatusFilterList(c)
	if !ok {
		return
	}

	// 总组数（distinct drama）用于分页
	var totalGroups int64
	s.db.Model(&model.Order{}).
		Where("user_id = ? AND status IN ?", uid, statuses).
		Distinct("drama_id").Count(&totalGroups)

	// 本页短剧组 + 聚合（总额/笔数/最近下单时间）
	type groupAgg struct {
		DramaID    uint64
		TotalCents int64
		OrderCnt   int64
		LastAt     time.Time
	}
	var aggs []groupAgg
	s.db.Model(&model.Order{}).
		Select("drama_id, COALESCE(SUM(amount_cents),0) as total_cents, COUNT(*) as order_cnt, MAX(created_at) as last_at").
		Where("user_id = ? AND status IN ?", uid, statuses).
		Group("drama_id").Order("last_at desc").
		Offset((page - 1) * pageSize).Limit(pageSize).Scan(&aggs)

	if len(aggs) == 0 {
		resp := pageResp([]gin.H{}, page, pageSize, totalGroups)
		resp["counts"] = s.orderStatusCounts(uid)
		response.OK(c, resp)
		return
	}

	dramaIDs := make([]uint64, 0, len(aggs))
	for _, a := range aggs {
		dramaIDs = append(dramaIDs, a.DramaID)
	}
	dramas := s.loadDramaBrief(dramaIDs)
	purchased := s.purchasedEpisodeCounts(uid, dramaIDs)

	// 本页各短剧的订单明细（展开用）
	var orders []model.Order
	s.db.Where("user_id = ? AND status IN ? AND drama_id IN ?", uid, statuses, dramaIDs).
		Order("created_at desc").Find(&orders)

	episodeIDs := make([]uint64, 0, len(orders))
	for _, o := range orders {
		if len(o.EpisodeIDs) > 0 {
			episodeIDs = append(episodeIDs, o.EpisodeIDs...)
		} else {
			episodeIDs = append(episodeIDs, o.EpisodeID)
		}
	}
	episodes := s.loadEpisodeBrief(episodeIDs)

	ordersByDrama := map[uint64][]gin.H{}
	pendingByDrama := map[uint64]bool{}
	for _, o := range orders {
		ids := o.EpisodeIDs
		if len(ids) == 0 {
			ids = []uint64{o.EpisodeID}
		}
		eps := make([]gin.H, 0, len(ids))
		for _, id := range ids {
			if ev := episodes[id]; ev != nil {
				eps = append(eps, ev)
			}
		}
		if o.Status == model.OrderStatusPending {
			pendingByDrama[o.DramaID] = true
		}
		ordersByDrama[o.DramaID] = append(ordersByDrama[o.DramaID], gin.H{
			"order_no":            o.OrderNo,
			"status":              o.Status,
			"amount_cents":        o.AmountCents,
			"refund_amount_cents": o.RefundAmountCents,
			"created_at":          o.CreatedAt,
			"paid_at":             o.PaidAt,
			"episode_count":       len(ids),
			"episodes":            eps, // 该单购买的集（单集=1条，批量=N条）
		})
	}

	groups := make([]gin.H, 0, len(aggs))
	for _, a := range aggs {
		ods := ordersByDrama[a.DramaID]
		if ods == nil {
			ods = []gin.H{}
		}
		groups = append(groups, gin.H{
			"drama":              dramas[a.DramaID],
			"total_amount_cents": a.TotalCents, // 折叠后显示的总额
			"order_count":        a.OrderCnt,
			"purchased_episodes": purchased[a.DramaID], // 该剧累计已购集数
			"has_pending":        pendingByDrama[a.DramaID],
			"last_order_at":      a.LastAt,
			"orders":             ods, // 展开态：该剧各订单及所买集明细
		})
	}

	resp := pageResp(groups, page, pageSize, totalGroups)
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
