package handler

import (
	"ai-drama-platform/internal/model"
	"encoding/json"
	"log"
	"strconv"
)

// 2026-07-06 P1-5：状态变迁写入器 / 回看工具。
//
// 设计原则：
//   - 写：每个状态变化点（cron 算账 / creator 提现 / admin 审核 / admin 打款 / admin 驳回）
//         都调 recordTransition 追加一行
//   - 读：getTimelineAsOf 返"截至 as_of"那一刻的事件列表
//         + 该实体在 as_of 那一刻的"快照状态"（最后一条 to_status）

// recordTransition 写一条状态变迁事件。
// 入参说明：
//   - entityType: "settlement" / "invoice" / "withdrawal"
//   - entityID:   对应表的 id
//   - fromStatus / toStatus: 旧状态 / 新状态
//   - actorType:  "system" / "creator" / "admin"
//   - actorID:    触发方 id（system 时可空）
//   - reason:     备注 / 驳回原因 / 打款流水号
//   - metadata:   任意 map（比如金额、关联 invoice_ids），会序列化成 JSON
//
// 写入失败只记日志不影响主业务（避免因为事件表写挂导致业务回滚）。
func (s *Server) recordTransition(entityType string, entityID uint64, fromStatus, toStatus, actorType string, actorID *uint64, reason string, metadata map[string]interface{}) {
	if entityID == 0 {
		return
	}
	// 2026-07-06 修：metadata 是 *string 指针——nil 写 NULL，map 序列化后传字符串指针。
	// 配合 jsonb 列：null 和 {} 都合法。
	var metaPtr *string
	if metadata != nil {
		if b, err := json.Marshal(metadata); err == nil {
			s := string(b)
			metaPtr = &s
		}
	}
	st := model.StateTransition{
		EntityType: entityType,
		EntityID:   entityID,
		FromStatus: fromStatus,
		ToStatus:   toStatus,
		ActorType:  actorType,
		ActorID:    actorID,
		Reason:     reason,
		Metadata:   metaPtr,
	}
	if err := s.db.Create(&st).Error; err != nil {
		// 不阻断业务
		log.Printf("[timeline] recordTransition err entity=%s id=%d %s->%s: %v",
			entityType, entityID, fromStatus, toStatus, err)
	}
}

// timelineItem 通用回看项
type timelineItem struct {
	ID         uint64                 `json:"id"`
	EntityType string                 `json:"entity_type"`
	EntityID   uint64                 `json:"entity_id"`
	FromStatus string                 `json:"from_status"`
	ToStatus   string                 `json:"to_status"`
	ActorType  string                 `json:"actor_type"`
	ActorID    *uint64                `json:"actor_id,omitempty"`
	Reason     string                 `json:"reason"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt  string                 `json:"created_at"`
}

// getTimelineAsOf 拉一个实体在 as_of 时间点之前的所有变迁，按 created_at ASC 排。
// 返：
//   - items: 变迁列表（按时间正序）
//   - snapshotStatus: as_of 那一刻的状态（items 末尾的 to_status；items 为空时返回 ""）
//   - firstEventAt:   该实体最早一次 transition 的 created_at（前端可提示"只能回看到 xxx 之前"）
//
// 关键设计：as_of=空时，返"当前"——即不传 as_of 时按"现在"算。
//         前端拖动时间轴传入 as_of=YYYY-MM-DD 时返"截至当天 23:59:59"的所有变迁。
func (s *Server) getTimelineAsOf(entityType string, entityID uint64, asOf string) ([]timelineItem, string, string, error) {
	var rows []model.StateTransition
	q := s.db.Where("entity_type = ? AND entity_id = ?", entityType, entityID)
	if asOf != "" {
		// asOf 形如 "2026-07-03"：取 [0:00, 次日 0:00)
		// 也就是当天 23:59:59 前的所有变迁
		q = q.Where("created_at < (to_date(?, 'YYYY-MM-DD') + interval '1 day')", asOf)
	}
	if err := q.Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, "", "", err
	}
	items := make([]timelineItem, 0, len(rows))
	snapshot := ""
	firstAt := ""
	for _, r := range rows {
		var meta map[string]interface{}
		if r.Metadata != nil && *r.Metadata != "" {
			_ = json.Unmarshal([]byte(*r.Metadata), &meta)
		}
		items = append(items, timelineItem{
			ID:         r.ID,
			EntityType: r.EntityType,
			EntityID:   r.EntityID,
			FromStatus: r.FromStatus,
			ToStatus:   r.ToStatus,
			ActorType:  r.ActorType,
			ActorID:    r.ActorID,
			Reason:     r.Reason,
			Metadata:   meta,
			CreatedAt:  r.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
		snapshot = r.ToStatus
		if firstAt == "" {
			firstAt = r.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
	}
	return items, snapshot, firstAt, nil
}

// 工具：从 gin 拿 uint64 id
func parseUint64FromStr(s string) uint64 {
	id, _ := strconv.ParseUint(s, 10, 64)
	return id
}
