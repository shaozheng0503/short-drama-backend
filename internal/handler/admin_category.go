package handler

import (
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

// validCategoryType 白名单：只接受 4 个红果维度，避免 type 字段被乱写。
func validCategoryType(t string) bool {
	switch t {
	case model.CategoryTypeTheme, model.CategoryTypeSetting,
		model.CategoryTypeBackground, model.CategoryTypeAudience:
		return true
	}
	return false
}

func (s *Server) adminListCategories(c *gin.Context) {
	q := s.db.Model(&model.Category{})
	if t := c.Query("type"); t != "" {
		if !validCategoryType(t) {
			response.InvalidParam(c, "type 不合法")
			return
		}
		q = q.Where("type = ?", t)
	}
	var items []model.Category
	if err := q.Order("type asc, sort_order asc, id asc").Find(&items).Error; err != nil {
		response.ServerError(c, "查询分类失败")
		return
	}
	views := make([]gin.H, 0, len(items))
	for _, cat := range items {
		views = append(views, categoryView(cat))
	}
	// 分类总条数有限（按 4 维拆开顶多几十条），不做分页；但为了让前端拿到的列表
	// 响应结构与其他 list 接口对齐（total/page/page_size/has_more），明确给死值。
	response.OK(c, gin.H{
		"list":      views,
		"total":     int64(len(views)),
		"page":      1,
		"page_size": len(views),
		"has_more":  false,
	})
}

type categoryUpsertRequest struct {
	Type      *string `json:"type"`
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

	cat := model.Category{Name: *req.Name, Status: model.StatusActive, Type: model.CategoryTypeTheme}
	if req.Type != nil && *req.Type != "" {
		if !validCategoryType(*req.Type) {
			response.InvalidParam(c, "type 不合法")
			return
		}
		cat.Type = *req.Type
	}
	if req.SortOrder != nil {
		cat.SortOrder = *req.SortOrder
	}
	if req.Status != nil && *req.Status != "" {
		cat.Status = *req.Status
	}
	if err := s.db.Create(&cat).Error; err != nil {
		if isUniqueViolation(err) {
			response.Conflict(c, "该 type 下同名分类已存在")
			return
		}
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
	if req.Type != nil && *req.Type != "" {
		if !validCategoryType(*req.Type) {
			response.InvalidParam(c, "type 不合法")
			return
		}
		updates["type"] = *req.Type
	}
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
		if isUniqueViolation(err) {
			response.Conflict(c, "该 type 下同名分类已存在")
			return
		}
		response.ServerError(c, "更新分类失败")
		return
	}
	s.db.First(&cat, id)
	response.OK(c, categoryView(cat))
}
