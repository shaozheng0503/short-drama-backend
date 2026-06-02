package handler

import (
	"log"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// appPlayEpisode 播放地址：
//   - 默认（VOD_PLAY_SIGN_ENABLED=false）：直返 episodes.video_url 兜底，联调期常态。
//   - 开通腾讯 VOD「Key 防盗链」后置 VOD_PLAY_SIGN_ENABLED=true：把 video_url 走 SignPlayURL 拼一次性 token，URL 泄露也无法播。
func (s *Server) appPlayEpisode(c *gin.Context) {
	uid := middleware.CurrentID(c)
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}

	var ep model.Episode
	if err := s.db.First(&ep, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "剧集不存在")
			return
		}
		response.ServerError(c, "查询剧集失败")
		return
	}
	if ep.Status != model.EpisodeStatusReady {
		response.NotFound(c, "剧集尚未就绪")
		return
	}

	var drama model.Drama
	if err := s.db.First(&drama, ep.DramaID).Error; err != nil {
		response.ServerError(c, "查询短剧失败")
		return
	}
	if drama.Status != model.DramaStatusPublished {
		response.NotFound(c, "短剧未上架")
		return
	}

	if ep.EpisodeNo > drama.FreeEpisodes {
		var unlock model.EpisodeUnlock
		err := s.db.Where("user_id = ? AND episode_id = ?", uid, ep.ID).First(&unlock).Error
		if err != nil {
			if !isNotFound(err) {
				response.ServerError(c, "查询解锁失败")
				return
			}
			response.FailWithData(c, response.CodeEpisodeLocked, "剧集未解锁", gin.H{
				"need_unlock": true,
				"price_cents": drama.PriceCents,
			})
			return
		}
	}

	// 异步增加播放量（同事务里不阻塞返回；MVP 直接同步 +1）。
	s.db.Model(&model.Drama{}).Where("id = ?", drama.ID).
		UpdateColumn("play_count", gorm.Expr("play_count + ?", 1))

	var nextID *uint64
	var next model.Episode
	if err := s.db.Where("drama_id = ? AND episode_no = ? AND status = ?", drama.ID, ep.EpisodeNo+1, model.EpisodeStatusReady).
		First(&next).Error; err == nil {
		nid := next.ID
		nextID = &nid
	}

	playURL := ep.VideoURL
	expireSeconds := 3600
	if s.vod.PlaySignConfigured() && playURL != "" {
		signed, err := s.vod.SignPlayURL(playURL)
		if err != nil {
			// 签名失败不暴露给用户，退回裸链以保证可播；同时打 error 日志方便排查。
			log.Printf("[play] sign vod url err=%v ep_id=%d", err, ep.ID)
		} else {
			playURL = signed
			expireSeconds = int(s.cfg.VODPlaySignExpire.Seconds())
		}
	}

	var likedCnt int64
	s.db.Model(&model.UserAction{}).
		Where("user_id = ? AND episode_id = ? AND action = ?", uid, ep.ID, model.ActionLike).
		Count(&likedCnt)

	response.OK(c, gin.H{
		"episode": gin.H{
			"id":         ep.ID,
			"drama_id":   ep.DramaID,
			"episode_no": ep.EpisodeNo,
			"title":      ep.Title,
			"like_count": ep.LikeCount,
			"liked":      likedCnt > 0,
		},
		"play_url":        playURL,
		"expire_seconds":  expireSeconds,
		"next_episode_id": nextID,
	})
}
