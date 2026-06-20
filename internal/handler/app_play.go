package handler

import (
	"log"

	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// appPlayEpisode 播放地址：
//   - 默认（VOD_PLAY_SIGN_ENABLED=false）：直返 episodes.video_url 兜底，联调期常态。
//   - 开通腾讯 VOD「Key 防盗链」后置 VOD_PLAY_SIGN_ENABLED=true：把 video_url 走 SignPlayURL 拼一次性 token，URL 泄露也无法播。
//
// 「不登录刷短剧」（2026-06-18 会议）：本接口允许匿名访问——免费集任何人都能直接拿到播放地址；
// 付费集对未登录用户返回 need_login（提示登录后购买），对已登录未解锁用户返回 need_unlock。
func (s *Server) appPlayEpisode(c *gin.Context) {
	uid := optionalAppUserID(c, s)
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

	if ep.EpisodeNo > s.effectiveFreeEpisodes(drama) {
		// 付费集：未登录用户先引导登录（无法定位 user 的解锁记录），登录后再校验是否已解锁。
		if uid == 0 {
			response.FailWithData(c, response.CodeEpisodeLocked, "登录后才能解锁付费剧集", gin.H{
				"need_login":  true,
				"need_unlock": true,
				"price_cents": drama.PriceCents,
			})
			return
		}
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
