package handler

import (
	"regexp"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

// 2026-07-06 加 P1-5：时间线按天回看接口（3 个：settlement / invoice / withdrawal）
// 共用同一个时间线查询逻辑——只是 entityType 不同 + 各自做权限校验。
// 入参：
//   - :id   路径参数
//   - ?as_of=YYYY-MM-DD  可选，返截至当天的事件列表 + 当天状态快照
//                       不传时返"当前"——所有事件 + 现状
// 返：
//   {
//     "entity_type": "settlement",
//     "entity_id":   2,
//     "current_status": "open",           // 当前真实状态
//     "snapshot_status": "open",          // as_of 那一刻的状态（items 末尾的 to_status；空时为 ""）
//     "first_event_at": "...",            // 该实体最早 transition 的时间
//     "items": [...]
//   }

var asOfRegex = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func (s *Server) creatorSettlementTimeline(c *gin.Context) {
	creatorID := middleware.CurrentID(c)
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	// 鉴权：必须先确认该 settlement 属于当前创作者
	var creatorIDFromDB uint64
	if err := s.db.Table("settlements").Select("creator_id").Where("id = ?", id).Scan(&creatorIDFromDB).Error; err != nil {
		response.InvalidParam(c, "查询失败")
		return
	}
	if creatorIDFromDB == 0 {
		response.NotFound(c, "结算单不存在")
		return
	}
	if creatorIDFromDB != creatorID {
		response.NotFound(c, "结算单不存在")
		return
	}
	s.doTimeline(c, "settlement", id)
}

func (s *Server) creatorInvoiceTimeline(c *gin.Context) {
	creatorID := middleware.CurrentID(c)
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var creatorIDFromDB uint64
	if err := s.db.Table("invoices").Select("creator_id").Where("id = ?", id).Scan(&creatorIDFromDB).Error; err != nil {
		response.InvalidParam(c, "查询失败")
		return
	}
	if creatorIDFromDB == 0 {
		response.NotFound(c, "发票不存在")
		return
	}
	if creatorIDFromDB != creatorID {
		response.NotFound(c, "发票不存在")
		return
	}
	s.doTimeline(c, "invoice", id)
}

func (s *Server) creatorWithdrawalTimeline(c *gin.Context) {
	creatorID := middleware.CurrentID(c)
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var creatorIDFromDB uint64
	if err := s.db.Table("withdrawals").Select("creator_id").Where("id = ?", id).Scan(&creatorIDFromDB).Error; err != nil {
		response.InvalidParam(c, "查询失败")
		return
	}
	if creatorIDFromDB == 0 {
		response.NotFound(c, "提现申请不存在")
		return
	}
	if creatorIDFromDB != creatorID {
		response.NotFound(c, "提现申请不存在")
		return
	}
	s.doTimeline(c, "withdrawal", id)
}

// doTimeline 通用时间线查询实现
func (s *Server) doTimeline(c *gin.Context, entityType string, entityID uint64) {
	asOf := c.Query("as_of")
	if asOf != "" && !asOfRegex.MatchString(asOf) {
		response.InvalidParam(c, "as_of 必须为 YYYY-MM-DD")
		return
	}
	items, snapshot, firstEventAt, err := s.getTimelineAsOf(entityType, entityID, asOf)
	if err != nil {
		response.ServerError(c, "查询时间线失败")
		return
	}
	// 取"当前"真实状态
	currentStatus := ""
	switch entityType {
	case "settlement":
		var sRow struct {
			Status string
		}
		s.db.Table("settlements").Select("status").Where("id = ?", entityID).Scan(&sRow)
		currentStatus = sRow.Status
	case "invoice":
		var iRow struct {
			Status string
		}
		s.db.Table("invoices").Select("status").Where("id = ?", entityID).Scan(&iRow)
		currentStatus = iRow.Status
	case "withdrawal":
		var wRow struct {
			Status string
		}
		s.db.Table("withdrawals").Select("status").Where("id = ?", entityID).Scan(&wRow)
		currentStatus = wRow.Status
	}
	c.JSON(200, gin.H{
		"code": 0,
		"data": gin.H{
			"entity_type":     entityType,
			"entity_id":       entityID,
			"current_status":  currentStatus,
			"snapshot_status": snapshot,
			"first_event_at":  firstEventAt,
			"as_of":           asOf,
			"items":           items,
		},
	})
}
