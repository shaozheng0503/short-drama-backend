package handler

import (
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	dramaTitleMaxRune = 20
	dramaDescMaxRune  = 200
	dramaMaxCovers    = 5
)

// characterInput —— 角色录入。姓名必填，照片/简介选填。
type characterInput struct {
	Name     string `json:"name"`
	PhotoURL string `json:"photo_url"`
	Intro    string `json:"intro"`
}

// creatorDramaRequest —— 创作者侧的创建/更新载荷。
// 故意不收 creator_id（始终用当前登录 creator）/ sort_order（编排归 admin）。
// 所有字段都是指针/可选：create 时缺省走默认，update 时只改传了的字段。
type creatorDramaRequest struct {
	Title         *string  `json:"title"`
	Description   *string  `json:"description"`
	CoverURL      *string  `json:"cover_url"`    // 兼容旧单图字段
	CoverURLs     []string `json:"cover_urls"`   // 多图封面（≤5），传了以它为准；主封面取第一张
	CategoryID    *uint64  `json:"category_id"`  // 兼容旧单分类
	CategoryIDs   []uint64 `json:"category_ids"` // 多选分类（写 drama_tags，主分类取第一项）
	FreeEpisodes  *int     `json:"free_episodes"`
	PriceCents    *int64   `json:"price_cents"`
	TotalEpisodes *int     `json:"total_episodes"` // 承诺总集数

	// === 申报级扩展字段 ===
	IsAI                *bool      `json:"is_ai"`
	Audience            *string    `json:"audience"` // 男频/女频/通用
	AliasPaid           *string    `json:"alias_paid"`
	AliasFree           *string    `json:"alias_free"`
	ProductionOrg       *string    `json:"production_org"`
	Producer            *string    `json:"producer"`
	Director            *string    `json:"director"`
	Screenwriter        *string    `json:"screenwriter"`
	ProductionCostCents *int64     `json:"production_cost_cents"`
	CostConfigURL       *string    `json:"cost_config_url"`
	IsIPAdaptation      *bool      `json:"is_ip_adaptation"`
	CopyrightFileURL    *string    `json:"copyright_file_url"`
	NonInfringementURL  *string    `json:"non_infringement_url"`
	PublishType         *string    `json:"publish_type"` // self/platform
	ScheduledPublishAt  *time.Time `json:"scheduled_publish_at"`

	// 角色：传了就整体替换（含空数组=清空）。MVP 从宽，不强制至少一位。
	Characters *[]characterInput `json:"characters"`
}

// validateCreatorDrama 做轻量校验。MVP 原则：字段从宽，只挡明显错误（长度上限、负数、枚举、多图上限）。
func validateCreatorDrama(req *creatorDramaRequest) string {
	if req.Title != nil && runeLen(*req.Title) > dramaTitleMaxRune {
		return "title 不能超过 20 个字"
	}
	if req.Description != nil && runeLen(*req.Description) > dramaDescMaxRune {
		return "description 不能超过 200 个字"
	}
	if len(req.CoverURLs) > dramaMaxCovers {
		return "封面最多 5 张"
	}
	if req.PriceCents != nil && *req.PriceCents < 0 {
		return "price_cents 不能为负"
	}
	if req.FreeEpisodes != nil && *req.FreeEpisodes < 0 {
		return "free_episodes 不能为负"
	}
	if req.TotalEpisodes != nil && *req.TotalEpisodes < 0 {
		return "total_episodes 不能为负"
	}
	if req.ProductionCostCents != nil && *req.ProductionCostCents < 0 {
		return "production_cost_cents 不能为负"
	}
	if req.Audience != nil && *req.Audience != "" && !model.ValidAudience(*req.Audience) {
		return "audience 只能是 男频/女频/通用"
	}
	if req.PublishType != nil && *req.PublishType != "" && !model.ValidPublishType(*req.PublishType) {
		return "publish_type 只能是 self/platform"
	}
	if req.Characters != nil {
		for _, ch := range *req.Characters {
			if ch.Name == "" {
				return "角色姓名必填"
			}
		}
	}
	return ""
}

// effectiveCovers 取最终封面列表：优先 cover_urls，否则退回单图 cover_url。
func effectiveCovers(req *creatorDramaRequest) ([]string, bool) {
	if req.CoverURLs != nil {
		return req.CoverURLs, true
	}
	if req.CoverURL != nil && *req.CoverURL != "" {
		return []string{*req.CoverURL}, true
	}
	return nil, false
}

