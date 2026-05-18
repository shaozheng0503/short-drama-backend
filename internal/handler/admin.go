package handler

import (
	"net/http"

	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

func (s *Server) adminDashboard(c *gin.Context) {
	var users, creators, dramas, orders int64
	var income int64
	s.db.Model(&model.User{}).Count(&users)
	s.db.Model(&model.User{}).Where("role = ?", model.RoleCreator).Count(&creators)
	s.db.Model(&model.Drama{}).Count(&dramas)
	s.db.Model(&model.Order{}).Where("status = ?", "paid").Count(&orders)
	s.db.Model(&model.Order{}).Where("status = ?", "paid").Select("COALESCE(SUM(amount_cents),0)").Scan(&income)
	response.OK(c, gin.H{"users": users, "creators": creators, "dramas": dramas, "paid_orders": orders, "income_cents": income})
}

func (s *Server) adminUsers(c *gin.Context) {
	var users []model.User
	q := s.db.Model(&model.User{})
	if role := c.Query("role"); role != "" {
		q = q.Where("role = ?", role)
	}
	q.Order("created_at desc").Limit(100).Find(&users)
	response.OK(c, users)
}

func (s *Server) verifyCreator(c *gin.Context) {
	var req struct {
		Status string `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.db.Model(&model.CreatorProfile{}).Where("user_id = ?", c.Param("id")).Update("verified_status", req.Status).Error; err != nil {
		response.Error(c, http.StatusBadRequest, "failed to update creator")
		return
	}
	response.OK(c, gin.H{"status": req.Status})
}

func (s *Server) adminCreateDrama(c *gin.Context) {
	var req model.Drama
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	req.ID = 0
	if req.Status == "" {
		req.Status = "published"
	}
	if err := s.db.Create(&req).Error; err != nil {
		response.Error(c, http.StatusBadRequest, "failed to create drama")
		return
	}
	response.Created(c, req)
}

func (s *Server) adminUpdateDrama(c *gin.Context) {
	var drama model.Drama
	if err := s.db.First(&drama, c.Param("id")).Error; err != nil {
		response.Error(c, http.StatusNotFound, "drama not found")
		return
	}
	var req model.Drama
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	req.ID = drama.ID
	s.db.Model(&drama).Updates(req)
	response.OK(c, drama)
}

func (s *Server) updateDramaStatus(c *gin.Context) {
	var req struct {
		Status string `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	s.db.Model(&model.Drama{}).Where("id = ?", c.Param("id")).Update("status", req.Status)
	response.OK(c, gin.H{"status": req.Status})
}

func (s *Server) adminOrders(c *gin.Context) {
	var items []model.Order
	q := s.db.Model(&model.Order{})
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	q.Order("created_at desc").Limit(100).Find(&items)
	response.OK(c, items)
}

func (s *Server) adminWithdrawals(c *gin.Context) {
	var items []model.Withdrawal
	q := s.db.Model(&model.Withdrawal{})
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	q.Order("created_at desc").Limit(100).Find(&items)
	response.OK(c, items)
}

func (s *Server) updateWithdrawalStatus(c *gin.Context) {
	var req struct {
		Status string `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	s.db.Model(&model.Withdrawal{}).Where("id = ?", c.Param("id")).Update("status", req.Status)
	response.OK(c, gin.H{"status": req.Status})
}

func (s *Server) adminContracts(c *gin.Context) {
	var items []model.Contract
	q := s.db.Model(&model.Contract{})
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	q.Order("created_at desc").Limit(100).Find(&items)
	response.OK(c, items)
}

func (s *Server) updateContractStatus(c *gin.Context) {
	var req struct {
		Status      string `json:"status" binding:"required"`
		ExternalID  string `json:"external_id"`
		DownloadURL string `json:"download_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	updates := map[string]interface{}{"status": req.Status}
	if req.ExternalID != "" {
		updates["external_id"] = req.ExternalID
	}
	if req.DownloadURL != "" {
		updates["download_url"] = req.DownloadURL
	}
	s.db.Model(&model.Contract{}).Where("id = ?", c.Param("id")).Updates(updates)
	response.OK(c, updates)
}
