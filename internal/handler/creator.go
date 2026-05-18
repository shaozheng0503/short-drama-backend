package handler

import (
	"net/http"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm/clause"
)

func (s *Server) upsertCreatorProfile(c *gin.Context) {
	var req model.CreatorProfile
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	req.UserID = middleware.UserID(c)
	if req.VerifiedStatus == "" {
		req.VerifiedStatus = "pending"
	}
	if err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"real_name", "id_card_no", "id_card_image_url", "bank_account", "bank_name", "publisher_name", "verified_status", "updated_at"}),
	}).Create(&req).Error; err != nil {
		response.Error(c, http.StatusBadRequest, "failed to save creator profile")
		return
	}
	response.OK(c, req)
}

func (s *Server) creatorDashboard(c *gin.Context) {
	creatorID := middleware.UserID(c)
	var dramaCount int64
	var viewCount int64
	var revenue int64
	s.db.Model(&model.Drama{}).Where("creator_id = ?", creatorID).Count(&dramaCount)
	s.db.Model(&model.Drama{}).Where("creator_id = ?", creatorID).Select("COALESCE(SUM(view_count),0)").Scan(&viewCount)
	s.db.Model(&model.RevenueDaily{}).Where("creator_id = ?", creatorID).Select("COALESCE(SUM(amount_cents),0)").Scan(&revenue)
	response.OK(c, gin.H{"drama_count": dramaCount, "view_count": viewCount, "revenue_cents": revenue})
}

func (s *Server) createDrama(c *gin.Context) {
	var req model.Drama
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	creatorID := middleware.UserID(c)
	req.ID = 0
	req.CreatorID = &creatorID
	if req.Status == "" {
		req.Status = "draft"
	}
	if err := s.db.Create(&req).Error; err != nil {
		response.Error(c, http.StatusBadRequest, "failed to create drama")
		return
	}
	response.Created(c, req)
}

func (s *Server) updateDrama(c *gin.Context) {
	var drama model.Drama
	if err := s.db.Where("id = ? AND creator_id = ?", c.Param("id"), middleware.UserID(c)).First(&drama).Error; err != nil {
		response.Error(c, http.StatusNotFound, "drama not found")
		return
	}
	var req model.Drama
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	req.ID = drama.ID
	req.CreatorID = drama.CreatorID
	s.db.Model(&drama).Updates(req)
	response.OK(c, drama)
}

func (s *Server) createEpisode(c *gin.Context) {
	var drama model.Drama
	if err := s.db.Where("id = ? AND creator_id = ?", c.Param("id"), middleware.UserID(c)).First(&drama).Error; err != nil {
		response.Error(c, http.StatusNotFound, "drama not found")
		return
	}
	var req model.Episode
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	req.ID = 0
	req.DramaID = drama.ID
	if err := s.db.Create(&req).Error; err != nil {
		response.Error(c, http.StatusBadRequest, "failed to create episode")
		return
	}
	response.Created(c, req)
}

func (s *Server) creatorContracts(c *gin.Context) {
	var items []model.Contract
	s.db.Where("creator_id = ?", middleware.UserID(c)).Order("created_at desc").Find(&items)
	response.OK(c, items)
}

func (s *Server) createContract(c *gin.Context) {
	var req model.Contract
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	req.ID = 0
	req.CreatorID = middleware.UserID(c)
	if req.Status == "" {
		req.Status = "signing"
	}
	if req.Provider == "" {
		req.Provider = "tencent-ess"
	}
	if err := s.db.Create(&req).Error; err != nil {
		response.Error(c, http.StatusBadRequest, "failed to create contract")
		return
	}
	response.Created(c, req)
}

func (s *Server) creatorRevenues(c *gin.Context) {
	var items []model.RevenueDaily
	q := s.db.Where("creator_id = ?", middleware.UserID(c))
	if from := c.Query("from"); from != "" {
		q = q.Where("day >= ?", from)
	}
	if to := c.Query("to"); to != "" {
		q = q.Where("day <= ?", to)
	}
	q.Order("day desc").Find(&items)
	response.OK(c, items)
}

func (s *Server) createWithdrawal(c *gin.Context) {
	var req model.Withdrawal
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	req.ID = 0
	req.CreatorID = middleware.UserID(c)
	req.Status = "pending"
	if err := s.db.Create(&req).Error; err != nil {
		response.Error(c, http.StatusBadRequest, "failed to create withdrawal")
		return
	}
	response.Created(c, req)
}

func (s *Server) creatorWithdrawals(c *gin.Context) {
	var items []model.Withdrawal
	s.db.Where("creator_id = ?", middleware.UserID(c)).Order("created_at desc").Find(&items)
	response.OK(c, items)
}
