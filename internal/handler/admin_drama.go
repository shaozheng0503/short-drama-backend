package handler

import (
	"strings"
	"time"

	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

type dramaUpsertRequest struct {
	Title        *string  `json:"title"`
	Description  *string  `json:"description"`
	CoverURL     *string  `json:"cover_url"`
	CategoryID   *uint64  `json:"category_id"`
	CreatorID    *uint64  `json:"creator_id"`
	FreeEpisodes *int     `json:"free_episodes"`
	PriceCents   *int64   `json:"price_cents"`
	SortOrder    *int     `json:"sort_order"`
}

func (s *Server) adminListDramas(c *gin.Context) {
	page, pageSize := paginate(c)

	q := s.db.Model(&model.Drama{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := strings.TrimSpace(c.Query("keyword")); v != "" {
		like := "%" + v + "%"
		q = q.Where("title ILIKE ? OR description ILIKE ?", like, like)
	}
	if v := c.Query("category_id"); v != "" {
		if id := parseUint(v); id > 0 {
			q = q.Where("category_id = ?", id)
		}
	}
	if v := c.Query("creator_id"); v != "" {
		if id := parseUint(v); id > 0 {
			q = q.Where("creator_id = ?", id)
		}
	}

	var total int64
	q.Count(&total)
	var list []model.Drama
	if err := q.Order("updated_at desc").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Find(&list).Error; err != nil {
		response.ServerError(c, "查询短剧失败")
		return
	}

	views := make([]gin.H, 0, len(list))
	cats := s.collectCategoryNames(list)
	crts := s.collectCreatorNames(list)
	for _, d := range list {
		categoryName := ""
		if d.CategoryID != nil {
			categoryName = cats[*d.CategoryID]
		}
		creatorName := ""
		if d.CreatorID != nil {
			creatorName = crts[*d.CreatorID]
		}
		views = append(views, dramaAdminView(d, categoryName, creatorName))
	}
	response.OK(c, pageResp(views, page, pageSize, total))
}

func (s *Server) adminCreateDrama(c *gin.Context) {
	var req dramaUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Title == nil || *req.Title == "" {
		response.InvalidParam(c, "title 必填")
		return
	}
	drama := model.Drama{
		Title:        *req.Title,
		Status:       model.DramaStatusDraft,
		FreeEpisodes: 0,
		PriceCents:   0,
	}
	if req.Description != nil {
		drama.Description = *req.Description
	}
	if req.CoverURL != nil {
		drama.CoverURL = *req.CoverURL
	}
	if req.CategoryID != nil && *req.CategoryID > 0 {
		drama.CategoryID = req.CategoryID
	}
	if req.CreatorID != nil && *req.CreatorID > 0 {
		drama.CreatorID = req.CreatorID
	}
	if req.FreeEpisodes != nil && *req.FreeEpisodes >= 0 {
		drama.FreeEpisodes = *req.FreeEpisodes
	}
	if req.PriceCents != nil && *req.PriceCents >= 0 {
		drama.PriceCents = *req.PriceCents
	}
	if req.SortOrder != nil {
		drama.SortOrder = *req.SortOrder
	}
	if err := s.db.Create(&drama).Error; err != nil {
		response.ServerError(c, "创建短剧失败")
		return
	}
	response.OK(c, dramaAdminView(drama, s.nameOfCategory(drama.CategoryID), s.nameOfCreator(drama.CreatorID)))
}

func (s *Server) adminGetDrama(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var drama model.Drama
	if err := s.db.First(&drama, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "短剧不存在")
			return
		}
		response.ServerError(c, "查询短剧失败")
		return
	}

	var episodes []model.Episode
	s.db.Where("drama_id = ?", drama.ID).Order("episode_no asc").Find(&episodes)
	epViews := make([]gin.H, 0, len(episodes))
	for _, ep := range episodes {
		epViews = append(epViews, episodeAdminView(ep))
	}

	view := dramaAdminView(drama, s.nameOfCategory(drama.CategoryID), s.nameOfCreator(drama.CreatorID))
	view["episodes"] = epViews
	response.OK(c, view)
}

func (s *Server) adminUpdateDrama(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var drama model.Drama
	if err := s.db.First(&drama, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "短剧不存在")
			return
		}
		response.ServerError(c, "查询短剧失败")
		return
	}
	var req dramaUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}

	updates := map[string]interface{}{}
	if req.Title != nil && *req.Title != "" {
		updates["title"] = *req.Title
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.CoverURL != nil {
		updates["cover_url"] = *req.CoverURL
	}
	if req.CategoryID != nil {
		if *req.CategoryID == 0 {
			updates["category_id"] = nil
		} else {
			updates["category_id"] = *req.CategoryID
		}
	}
	if req.CreatorID != nil {
		if *req.CreatorID == 0 {
			updates["creator_id"] = nil
		} else {
			updates["creator_id"] = *req.CreatorID
		}
	}
	if req.FreeEpisodes != nil && *req.FreeEpisodes >= 0 {
		updates["free_episodes"] = *req.FreeEpisodes
	}
	if req.PriceCents != nil && *req.PriceCents >= 0 {
		updates["price_cents"] = *req.PriceCents
	}
	if req.SortOrder != nil {
		updates["sort_order"] = *req.SortOrder
	}

	if len(updates) > 0 {
		if err := s.db.Model(&drama).Updates(updates).Error; err != nil {
			response.ServerError(c, "更新短剧失败")
			return
		}
	}
	s.db.First(&drama, id)
	response.OK(c, dramaAdminView(drama, s.nameOfCategory(drama.CategoryID), s.nameOfCreator(drama.CreatorID)))
}

