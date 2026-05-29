package handler

import (
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm/clause"
)

type playHistoryRequest struct {
	DramaID         uint64 `json:"drama_id" binding:"required"`
	EpisodeID       uint64 `json:"episode_id" binding:"required"`
	ProgressSeconds int    `json:"progress_seconds"`
}

func (s *Server) appUpsertPlayHistory(c *gin.Context) {
	uid := middleware.CurrentID(c)
	var req playHistoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "drama_id 与 episode_id 必填")
		return
	}
	if req.ProgressSeconds < 0 {
		response.InvalidParam(c, "progress_seconds 不能小于 0")
		return
	}

	var ep model.Episode
	if err := s.db.First(&ep, req.EpisodeID).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "剧集不存在")
			return
		}
		response.ServerError(c, "查询剧集失败")
		return
	}
	if ep.DramaID != req.DramaID {
		response.InvalidParam(c, "drama_id 与 episode_id 不匹配")
		return
	}
	if ep.Status != model.EpisodeStatusReady {
		response.InvalidParam(c, "剧集尚未就绪，不能记录观看历史")
		return
	}
	var drama model.Drama
	if err := s.db.First(&drama, req.DramaID).Error; err != nil {
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
	if ep.EpisodeNo > drama.FreeEpisodes {
		var unlock model.EpisodeUnlock
		if err := s.db.Where("user_id = ? AND episode_id = ?", uid, ep.ID).First(&unlock).Error; err != nil {
			if isNotFound(err) {
				response.Forbidden(c, "未解锁剧集不能记录观看历史")
				return
			}
			response.ServerError(c, "查询解锁状态失败")
			return
		}
	}

	now := time.Now()
	history := model.PlayHistory{
		UserID:          uid,
		DramaID:         req.DramaID,
		EpisodeID:       req.EpisodeID,
		ProgressSeconds: req.ProgressSeconds,
		UpdatedAt:       now,
		CreatedAt:       now,
	}
	// 一剧一条：按 (user_id, drama_id) 冲突即覆盖，episode_id 更新为最近观看的那一集。
	if err := s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "drama_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"episode_id":       req.EpisodeID,
			"progress_seconds": req.ProgressSeconds,
			"updated_at":       now,
		}),
	}).Create(&history).Error; err != nil {
		response.ServerError(c, "保存观看历史失败")
		return
	}
	response.OK(c, gin.H{"saved": true})
}

func (s *Server) appListPlayHistory(c *gin.Context) {
	uid := middleware.CurrentID(c)
	page, pageSize := paginate(c)

	q := s.db.Model(&model.PlayHistory{}).Where("user_id = ?", uid)
	var total int64
	q.Count(&total)

	var histories []model.PlayHistory
	if err := q.Order("updated_at desc").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&histories).Error; err != nil {
		response.ServerError(c, "查询观看历史失败")
		return
	}

	dramaIDs := make([]uint64, 0, len(histories))
	epIDs := make([]uint64, 0, len(histories))
	for _, h := range histories {
		dramaIDs = append(dramaIDs, h.DramaID)
		epIDs = append(epIDs, h.EpisodeID)
	}

	dramaMap := map[uint64]model.Drama{}
	if len(dramaIDs) > 0 {
		var ds []model.Drama
		s.db.Where("id IN ?", dramaIDs).Find(&ds)
		for _, d := range ds {
			dramaMap[d.ID] = d
		}
	}
	epMap := map[uint64]model.Episode{}
	if len(epIDs) > 0 {
		var es []model.Episode
		s.db.Where("id IN ?", epIDs).Find(&es)
		for _, e := range es {
			epMap[e.ID] = e
		}
	}

	list := make([]gin.H, 0, len(histories))
	for _, h := range histories {
		d := dramaMap[h.DramaID]
		ep := epMap[h.EpisodeID]
		list = append(list, gin.H{
			"drama_id":         h.DramaID,
			"drama_title":      d.Title,
			"cover_url":        d.CoverURL,
			"episode_id":       h.EpisodeID,
			"episode_no":       ep.EpisodeNo,
			"progress_seconds": h.ProgressSeconds,
			"updated_at":       h.UpdatedAt,
		})
	}

	response.OK(c, pageResp(list, page, pageSize, total))
}
