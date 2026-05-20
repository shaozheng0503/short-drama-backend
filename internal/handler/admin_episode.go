package handler

import (
	"errors"

	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (s *Server) adminListEpisodes(c *gin.Context) {
	dramaID := dramaIDFromPath(c)
	if dramaID == 0 {
		response.InvalidParam(c, "drama_id 不合法")
		return
	}
	if !s.dramaExists(dramaID) {
		response.NotFound(c, "短剧不存在")
		return
	}
	var episodes []model.Episode
	if err := s.db.Where("drama_id = ?", dramaID).Order("episode_no asc").Find(&episodes).Error; err != nil {
		response.ServerError(c, "查询剧集失败")
		return
	}
	views := make([]gin.H, 0, len(episodes))
	for _, ep := range episodes {
		views = append(views, episodeAdminView(ep))
	}
	response.OK(c, gin.H{"list": views})
}

type episodeCreateRequest struct {
	EpisodeNo       int    `json:"episode_no" binding:"required"`
	Title           string `json:"title"`
	VODFileID       string `json:"vod_file_id"`
	VideoURL        string `json:"video_url"`
	DurationSeconds int    `json:"duration_seconds"`
	Status          string `json:"status"`
}

func (s *Server) adminCreateEpisode(c *gin.Context) {
	dramaID := dramaIDFromPath(c)
	if dramaID == 0 {
		response.InvalidParam(c, "drama_id 不合法")
		return
	}
	if !s.dramaExists(dramaID) {
		response.NotFound(c, "短剧不存在")
		return
	}

	var req episodeCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}
	if req.EpisodeNo < 1 {
		response.InvalidParam(c, "episode_no 必须大于 0")
		return
	}

	status := model.EpisodeStatusUploading
	if req.Status != "" {
		switch req.Status {
		case model.EpisodeStatusUploading, model.EpisodeStatusReady, model.EpisodeStatusFailed:
			status = req.Status
		default:
			response.InvalidParam(c, "status 非法")
			return
		}
	}
	if req.VideoURL != "" && req.Status == "" {
		status = model.EpisodeStatusReady
	}

	ep := model.Episode{
		DramaID:         dramaID,
		EpisodeNo:       req.EpisodeNo,
		Title:           req.Title,
		VODFileID:       req.VODFileID,
		VideoURL:        req.VideoURL,
		DurationSeconds: req.DurationSeconds,
		Status:          status,
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ep).Error; err != nil {
			return err
		}
		return refreshDramaTotalEpisodes(tx, dramaID)
	})
	if err != nil {
		if isUniqueViolation(err) {
			response.Conflict(c, "同一短剧下 episode_no 已存在")
			return
		}
		response.ServerError(c, "创建剧集失败")
		return
	}
	response.OK(c, episodeAdminView(ep))
}

type episodeUpdateRequest struct {
	Title           *string `json:"title"`
	VODFileID       *string `json:"vod_file_id"`
	VideoURL        *string `json:"video_url"`
	DurationSeconds *int    `json:"duration_seconds"`
	Status          *string `json:"status"`
}

func (s *Server) adminUpdateEpisode(c *gin.Context) {
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

	var req episodeUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}

	updates := map[string]interface{}{}
	if req.Title != nil {
		updates["title"] = *req.Title
	}
	if req.VODFileID != nil {
		updates["vod_file_id"] = *req.VODFileID
	}
	if req.VideoURL != nil {
		updates["video_url"] = *req.VideoURL
	}
	if req.DurationSeconds != nil && *req.DurationSeconds >= 0 {
		updates["duration_seconds"] = *req.DurationSeconds
	}
	if req.Status != nil {
		switch *req.Status {
		case model.EpisodeStatusUploading, model.EpisodeStatusReady, model.EpisodeStatusFailed:
			updates["status"] = *req.Status
		case "":
			// ignore
		default:
			response.InvalidParam(c, "status 非法")
			return
		}
	}

	if len(updates) > 0 {
		if err := s.db.Model(&ep).Updates(updates).Error; err != nil {
			response.ServerError(c, "更新剧集失败")
			return
		}
	}
	s.db.First(&ep, id)
	response.OK(c, episodeAdminView(ep))
}

func (s *Server) dramaExists(id uint64) bool {
	var cnt int64
	s.db.Model(&model.Drama{}).Where("id = ?", id).Count(&cnt)
	return cnt > 0
}

func refreshDramaTotalEpisodes(tx *gorm.DB, dramaID uint64) error {
	var cnt int64
	if err := tx.Model(&model.Episode{}).
		Where("drama_id = ?", dramaID).
		Count(&cnt).Error; err != nil {
		return err
	}
	return tx.Model(&model.Drama{}).
		Where("id = ?", dramaID).
		Update("total_episodes", cnt).Error
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// PostgreSQL unique_violation: SQLSTATE 23505
	return errors.Is(err, gorm.ErrDuplicatedKey) ||
		containsAny(msg, "duplicate key", "SQLSTATE 23505", "unique constraint")
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub != "" && indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
