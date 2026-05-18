package handler

import (
	"net/http"
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm/clause"
)

func (s *Server) listDramas(c *gin.Context) {
	page, pageSize := paginate(c)
	q := s.db.Model(&model.Drama{}).Where("status = ?", "published")
	if category := c.Query("category"); category != "" {
		q = q.Where("category = ?", category)
	}
	if region := c.Query("region"); region != "" {
		q = q.Where("region = ?", region)
	}
	var total int64
	q.Count(&total)
	var dramas []model.Drama
	q.Order("updated_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&dramas)
	response.OK(c, gin.H{"items": dramas, "total": total, "page": page, "page_size": pageSize})
}

func (s *Server) getDrama(c *gin.Context) {
	var drama model.Drama
	if err := s.db.First(&drama, c.Param("id")).Error; err != nil || drama.Status != "published" {
		response.Error(c, http.StatusNotFound, "drama not found")
		return
	}
	s.db.Model(&drama).UpdateColumn("view_count", gormExpr("view_count + ?", 1))
	response.OK(c, drama)
}

func (s *Server) listEpisodes(c *gin.Context) {
	var episodes []model.Episode
	s.db.Where("drama_id = ?", c.Param("id")).Order("episode_no asc").Find(&episodes)
	response.OK(c, episodes)
}

func (s *Server) search(c *gin.Context) {
	page, pageSize := paginate(c)
	keyword := "%" + c.Query("q") + "%"
	var dramas []model.Drama
	q := s.db.Where("status = ? AND (title ILIKE ? OR description ILIKE ? OR tags ILIKE ?)", "published", keyword, keyword, keyword)
	var total int64
	q.Model(&model.Drama{}).Count(&total)
	q.Offset((page - 1) * pageSize).Limit(pageSize).Order("view_count desc").Find(&dramas)
	response.OK(c, gin.H{"items": dramas, "total": total})
}

func (s *Server) likeDrama(c *gin.Context) {
	s.toggleAction(c, "like", "like_count")
}

func (s *Server) favoriteDrama(c *gin.Context) {
	s.toggleAction(c, "favorite", "fav_count")
}

func (s *Server) shareDrama(c *gin.Context) {
	id := c.Param("id")
	s.db.Model(&model.Drama{}).Where("id = ?", id).UpdateColumn("share_count", gormExpr("share_count + ?", 1))
	response.OK(c, gin.H{"shared": true})
}

func (s *Server) toggleAction(c *gin.Context, action, counter string) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	userID := middleware.UserID(c)
	var drama model.Drama
	if err := s.db.First(&drama, c.Param("id")).Error; err != nil {
		response.Error(c, http.StatusNotFound, "drama not found")
		return
	}

	var existing model.UserDramaAction
	err := s.db.Where("user_id = ? AND drama_id = ? AND action = ?", userID, drama.ID, action).First(&existing).Error
	if req.Enabled && err != nil {
		s.db.Create(&model.UserDramaAction{UserID: userID, DramaID: drama.ID, Action: action})
		s.db.Model(&drama).UpdateColumn(counter, gormExpr(counter+" + ?", 1))
	}
	if !req.Enabled && err == nil {
		s.db.Delete(&existing)
		s.db.Model(&drama).UpdateColumn(counter, gormExpr("GREATEST("+counter+" - ?, 0)", 1))
	}
	response.OK(c, gin.H{"enabled": req.Enabled})
}

func (s *Server) createComment(c *gin.Context) {
	var req struct {
		Content string `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	comment := model.Comment{UserID: middleware.UserID(c), DramaID: parseUintParam(c.Param("id")), Content: req.Content}
	if err := s.db.Create(&comment).Error; err != nil {
		response.Error(c, http.StatusBadRequest, "failed to create comment")
		return
	}
	response.Created(c, comment)
}

func (s *Server) listComments(c *gin.Context) {
	var comments []model.Comment
	s.db.Preload("User").Where("drama_id = ?", c.Param("id")).Order("created_at desc").Find(&comments)
	response.OK(c, comments)
}

func (s *Server) upsertWatchHistory(c *gin.Context) {
	var req struct {
		DramaID     uint `json:"drama_id" binding:"required"`
		EpisodeID   uint `json:"episode_id" binding:"required"`
		ProgressSec int  `json:"progress_sec"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	history := model.WatchHistory{UserID: middleware.UserID(c), DramaID: req.DramaID, EpisodeID: req.EpisodeID, ProgressSec: req.ProgressSec}
	if err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "episode_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"drama_id", "progress_sec", "updated_at"}),
	}).Create(&history).Error; err != nil {
		response.Error(c, http.StatusBadRequest, "failed to save history")
		return
	}
	response.OK(c, history)
}

func (s *Server) listWatchHistory(c *gin.Context) {
	var histories []model.WatchHistory
	s.db.Where("user_id = ?", middleware.UserID(c)).Order("updated_at desc").Find(&histories)
	response.OK(c, histories)
}

func (s *Server) checkIn(c *gin.Context) {
	day := time.Now().Format("2006-01-02")
	checkin := model.CheckIn{UserID: middleware.UserID(c), Day: day, Points: 10}
	if err := s.db.Create(&checkin).Error; err != nil {
		response.Error(c, http.StatusConflict, "already checked in today")
		return
	}
	s.db.Model(&model.User{}).Where("id = ?", checkin.UserID).UpdateColumn("points", gormExpr("points + ?", checkin.Points))
	response.Created(c, checkin)
}

func (s *Server) createOrder(c *gin.Context) {
	var req struct {
		DramaID     *uint  `json:"drama_id"`
		Channel     string `json:"channel" binding:"required"`
		AmountCents int64  `json:"amount_cents" binding:"required,min=1"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	order := model.Order{UserID: middleware.UserID(c), DramaID: req.DramaID, Channel: req.Channel, AmountCents: req.AmountCents, Status: "pending"}
	if err := s.db.Create(&order).Error; err != nil {
		response.Error(c, http.StatusBadRequest, "failed to create order")
		return
	}
	response.Created(c, gin.H{"order": order, "pay_status": "mock_pending"})
}

func (s *Server) markOrderPaid(c *gin.Context) {
	var order model.Order
	if err := s.db.Where("id = ? AND user_id = ?", c.Param("id"), middleware.UserID(c)).First(&order).Error; err != nil {
		response.Error(c, http.StatusNotFound, "order not found")
		return
	}
	order.Status = "paid"
	order.TradeNo = "mock-" + time.Now().Format("20060102150405")
	s.db.Save(&order)
	response.OK(c, order)
}

func (s *Server) listNotifications(c *gin.Context) {
	var items []model.Notification
	s.db.Where("user_id = ?", middleware.UserID(c)).Order("created_at desc").Find(&items)
	response.OK(c, items)
}

func (s *Server) readNotification(c *gin.Context) {
	now := time.Now()
	s.db.Model(&model.Notification{}).Where("id = ? AND user_id = ?", c.Param("id"), middleware.UserID(c)).Update("read_at", &now)
	response.OK(c, gin.H{"read": true})
}
