package handler

import (
	"context"
	"fmt"
	"log"
	"time"

	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const sharePageCountTTL = time.Hour

// appShareDramaPage 给站外 H5 分享页使用：公开返回短剧展示信息 + 第一集视频流。
// 访问会尝试增加分享数，但按 IP+短剧 1 小时去重，防止刷新/爬虫刷分享数。
func (s *Server) appShareDramaPage(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}

	var drama model.Drama
	if err := s.db.First(&drama, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "短剧不存在")
			return
		}
		response.ServerError(c, "查询短剧失败")
		return
	}
	if drama.Status != model.DramaStatusPublished {
		response.NotFound(c, "短剧未上架")
		return
	}

	sharedCounted := s.allowSharePageCount(c.ClientIP(), id)
	if sharedCounted {
		if err := s.db.Model(&model.Drama{}).Where("id = ?", id).
			UpdateColumn("share_count", gorm.Expr("share_count + ?", 1)).Error; err != nil {
			// 分享计数不是主链路，失败只记日志，继续返回页面数据。
			log.Printf("[share-page] increment share_count failed drama=%d ip=%s err=%v", id, c.ClientIP(), err)
			sharedCounted = false
		} else {
			drama.ShareCount++
		}
	}

	var first model.Episode
	firstEpisode := gin.H(nil)
	if err := s.db.Where("drama_id = ? AND status = ?", id, model.EpisodeStatusReady).
		Order("episode_no asc").
		First(&first).Error; err == nil {
		playURL := first.VideoURL
		expireSeconds := 3600
		if s.vod.PlaySignConfigured() && playURL != "" {
			if signed, err := s.vod.SignPlayURL(playURL); err == nil {
				playURL = signed
				expireSeconds = int(s.cfg.VODPlaySignExpire.Seconds())
			} else {
				log.Printf("[share-page] sign first episode url failed drama=%d ep=%d err=%v", id, first.ID, err)
			}
		}
		firstEpisode = gin.H{
			"id":                  first.ID,
			"episode_no":          first.EpisodeNo,
			"title":               first.Title,
			"play_url":            playURL,
			"play_url_expires_in": expireSeconds,
			"duration_seconds":    first.DurationSeconds,
			"like_count":          first.LikeCount,
			"comment_count":       s.commentCountByEpisodeIDs([]uint64{first.ID})[first.ID],
		}
	} else if err != nil && !isNotFound(err) {
		response.ServerError(c, "查询首集失败")
		return
	}

	response.OK(c, gin.H{
		"id":             drama.ID,
		"title":          drama.Title,
		"description":    drama.Description,
		"cover_url":      drama.CoverURL,
		"like_count":     drama.LikeCount,
		"comment_count":  s.commentCountByDramaID(drama.ID),
		"favorite_count": drama.FavoriteCount,
		"share_count":    drama.ShareCount,
		"shared_counted": sharedCounted,
		"first_episode":  firstEpisode,
	})
}

func (s *Server) commentCountByDramaID(dramaID uint64) int64 {
	var count int64
	s.db.Model(&model.Comment{}).Where("drama_id = ?", dramaID).Count(&count)
	return count
}

func (s *Server) allowSharePageCount(ip string, dramaID uint64) bool {
	if ip == "" {
		ip = "unknown"
	}
	key := fmt.Sprintf("share_page:%d:%s", dramaID, ip)
	if s.redis != nil {
		ok, err := s.redis.SetNX(context.Background(), key, "1", sharePageCountTTL).Result()
		if err == nil {
			return ok
		}
		if err != redis.Nil {
			log.Printf("[share-page] redis SetNX failed key=%s err=%v; fallback memory limiter", key, err)
		}
	}
	return s.allowSharePageCountMemory(key, time.Now())
}

func (s *Server) allowSharePageCountMemory(key string, now time.Time) bool {
	s.shareMu.Lock()
	defer s.shareMu.Unlock()

	for k, exp := range s.shareSeen {
		if now.After(exp) {
			delete(s.shareSeen, k)
		}
	}
	if exp, ok := s.shareSeen[key]; ok && now.Before(exp) {
		return false
	}
	s.shareSeen[key] = now.Add(sharePageCountTTL)
	return true
}
