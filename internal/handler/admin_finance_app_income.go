package handler

import (
	"strings"
	"time"

	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// adminListAppIncome —— GET /v1/admin/finance/app-income（财务角色）
// 「支持查看剧集的App付费收入」+「订单中心，展示每部剧的收益情况」（2026-06-18 会议）。
//
// 口径：直接读 orders 表里**曾经支付成功**（paid_at 非空，含已退款）的订单，按短剧聚合：
//   - gross_cents  用户实付总额（SUM amount_cents）
//   - refund_cents 已退款总额（SUM refund_amount_cents）
//   - net_cents    平台净收入 = gross - refund
//
// 注意：这是平台侧 App 付费**毛收入**，与 creator_stats_daily（创作者分成实得）口径不同，财务汇总打款看这里。
// 查询参数：start_date / end_date（按 paid_at 过滤，YYYY-MM-DD，闭区间）、drama_id、payment_method、分页。
func (s *Server) adminListAppIncome(c *gin.Context) {
	page, pageSize := paginate(c)

	startDate := strings.TrimSpace(c.Query("start_date"))
	endDate := strings.TrimSpace(c.Query("end_date"))
	var startAt, endExclusive time.Time
	if startDate != "" {
		t, err := time.ParseInLocation("2006-01-02", startDate, time.Local)
		if err != nil {
			response.InvalidParam(c, "start_date 格式应为 YYYY-MM-DD")
			return
		}
		startAt = t
	}
	if endDate != "" {
		t, err := time.ParseInLocation("2006-01-02", endDate, time.Local)
		if err != nil {
			response.InvalidParam(c, "end_date 格式应为 YYYY-MM-DD")
			return
		}
		endExclusive = t.AddDate(0, 0, 1) // 闭区间：含当天
	}
	dramaID := parseUint(c.Query("drama_id"))
	paymentMethod := strings.TrimSpace(c.Query("payment_method"))

	// 每次聚合都从 buildQuery() 拿一条全新的查询，避免 Group / Order 等子句在两次聚合间互相污染。
	buildQuery := func() *gorm.DB {
		q := s.db.Model(&model.Order{}).Where("paid_at IS NOT NULL")
		if startDate != "" {
			q = q.Where("paid_at >= ?", startAt)
		}
		if endDate != "" {
			q = q.Where("paid_at < ?", endExclusive)
		}
		if dramaID > 0 {
			q = q.Where("drama_id = ?", dramaID)
		}
		if paymentMethod != "" {
			q = q.Where("payment_method = ?", paymentMethod)
		}
		return q
	}

	// 总计（整个筛选范围，不分页）：供看板/汇总展示。
	var summary struct {
		DramaCount  int64
		OrderCount  int64
		GrossCents  int64
		RefundCents int64
	}
	if err := buildQuery().
		Select("COUNT(DISTINCT drama_id) as drama_count, COUNT(*) as order_count, COALESCE(SUM(amount_cents),0) as gross_cents, COALESCE(SUM(refund_amount_cents),0) as refund_cents").
		Scan(&summary).Error; err != nil {
		response.ServerError(c, "统计 App 付费收入失败")
		return
	}

	// 分组明细：按短剧聚合，净收入降序。
	type aggRow struct {
		DramaID     uint64
		OrderCount  int64
		GrossCents  int64
		RefundCents int64
	}
	var rows []aggRow
	if err := buildQuery().
		Select("drama_id, COUNT(*) as order_count, COALESCE(SUM(amount_cents),0) as gross_cents, COALESCE(SUM(refund_amount_cents),0) as refund_cents").
		Group("drama_id").
		// ORDER BY 里 Postgres 不认 SELECT 别名做表达式，需重复聚合式；按净收入降序。
		Order("(COALESCE(SUM(amount_cents),0) - COALESCE(SUM(refund_amount_cents),0)) DESC, drama_id DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Scan(&rows).Error; err != nil {
		response.ServerError(c, "查询 App 付费收入失败")
		return
	}

	// 标题 / 创作者名按需补齐。
	dramaIDs := make([]uint64, 0, len(rows))
	for _, r := range rows {
		dramaIDs = append(dramaIDs, r.DramaID)
	}
	titles, creatorIDs := s.dramaTitleCreatorMap(dramaIDs)
	creatorNames := s.creatorNameMap(creatorIDs)

	list := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		cid := creatorIDs[r.DramaID]
		list = append(list, gin.H{
			"drama_id":     r.DramaID,
			"drama_title":  titles[r.DramaID],
			"creator_id":   cid,
			"creator_name": creatorNames[cid],
			"order_count":  r.OrderCount,
			"gross_cents":  r.GrossCents,
			"refund_cents": r.RefundCents,
			"net_cents":    r.GrossCents - r.RefundCents,
		})
	}

	data := pageResp(list, page, pageSize, summary.DramaCount)
	data["summary"] = gin.H{
		"drama_count":  summary.DramaCount,
		"order_count":  summary.OrderCount,
		"gross_cents":  summary.GrossCents,
		"refund_cents": summary.RefundCents,
		"net_cents":    summary.GrossCents - summary.RefundCents,
	}
	response.OK(c, data)
}

// dramaTitleCreatorMap 批量取短剧标题与 creator_id，返回 (drama_id->title, drama_id->creator_id)。
func (s *Server) dramaTitleCreatorMap(dramaIDs []uint64) (map[uint64]string, map[uint64]uint64) {
	titles := map[uint64]string{}
	creators := map[uint64]uint64{}
	if len(dramaIDs) == 0 {
		return titles, creators
	}
	var rows []struct {
		ID        uint64
		Title     string
		CreatorID *uint64
	}
	s.db.Table("dramas").Select("id, title, creator_id").Where("id IN ?", dramaIDs).Scan(&rows)
	for _, r := range rows {
		titles[r.ID] = r.Title
		if r.CreatorID != nil {
			creators[r.ID] = *r.CreatorID
		}
	}
	return titles, creators
}

// creatorNameMap 批量取创作者展示名，返回 creator_id->name（含去重）。
func (s *Server) creatorNameMap(creatorIDByDrama map[uint64]uint64) map[uint64]string {
	names := map[uint64]string{}
	ids := make([]uint64, 0, len(creatorIDByDrama))
	seen := map[uint64]bool{}
	for _, cid := range creatorIDByDrama {
		if cid == 0 || seen[cid] {
			continue
		}
		seen[cid] = true
		ids = append(ids, cid)
	}
	if len(ids) == 0 {
		return names
	}
	var creators []model.Creator
	s.db.Where("id IN ?", ids).Find(&creators)
	for _, cr := range creators {
		names[cr.ID] = creatorDisplayName(cr)
	}
	return names
}
