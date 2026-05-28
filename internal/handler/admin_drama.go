package handler

import (
	"strings"
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type dramaUpsertRequest struct {
	Title        *string `json:"title"`
	Description  *string `json:"description"`
	CoverURL     *string `json:"cover_url"`
	CategoryID   *uint64 `json:"category_id"`
	CreatorID    *uint64 `json:"creator_id"`
	FreeEpisodes *int    `json:"free_episodes"`
	PriceCents   *int64  `json:"price_cents"`
	SortOrder    *int    `json:"sort_order"`
}

func validateDramaNumericFields(req *dramaUpsertRequest) string {
	if req.PriceCents != nil && *req.PriceCents < 0 {
		return "price_cents 不能为负"
	}
	if req.FreeEpisodes != nil && *req.FreeEpisodes < 0 {
		return "free_episodes 不能为负"
	}
	return ""
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
	if msg := validateDramaNumericFields(&req); msg != "" {
		response.InvalidParam(c, msg)
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
		if !s.categoryExists(*req.CategoryID) {
			response.NotFound(c, "分类不存在")
			return
		}
		drama.CategoryID = req.CategoryID
	}
	if req.CreatorID != nil && *req.CreatorID > 0 {
		if !s.creatorExists(*req.CreatorID) {
			response.NotFound(c, "创作者不存在")
			return
		}
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
	if msg := validateDramaNumericFields(&req); msg != "" {
		response.InvalidParam(c, msg)
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
			if !s.categoryExists(*req.CategoryID) {
				response.NotFound(c, "分类不存在")
				return
			}
			updates["category_id"] = *req.CategoryID
		}
	}
	if req.CreatorID != nil {
		if *req.CreatorID == 0 {
			updates["creator_id"] = nil
		} else {
			if !s.creatorExists(*req.CreatorID) {
				response.NotFound(c, "创作者不存在")
				return
			}
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

	var totalEpisodes int64
	s.db.Model(&model.Episode{}).Where("drama_id = ?", drama.ID).Count(&totalEpisodes)
	if totalEpisodes > int64(drama.FreeEpisodes) && drama.PriceCents <= 0 {
		response.InvalidParam(c, "存在付费剧集时 price_cents 必须大于 0")
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

// adminRejectDrama —— 管理员驳回。audit_status → rejected；
// 若 drama 当前为 published 强制 offline（涉及合规风险，立即下架优先于通知 creator）。
type adminRejectDramaRequest struct {
	Reason string `json:"reason"`
}

func (s *Server) adminRejectDrama(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var req adminRejectDramaRequest
	_ = c.ShouldBindJSON(&req)
	if len(req.Reason) > 255 {
		response.InvalidParam(c, "reason 不能超过 255 字符")
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
	now := time.Now()
	reviewerID := middleware.CurrentID(c)
	updates := map[string]interface{}{
		"audit_status": model.DramaAuditRejected,
		"audit_reason": req.Reason,
		"reviewer_id":  reviewerID,
		"reviewed_at":  now,
	}
	// 驳回时同步推进 status，避免 reviewing+rejected 这种"还在审核中又被驳回"的语义矛盾态：
	//   - published → offline（立即下架）
	//   - reviewing → draft（等创作者改完重新提审）
	//   - draft / offline 不动（边界情况兜底，例如对 draft 标驳回作为"硬卡"）
	switch drama.Status {
	case model.DramaStatusPublished:
		updates["status"] = model.DramaStatusOffline
	case model.DramaStatusReviewing:
		updates["status"] = model.DramaStatusDraft
	}
	if err := s.db.Model(&drama).Updates(updates).Error; err != nil {
		response.ServerError(c, "驳回失败")
		return
	}
	s.db.First(&drama, id)
	if drama.CreatorID != nil {
		content := "您的作品《" + drama.Title + "》审核未通过，请修改后重新提交。"
		if req.Reason != "" {
			content += "驳回原因：" + req.Reason
		}
		s.sendNotification(*drama.CreatorID, "作品审核未通过", content, "")
	}
	response.OK(c, dramaAdminView(drama, s.nameOfCategory(drama.CategoryID), s.nameOfCreator(drama.CreatorID)))
}

// adminApproveDrama —— 管理员审核通过（多用于把先前 rejected 的剧恢复）。
// 只动 audit_status 与审计字段，不动 status；要上线由创作者自助 publish 或 admin publish。
func (s *Server) adminApproveDrama(c *gin.Context) {
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
	now := time.Now()
	reviewerID := middleware.CurrentID(c)
	updates := map[string]interface{}{
		"audit_status": model.DramaAuditApproved,
		"audit_reason": "",
		"reviewer_id":  reviewerID,
		"reviewed_at":  now,
	}
	if err := s.db.Model(&drama).Updates(updates).Error; err != nil {
		response.ServerError(c, "审核通过失败")
		return
	}
	s.db.First(&drama, id)
	if drama.CreatorID != nil {
		s.sendNotification(*drama.CreatorID, "作品审核通过",
			"您的作品《"+drama.Title+"》已审核通过，可以发布上架。", "")
	}
	response.OK(c, dramaAdminView(drama, s.nameOfCategory(drama.CategoryID), s.nameOfCreator(drama.CreatorID)))
}

// adminDeleteDrama —— 仅 draft 状态允许删除，避免误删已上架/曾发布过的剧。
// 同事务里级联 episodes + drama_tags；订单/解锁等用户已产生的数据不动（理论上 draft 不可能有这些）。
func (s *Server) adminDeleteDrama(c *gin.Context) {
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
	if drama.Status != model.DramaStatusDraft {
		response.Conflict(c, "仅草稿状态可删除，请先下架并改回草稿")
		return
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("drama_id = ?", id).Delete(&model.Episode{}).Error; err != nil {
			return err
		}
		if err := tx.Where("drama_id = ?", id).Delete(&model.DramaTag{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Drama{}, id).Error
	})
	if err != nil {
		response.ServerError(c, "删除短剧失败")
		return
	}
	response.OK(c, gin.H{"deleted": true, "id": id})
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

func (s *Server) categoryExists(id uint64) bool {
	var cnt int64
	s.db.Model(&model.Category{}).Where("id = ?", id).Count(&cnt)
	return cnt > 0
}

func (s *Server) creatorExists(id uint64) bool {
	var cnt int64
	s.db.Model(&model.Creator{}).Where("id = ?", id).Count(&cnt)
	return cnt > 0
}
