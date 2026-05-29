package handler

import (
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

func languageView(l model.Language) gin.H {
	return gin.H{
		"id":         l.ID,
		"parent_id":  l.ParentID,
		"name":       l.Name,
		"code":       l.Code,
		"sort_order": l.SortOrder,
		"status":     l.Status,
	}
}

// listLanguages —— GET /v1/common/languages
// 返回语言树：每个语言带其下方言子项（dialects）。海外版做语言/方言筛选用。
// 默认只返回 active；带 ?all=1（管理端）返回全部。
func (s *Server) listLanguages(c *gin.Context) {
	q := s.db.Model(&model.Language{})
	if c.Query("all") != "1" {
		q = q.Where("status = ?", model.StatusActive)
	}
	var items []model.Language
	if err := q.Order("sort_order asc, id asc").Find(&items).Error; err != nil {
		response.ServerError(c, "查询语言列表失败")
		return
	}
	// 分组：parent 为空的是语言，其余挂到对应语言的 dialects 下。
	dialectsByParent := map[uint64][]gin.H{}
	for _, l := range items {
		if l.ParentID != nil {
			dialectsByParent[*l.ParentID] = append(dialectsByParent[*l.ParentID], languageView(l))
		}
	}
	list := make([]gin.H, 0)
	for _, l := range items {
		if l.ParentID != nil {
			continue
		}
		v := languageView(l)
		ds := dialectsByParent[l.ID]
		if ds == nil {
			ds = []gin.H{}
		}
		v["dialects"] = ds
		list = append(list, v)
	}
	response.OK(c, gin.H{"list": list, "total": int64(len(list))})
}

type languageUpsertRequest struct {
	ParentID  *uint64 `json:"parent_id"`
	Name      *string `json:"name"`
	Code      *string `json:"code"`
	SortOrder *int    `json:"sort_order"`
	Status    *string `json:"status"`
}

// 校验 parent_id 指向的必须是一个存在的「语言」（parent 为空），方言不能再挂方言。
func (s *Server) validLanguageParent(parentID uint64) bool {
	var parent model.Language
	if err := s.db.First(&parent, parentID).Error; err != nil {
		return false
	}
	return parent.ParentID == nil
}

func (s *Server) adminCreateLanguage(c *gin.Context) {
	var req languageUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == nil || *req.Name == "" {
		response.InvalidParam(c, "name 必填")
		return
	}
	lang := model.Language{Name: *req.Name, Status: model.StatusActive}
	if req.ParentID != nil && *req.ParentID > 0 {
		if !s.validLanguageParent(*req.ParentID) {
			response.InvalidParam(c, "parent_id 必须指向一个已存在的语言（方言不能再挂方言）")
			return
		}
		lang.ParentID = req.ParentID
	}
	if req.Code != nil {
		lang.Code = *req.Code
	}
	if req.SortOrder != nil {
		lang.SortOrder = *req.SortOrder
	}
	if req.Status != nil && *req.Status != "" {
		lang.Status = *req.Status
	}
	if err := s.db.Create(&lang).Error; err != nil {
		response.ServerError(c, "创建语言失败")
		return
	}
	response.OK(c, languageView(lang))
}

func (s *Server) adminUpdateLanguage(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var lang model.Language
	if err := s.db.First(&lang, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "语言不存在")
			return
		}
		response.ServerError(c, "查询语言失败")
		return
	}
	var req languageUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}
	updates := map[string]interface{}{}
	if req.ParentID != nil {
		if *req.ParentID == 0 {
			updates["parent_id"] = nil
		} else {
			if *req.ParentID == id {
				response.InvalidParam(c, "parent_id 不能指向自身")
				return
			}
			if !s.validLanguageParent(*req.ParentID) {
				response.InvalidParam(c, "parent_id 必须指向一个已存在的语言")
				return
			}
			updates["parent_id"] = *req.ParentID
		}
	}
	if req.Name != nil && *req.Name != "" {
		updates["name"] = *req.Name
	}
	if req.Code != nil {
		updates["code"] = *req.Code
	}
	if req.SortOrder != nil {
		updates["sort_order"] = *req.SortOrder
	}
	if req.Status != nil && *req.Status != "" {
		updates["status"] = *req.Status
	}
	if len(updates) == 0 {
		response.OK(c, languageView(lang))
		return
	}
	if err := s.db.Model(&lang).Updates(updates).Error; err != nil {
		response.ServerError(c, "更新语言失败")
		return
	}
	s.db.First(&lang, id)
	response.OK(c, languageView(lang))
}

func (s *Server) adminDeleteLanguage(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var lang model.Language
	if err := s.db.First(&lang, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "语言不存在")
			return
		}
		response.ServerError(c, "查询语言失败")
		return
	}
	// 语言下若还有方言子项，先挡住。
	var childCnt int64
	s.db.Model(&model.Language{}).Where("parent_id = ?", id).Count(&childCnt)
	if childCnt > 0 {
		response.Conflict(c, "请先删除该语言下的方言")
		return
	}
	// 被短剧引用（语言或方言）时不允许删除。
	var refCnt int64
	s.db.Model(&model.Drama{}).Where("language_id = ? OR dialect_id = ?", id, id).Count(&refCnt)
	if refCnt > 0 {
		response.Conflict(c, "语言仍被短剧引用，无法删除")
		return
	}
	if err := s.db.Delete(&lang).Error; err != nil {
		response.ServerError(c, "删除语言失败")
		return
	}
	response.OK(c, gin.H{"id": id, "deleted": true})
}
