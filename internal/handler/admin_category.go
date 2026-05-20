package handler

import (
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

func (s *Server) adminListCategories(c *gin.Context) {
	var items []model.Category
	if err := s.db.Order("sort_order asc, id asc").Find(&items).Error; err != nil {
		response.ServerError(c, "查询分类失败")
		return
	}
	views := make([]gin.H, 0, len(items))
	for _, cat := range items {
		views = append(views, categoryView(cat))
	}
	response.OK(c, gin.H{"list": views})
}

type categoryUpsertRequest struct {
	Name      *string `json:"name"`
	SortOrder *int    `json:"sort_order"`
	Status    *string `json:"status"`
}

func (s *Server) adminCreateCategory(c *gin.Context) {
	var req categoryUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == nil || *req.Name == "" {
		response.InvalidParam(c, "name 必填")
		return
	}

	cat := model.Category{Name: *req.Name, Status: model.StatusActive}
	if req.SortOrder != nil {
		cat.SortOrder = *req.SortOrder
	}
	if req.Status != nil && *req.Status != "" {
		cat.Status = *req.Status
	}
	if err := s.db.Create(&cat).Error; err != nil {
		response.ServerError(c, "创建分类失败")
		return
	}
	response.OK(c, categoryView(cat))
}

func (s *Server) adminUpdateCategory(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var cat model.Category
	if err := s.db.First(&cat, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "分类不存在")
			return
		}
		response.ServerError(c, "查询分类失败")
		return
	}

	var req categoryUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}

	updates := map[string]interface{}{}
	if req.Name != nil && *req.Name != "" {
		updates["name"] = *req.Name
	}
	if req.SortOrder != nil {
		updates["sort_order"] = *req.SortOrder
	}
	if req.Status != nil && *req.Status != "" {
		updates["status"] = *req.Status
	}
	if len(updates) == 0 {
		response.OK(c, categoryView(cat))
		return
	}

	if err := s.db.Model(&cat).Updates(updates).Error; err != nil {
		response.ServerError(c, "更新分类失败")
		return
	}
	s.db.First(&cat, id)
	response.OK(c, categoryView(cat))
}
