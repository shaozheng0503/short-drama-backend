// publish-scheduled —— 定时发布轮询。
//
// 用法（建议 crontab 每日 0 点跑一次）：
//
//	0 0 * * *  /path/to/publish-scheduled
//
// 逻辑：把「已审核通过 + 已配置计划发布时间且 <= 当前 + 处于待发布状态 + 至少 1 集 ready」
// 的短剧自动置为 published，并写入实际发布时间 published_at。
// 跨度按「当前时间」判断（scheduled_publish_at <= now），所以每天 0 点跑一次即可覆盖当天到点的剧；
// 后续若要精确到分钟，把 cron 频率调高即可，逻辑不变。
package main

import (
	"fmt"
	"log"
	"time"

	"ai-drama-platform/internal/alert"
	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/model"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func main() {
	cfg := config.Load()
	db, err := gorm.Open(postgres.Open(cfg.DSN), &gorm.Config{})
	if err != nil {
		log.Fatalf("connect database failed: %v", err)
	}

	now := time.Now()
	published, ids, err := publishDue(db, now)
	if err != nil {
		alert.New(cfg).SendAsync(alert.Event{
			Level:   "error",
			Type:    "publish_scheduled_failed",
			Message: "定时发布失败",
			Fields:  map[string]interface{}{"error": err.Error()},
		})
		log.Fatalf("publish scheduled failed: %v", err)
	}
	if published > 0 {
		alert.New(cfg).SendAsync(alert.Event{
			Level:   "info",
			Type:    "scheduled_dramas_published",
			Message: "定时发布已执行",
			Fields:  map[string]interface{}{"published_count": published, "drama_ids": ids},
		})
		time.Sleep(200 * time.Millisecond)
	}
	fmt.Printf("published_scheduled: %d\n", published)
	if len(ids) > 0 {
		fmt.Printf("drama_ids: %v\n", ids)
	}
}

// publishDue 发布所有到点的计划发布短剧，返回发布数量与剧 ID 列表。
func publishDue(db *gorm.DB, now time.Time) (int, []uint64, error) {
	var due []model.Drama
	if err := db.
		Where("audit_status = ?", model.DramaAuditApproved).
		Where("status = ?", model.DramaStatusAwaitingPublish).
		Where("scheduled_publish_at IS NOT NULL AND scheduled_publish_at <= ?", now).
		Find(&due).Error; err != nil {
		return 0, nil, err
	}

	published := make([]uint64, 0, len(due))
	for _, d := range due {
		// 守卫：与创作者手动上架一致，至少 1 集 ready 才能发布。
		var readyCount int64
		if err := db.Model(&model.Episode{}).
			Where("drama_id = ? AND status = ?", d.ID, model.EpisodeStatusReady).
			Count(&readyCount).Error; err != nil {
			return len(published), published, err
		}
		if readyCount == 0 {
			log.Printf("[publish-scheduled] skip drama=%d：无 ready 剧集", d.ID)
			continue
		}
		if err := db.Model(&model.Drama{}).Where("id = ? AND status = ?", d.ID, model.DramaStatusAwaitingPublish).
			Updates(map[string]interface{}{
				"status":       model.DramaStatusPublished,
				"published_at": now,
			}).Error; err != nil {
			return len(published), published, err
		}
		published = append(published, d.ID)
		log.Printf("[publish-scheduled] published drama=%d scheduled=%v", d.ID, d.ScheduledPublishAt)
	}
	return len(published), published, nil
}