func (s *Server) adminPublishDrama(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var drama model.Drama
	if err := s.db.First(&drama, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "短剧不存在")
			return
		}
		response.ServerError(c, "查询短剧失败")
		return
	}

	var readyCount int64
	s.db.Model(&model.Episode{}).
		Where("drama_id = ? AND status = ?", drama.ID, model.EpisodeStatusReady).
		Count(&readyCount)
	if readyCount == 0 {
		response.InvalidParam(c, "至少需要 1 集 ready 状态剧集才能上架")
		return
	}

	now := time.Now()
	updates := map[string]interface{}{
		"status":       model.DramaStatusPublished,
		"published_at": now,
	}
	if err := s.db.Model(&drama).Updates(updates).Error; err != nil {
		response.ServerError(c, "上架失败")
		return
	}
	s.db.First(&drama, id)
	response.OK(c, dramaAdminView(drama, s.nameOfCategory(drama.CategoryID), s.nameOfCreator(drama.CreatorID)))
}

func (s *Server) adminOfflineDrama(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var drama model.Drama
	if err := s.db.First(&drama, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "短剧不存在")
			return
		}
		response.ServerError(c, "查询短剧失败")
		return
	}
	if err := s.db.Model(&drama).Update("status", model.DramaStatusOffline).Error; err != nil {
		response.ServerError(c, "下架失败")
		return
	}
	s.db.First(&drama, id)
	response.OK(c, dramaAdminView(drama, s.nameOfCategory(drama.CategoryID), s.nameOfCreator(drama.CreatorID)))
}

func (s *Server) collectCategoryNames(dramas []model.Drama) map[uint64]string {
	ids := make([]uint64, 0)
	seen := map[uint64]bool{}
	for _, d := range dramas {
		if d.CategoryID != nil && !seen[*d.CategoryID] {
			ids = append(ids, *d.CategoryID)
			seen[*d.CategoryID] = true
		}
	}
	if len(ids) == 0 {
		return map[uint64]string{}
	}
	var cats []model.Category
	s.db.Where("id IN ?", ids).Find(&cats)
	out := make(map[uint64]string, len(cats))
	for _, c := range cats {
		out[c.ID] = c.Name
	}
	return out
}

func (s *Server) collectCreatorNames(dramas []model.Drama) map[uint64]string {
	ids := make([]uint64, 0)
	seen := map[uint64]bool{}
	for _, d := range dramas {
		if d.CreatorID != nil && !seen[*d.CreatorID] {
			ids = append(ids, *d.CreatorID)
			seen[*d.CreatorID] = true
		}
	}
	if len(ids) == 0 {
		return map[uint64]string{}
	}
	var crts []model.Creator
	s.db.Where("id IN ?", ids).Find(&crts)
	out := make(map[uint64]string, len(crts))
	for _, cr := range crts {
		out[cr.ID] = cr.Name
	}
	return out
}

func (s *Server) nameOfCategory(id *uint64) string {
	if id == nil {
		return ""
	}
	var cat model.Category
	if err := s.db.Select("name").First(&cat, *id).Error; err != nil {
		return ""
	}
	return cat.Name
}

func (s *Server) nameOfCreator(id *uint64) string {
	if id == nil {
		return ""
	}
	var cr model.Creator
	if err := s.db.Select("name").First(&cr, *id).Error; err != nil {
		return ""
	}
	return cr.Name
}