// effectiveCategoryIDs 取最终分类列表：优先 category_ids，否则退回单分类 category_id。
func effectiveCategoryIDs(req *creatorDramaRequest) ([]uint64, bool) {
	if req.CategoryIDs != nil {
		return req.CategoryIDs, true
	}
	if req.CategoryID != nil && *req.CategoryID > 0 {
		return []uint64{*req.CategoryID}, true
	}
	return nil, false
}

// replaceDramaCovers 整体替换某剧的封面行，并把第一张写回 dramas.cover_url 作为主封面。
func (s *Server) replaceDramaCovers(tx *gorm.DB, dramaID uint64, urls []string) error {
	if err := tx.Where("drama_id = ?", dramaID).Delete(&model.DramaCover{}).Error; err != nil {
		return err
	}
	primary := ""
	for i, u := range urls {
		if u == "" {
			continue
		}
		if primary == "" {
			primary = u
		}
		if err := tx.Create(&model.DramaCover{DramaID: dramaID, URL: u, SortOrder: i}).Error; err != nil {
			return err
		}
	}
	return tx.Model(&model.Drama{}).Where("id = ?", dramaID).Update("cover_url", primary).Error
}

// replaceDramaCharacters 整体替换角色行。
func (s *Server) replaceDramaCharacters(tx *gorm.DB, dramaID uint64, chars []characterInput) error {
	if err := tx.Where("drama_id = ?", dramaID).Delete(&model.DramaCharacter{}).Error; err != nil {
		return err
	}
	for i, ch := range chars {
		row := model.DramaCharacter{
			DramaID:   dramaID,
			Name:      ch.Name,
			PhotoURL:  ch.PhotoURL,
			Intro:     ch.Intro,
			SortOrder: i,
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

// replaceDramaCategoryTags 整体替换分类多对多，并把第一项写回 dramas.category_id 作为主分类。
func (s *Server) replaceDramaCategoryTags(tx *gorm.DB, dramaID uint64, categoryIDs []uint64) error {
	if err := tx.Where("drama_id = ?", dramaID).Delete(&model.DramaTag{}).Error; err != nil {
		return err
	}
	var primary *uint64
	seen := map[uint64]bool{}
	for _, cid := range categoryIDs {
		if cid == 0 || seen[cid] {
			continue
		}
		seen[cid] = true
		if primary == nil {
			c := cid
			primary = &c
		}
		if err := tx.Create(&model.DramaTag{DramaID: dramaID, CategoryID: cid}).Error; err != nil {
			return err
		}
	}
	return tx.Model(&model.Drama{}).Where("id = ?", dramaID).Update("category_id", primary).Error
}

// loadDramaExtras 读取某剧的封面 + 角色列表，供详情视图拼装。
func (s *Server) loadDramaExtras(dramaID uint64) (covers []gin.H, characters []gin.H) {
	var coverRows []model.DramaCover
	s.db.Where("drama_id = ?", dramaID).Order("sort_order asc").Find(&coverRows)
	covers = make([]gin.H, 0, len(coverRows))
	for _, cv := range coverRows {
		covers = append(covers, gin.H{"id": cv.ID, "url": cv.URL, "sort_order": cv.SortOrder})
	}
	var charRows []model.DramaCharacter
	s.db.Where("drama_id = ?", dramaID).Order("sort_order asc").Find(&charRows)
	characters = make([]gin.H, 0, len(charRows))
	for _, ch := range charRows {
		characters = append(characters, gin.H{
			"id": ch.ID, "name": ch.Name, "photo_url": ch.PhotoURL, "intro": ch.Intro, "sort_order": ch.SortOrder,
		})
	}
	return covers, characters
}

// validateCategoryIDs 校验分类 ID 都存在。
func (s *Server) validateCategoryIDs(ids []uint64) bool {
	for _, id := range ids {
		if id != 0 && !s.categoryExists(id) {
			return false
		}
	}
	return true
}

func (s *Server) creatorCreateDrama(c *gin.Context) {
	cid := middleware.CurrentID(c)
	var req creatorDramaRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Title == nil || *req.Title == "" {
		response.InvalidParam(c, "title 必填")
		return
	}
	if msg := validateCreatorDrama(&req); msg != "" {
		response.InvalidParam(c, msg)
		return
	}

	categoryIDs, hasCats := effectiveCategoryIDs(&req)
	if hasCats && !s.validateCategoryIDs(categoryIDs) {
		response.NotFound(c, "分类不存在")
		return
	}

	creatorID := cid
	// 价格 / 免费集数缺省走全局配置兜底，未配置则 0。
	freeEp, priceCents := s.pricingDefaults()
	drama := model.Drama{
		Title:        *req.Title,
		Status:       model.DramaStatusDraft,
		AuditStatus:  model.DramaAuditApproved, // 默认通过，admin 可单独驳回
		CreatorID:    &creatorID,
		FreeEpisodes: freeEp,
		PriceCents:   priceCents,
	}
	applyDramaScalars(&drama, &req)
	if req.FreeEpisodes != nil && *req.FreeEpisodes >= 0 {
		drama.FreeEpisodes = *req.FreeEpisodes
	}
	if req.PriceCents != nil && *req.PriceCents >= 0 {
		drama.PriceCents = *req.PriceCents
	}

	covers, hasCovers := effectiveCovers(&req)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&drama).Error; err != nil {
			return err
		}
		if hasCats {
			if err := s.replaceDramaCategoryTags(tx, drama.ID, categoryIDs); err != nil {
				return err
			}
		}
		if hasCovers {
			if err := s.replaceDramaCovers(tx, drama.ID, covers); err != nil {
				return err
			}
		}
		if req.Characters != nil {
			if err := s.replaceDramaCharacters(tx, drama.ID, *req.Characters); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		response.ServerError(c, "创建短剧失败")
		return
	}
	var fresh model.Drama
	s.db.First(&fresh, drama.ID)
	view := dramaAdminView(fresh, s.nameOfCategory(fresh.CategoryID), s.nameOfCreator(fresh.CreatorID))
	view["covers"], view["characters"] = s.loadDramaExtras(fresh.ID)
	response.OK(c, view)
}

// applyDramaScalars 把 req 里非空的标量申报字段写进 drama（用于 create）。
func applyDramaScalars(d *model.Drama, req *creatorDramaRequest) {
	if req.Description != nil {
		d.Description = *req.Description
	}
	if req.TotalEpisodes != nil {
		d.TotalEpisodes = *req.TotalEpisodes
	}
	if req.IsAI != nil {
		d.IsAI = *req.IsAI
	}
	if req.Audience != nil {
		d.Audience = *req.Audience
	}
	if req.AliasPaid != nil {
		d.AliasPaid = *req.AliasPaid
	}
	if req.AliasFree != nil {
		d.AliasFree = *req.AliasFree
	}
	if req.ProductionOrg != nil {
		d.ProductionOrg = *req.ProductionOrg
	}
	if req.Producer != nil {
		d.Producer = *req.Producer
	}
	if req.Director != nil {
		d.Director = *req.Director
	}
	if req.Screenwriter != nil {
		d.Screenwriter = *req.Screenwriter
	}
	if req.ProductionCostCents != nil {
		d.ProductionCostCents = *req.ProductionCostCents
	}
	if req.CostConfigURL != nil {
		d.CostConfigURL = *req.CostConfigURL
	}
	if req.IsIPAdaptation != nil {
		d.IsIPAdaptation = *req.IsIPAdaptation
	}
	if req.CopyrightFileURL != nil {
		d.CopyrightFileURL = *req.CopyrightFileURL
	}
	if req.NonInfringementURL != nil {
		d.NonInfringementURL = *req.NonInfringementURL
	}
	if req.PublishType != nil {
		d.PublishType = *req.PublishType
	}
	if req.ScheduledPublishAt != nil {
		d.ScheduledPublishAt = req.ScheduledPublishAt
	}
}

// creatorGetDrama —— 创作者查看自己剧的详情（含集列表）。
func (s *Server) creatorGetDrama(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	d, ok := s.requireCreatorOwnsDrama(c, id)
	if !ok {
		return
	}
	var episodes []model.Episode
	s.db.Where("drama_id = ?", d.ID).Order("episode_no asc").Find(&episodes)
	epViews := make([]gin.H, 0, len(episodes))
	for _, ep := range episodes {
		epViews = append(epViews, episodeAdminView(ep))
	}
	view := dramaAdminView(*d, s.nameOfCategory(d.CategoryID), s.nameOfCreator(d.CreatorID))
	view["episodes"] = epViews
	view["covers"], view["characters"] = s.loadDramaExtras(d.ID)
	response.OK(c, view)
}

// creatorUpdateDrama —— draft/offline 可编辑；被审核驳回的 reviewing 也必须能编辑后重新提交。
// 若当前 audit_status=rejected，编辑视为重新修改：回到 draft，清掉驳回原因，等待再次 submit。
func (s *Server) creatorUpdateDrama(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	d, ok := s.requireCreatorOwnsDrama(c, id)
	if !ok {
		return
	}
	// draft / offline 都允许编辑；rejected 即使仍处于 reviewing，也允许编辑。
	// published 仍禁止改，避免动了用户已购的核心字段（price/free_episodes）。
	if d.Status != model.DramaStatusDraft && d.Status != model.DramaStatusOffline && d.AuditStatus != model.DramaAuditRejected {
		response.Conflict(c, "仅草稿/已下架状态可编辑，请先下架")
		return
	}

	var req creatorDramaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}
	if msg := validateCreatorDrama(&req); msg != "" {
		response.InvalidParam(c, msg)
		return
	}

	categoryIDs, hasCats := effectiveCategoryIDs(&req)
	if hasCats && !s.validateCategoryIDs(categoryIDs) {
		response.NotFound(c, "分类不存在")
		return
	}

	updates := buildDramaUpdates(&req)
	// 被驳回的剧创作者改字段时 status 保持不变（按"没发布前 status 都在 reviewing"的语义）：
	// audit_status=rejected / audit_reason / reviewer_id / reviewed_at 全部保留——让创作者改字段期间
	// 一直能看到驳回原因，也让中台 / 合规能追溯审核历史。等创作者显式调 creatorSubmitDrama 重新提审，
	// 那一步会把 audit_status 切回 pending 并清掉旧的驳回痕迹。

	covers, hasCovers := effectiveCovers(&req)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if len(updates) > 0 {
			if err := tx.Model(d).Updates(updates).Error; err != nil {
				return err
			}
		}
		if hasCats {
			if err := s.replaceDramaCategoryTags(tx, d.ID, categoryIDs); err != nil {
				return err
			}
		}
		if hasCovers {
			if err := s.replaceDramaCovers(tx, d.ID, covers); err != nil {
				return err
			}
		}
		if req.Characters != nil {
			if err := s.replaceDramaCharacters(tx, d.ID, *req.Characters); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		response.ServerError(c, "更新短剧失败")
		return
	}
	var fresh model.Drama
	s.db.First(&fresh, id)
	view := dramaAdminView(fresh, s.nameOfCategory(fresh.CategoryID), s.nameOfCreator(fresh.CreatorID))
	view["covers"], view["characters"] = s.loadDramaExtras(fresh.ID)
	response.OK(c, view)
}

// buildDramaUpdates 把 req 里传了的标量字段拼成 updates map（用于 update；封面/分类/角色走子表替换）。
func buildDramaUpdates(req *creatorDramaRequest) map[string]interface{} {
	updates := map[string]interface{}{}
	if req.Title != nil && *req.Title != "" {
		updates["title"] = *req.Title
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.TotalEpisodes != nil && *req.TotalEpisodes >= 0 {
		updates["total_episodes"] = *req.TotalEpisodes
	}
	if req.FreeEpisodes != nil && *req.FreeEpisodes >= 0 {
		updates["free_episodes"] = *req.FreeEpisodes
	}
	if req.PriceCents != nil && *req.PriceCents >= 0 {
		updates["price_cents"] = *req.PriceCents
	}
	if req.IsAI != nil {
		updates["is_ai"] = *req.IsAI
	}
	if req.Audience != nil {
		updates["audience"] = *req.Audience
	}
	if req.AliasPaid != nil {
		updates["alias_paid"] = *req.AliasPaid
	}
	if req.AliasFree != nil {
		updates["alias_free"] = *req.AliasFree
	}
	if req.ProductionOrg != nil {
		updates["production_org"] = *req.ProductionOrg
	}
	if req.Producer != nil {
		updates["producer"] = *req.Producer
	}
	if req.Director != nil {
		updates["director"] = *req.Director
	}
	if req.Screenwriter != nil {
		updates["screenwriter"] = *req.Screenwriter
	}
	if req.ProductionCostCents != nil && *req.ProductionCostCents >= 0 {
		updates["production_cost_cents"] = *req.ProductionCostCents
	}
	if req.CostConfigURL != nil {
		updates["cost_config_url"] = *req.CostConfigURL
	}
	if req.IsIPAdaptation != nil {
		updates["is_ip_adaptation"] = *req.IsIPAdaptation
	}
	if req.CopyrightFileURL != nil {
		updates["copyright_file_url"] = *req.CopyrightFileURL
	}
	if req.NonInfringementURL != nil {
		updates["non_infringement_url"] = *req.NonInfringementURL
	}
	if req.PublishType != nil {
		updates["publish_type"] = *req.PublishType
	}
	if req.ScheduledPublishAt != nil {
		updates["scheduled_publish_at"] = req.ScheduledPublishAt
	}
	return updates
}

// creatorDeleteDrama —— 仅 draft 可删；级联 episodes + drama_tags。
func (s *Server) creatorDeleteDrama(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	d, ok := s.requireCreatorOwnsDrama(c, id)
	if !ok {
		return
	}
	if d.Status != model.DramaStatusDraft {
		response.Conflict(c, "仅草稿状态可删除，请先下架")
		return
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("drama_id = ?", d.ID).Delete(&model.Episode{}).Error; err != nil {
			return err
		}
		if err := tx.Where("drama_id = ?", d.ID).Delete(&model.DramaTag{}).Error; err != nil {
			return err
		}
		if err := tx.Where("drama_id = ?", d.ID).Delete(&model.DramaCover{}).Error; err != nil {
			return err
		}
		if err := tx.Where("drama_id = ?", d.ID).Delete(&model.DramaCharacter{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Drama{}, d.ID).Error
	})
	if err != nil {
		response.ServerError(c, "删除短剧失败")
		return
	}
	response.OK(c, gin.H{"deleted": true, "id": d.ID})
}

// creatorPublishDrama —— 创作者自助上架。
// 校验：audit_status=approved + 至少 1 集 ready + 存在付费集时 price_cents>0。
// 这套规则与 adminPublishDrama 一致；admin 那条没卡 audit_status，按"运营兜底"留出口子。
func (s *Server) creatorPublishDrama(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	d, ok := s.requireCreatorOwnsDrama(c, id)
	if !ok {
		return
	}
	// 通过审核才能进入发布环节：audit_status 必须 approved，pending / rejected 都拒。
	switch d.AuditStatus {
	case model.DramaAuditPending:
		response.Forbidden(c, "该剧正在审核中，待审核通过后才能上架")
		return
	case model.DramaAuditRejected:
		response.Forbidden(c, "该剧已被驳回，请先修改后重新提审")
		return
	}
	// status 守卫：只允许从"待发布"或"已下架"上架。draft 状态(没通过审核或刚撤回)
	// / published(已上架，重复发) 都不应触发 publish。
	if d.Status != model.DramaStatusAwaitingPublish && d.Status != model.DramaStatusOffline {
		response.Conflict(c, "当前剧目状态不可上架：仅待发布或已下架状态可上架")
		return
	}

	var readyCount int64
	s.db.Model(&model.Episode{}).
		Where("drama_id = ? AND status = ?", d.ID, model.EpisodeStatusReady).
		Count(&readyCount)
	if readyCount == 0 {
		response.InvalidParam(c, "至少需要 1 集 ready 状态剧集才能上架")
		return
	}
	var totalEpisodes int64
	s.db.Model(&model.Episode{}).Where("drama_id = ?", d.ID).Count(&totalEpisodes)
	if totalEpisodes > int64(d.FreeEpisodes) && d.PriceCents <= 0 {
		response.InvalidParam(c, "存在付费剧集时 price_cents 必须大于 0")
		return
	}

	now := time.Now()
	updates := map[string]interface{}{
		"status":       model.DramaStatusPublished,
		"published_at": now,
	}
	if err := s.db.Model(d).Updates(updates).Error; err != nil {
		response.ServerError(c, "上架失败")
		return
	}
	var fresh model.Drama
	s.db.First(&fresh, id)
	response.OK(c, dramaAdminView(fresh, s.nameOfCategory(fresh.CategoryID), s.nameOfCreator(fresh.CreatorID)))
}

// creatorDramaPublishConfigRequest —— 发布配置精简载荷。
// 发布配置步骤只填两项：发布类型 + 计划发布时间，其它申报字段在「基本信息」步骤里维护。
type creatorDramaPublishConfigRequest struct {
	PublishType        *string    `json:"publish_type"`         // self/platform
	ScheduledPublishAt *time.Time `json:"scheduled_publish_at"` // 计划发布时间
}

// creatorUpdateDramaPublishConfig —— 填写 / 修改发布配置。
// 只接收发布类型 + 计划发布时间，不动其它字段；用于发布向导第 3 步「填写发布配置」。
// 路径：PUT /v1/creator/dramas/:id/publish-config
func (s *Server) creatorUpdateDramaPublishConfig(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	d, ok := s.requireCreatorOwnsDrama(c, id)
	if !ok {
		return
	}

	var req creatorDramaPublishConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}
	if req.PublishType == nil || *req.PublishType == "" {
		response.InvalidParam(c, "publish_type 必填")
		return
	}
	if !model.ValidPublishType(*req.PublishType) {
		response.InvalidParam(c, "publish_type 只能是 self/platform")
		return
	}
	if req.ScheduledPublishAt == nil {
		response.InvalidParam(c, "scheduled_publish_at 必填")
		return
	}

	updates := map[string]interface{}{
		"publish_type":         *req.PublishType,
		"scheduled_publish_at": req.ScheduledPublishAt,
	}
	if err := s.db.Model(d).Updates(updates).Error; err != nil {
		response.ServerError(c, "保存发布配置失败")
		return
	}
	var fresh model.Drama
	s.db.First(&fresh, id)
	view := dramaAdminView(fresh, s.nameOfCategory(fresh.CategoryID), s.nameOfCreator(fresh.CreatorID))
	view["covers"], view["characters"] = s.loadDramaExtras(fresh.ID)
	response.OK(c, view)
}

// creatorSubmitDrama —— 创作者提交审核。
// 置 status=reviewing；并为该剧自动生成一份关联的 demo 合同（若尚无），合同名即剧名（视图按 drama_title 展示）。
// MVP 从宽：不强制必须有剧集，先把"提交 → 审核 → 合同"流程跑通。
func (s *Server) creatorSubmitDrama(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	d, ok := s.requireCreatorOwnsDrama(c, id)
	if !ok {
		return
	}
	// 允许提交的入口：draft 或 offline。
	// 被驳回的剧 status 已经在 draft（admin reject 时不动 draft，本身就在 draft），
	// 重新提审从 draft 进入即可——靠 audit_status 区分"首次提交" vs "驳回后再提"。
	if d.Status != model.DramaStatusDraft && d.Status != model.DramaStatusOffline {
		response.Conflict(c, "仅草稿 / 已下架状态可提交审核")
		return
	}
	if d.CreatorID == nil {
		response.ServerError(c, "短剧未绑定创作者")
		return
	}
	creatorID := *d.CreatorID
	dramaID := d.ID

	var contractCreated bool
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 提交审核：status 保持 draft（status 字段不混审核语义）；audit_status → pending；清掉历史驳回痕迹。
		if err := tx.Model(d).Updates(map[string]interface{}{
			"audit_status": model.DramaAuditPending,
			"audit_reason": "",
			"reviewer_id":  nil,
			"reviewed_at":  nil,
		}).Error; err != nil {
			return err
		}
		// 自动生成关联合同（幂等：该剧+创作者已有合同则跳过）。
		var cnt int64
		if err := tx.Model(&model.Contract{}).
			Where("drama_id = ? AND creator_id = ?", dramaID, creatorID).
			Count(&cnt).Error; err != nil {
			return err
		}
		if cnt == 0 {
			ct := model.Contract{
				CreatorID:  creatorID,
				DramaID:    &dramaID,
				ContractNo: generateContractNo(),
				Status:     model.ContractStatusPending,
			}
			if err := tx.Create(&ct).Error; err != nil {
				return err
			}
			contractCreated = true
		}
		return nil
	})
	if err != nil {
		response.ServerError(c, "提交审核失败")
		return
	}

	msg := "您的作品《" + d.Title + "》已提交审核。"
	if contractCreated {
		msg += "系统已为该作品生成关联合同，请在合同中查看。"
	}
	s.sendNotification(creatorID, "作品已提交审核", msg, "")

	var fresh model.Drama
	s.db.First(&fresh, id)
	view := dramaAdminView(fresh, s.nameOfCategory(fresh.CategoryID), s.nameOfCreator(fresh.CreatorID))
	view["covers"], view["characters"] = s.loadDramaExtras(fresh.ID)
	response.OK(c, view)
}

// creatorOfflineDrama —— 创作者自助下架，只动 status 不动 audit_status。
func (s *Server) creatorOfflineDrama(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	d, ok := s.requireCreatorOwnsDrama(c, id)
	if !ok {
		return
	}
	if err := s.db.Model(d).Update("status", model.DramaStatusOffline).Error; err != nil {
		response.ServerError(c, "下架失败")
		return
	}
	var fresh model.Drama
	s.db.First(&fresh, id)
	response.OK(c, dramaAdminView(fresh, s.nameOfCategory(fresh.CategoryID), s.nameOfCreator(fresh.CreatorID)))
}
