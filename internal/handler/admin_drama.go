package handler

import (
	"encoding/json"
	"strings"
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type dramaUpsertRequest struct {
	Title        *string   `json:"title"`
	Description  *string   `json:"description"`
	CoverURL     *string   `json:"cover_url"`
	CategoryID   *uint64   `json:"category_id"`
	CreatorID    *uint64   `json:"creator_id"`
	FreeEpisodes *int      `json:"free_episodes"`
	PriceCents   *int64    `json:"price_cents"`
	SortOrder    *int      `json:"sort_order"`
	// 2026-07-03 加：权属文件多张图（与创作者端对齐）
	// null = 不改；[] = 清空；[url1,url2,...] = 整体替换（最多 10 张）
	CopyrightFileURLs *[]string `json:"copyright_file_urls"`
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
	// audit_status 过滤：与 status 字段独立——中台「是否通过审核」筛选用这个。
	// 取值：pending / approved / rejected；非法值直接 400，避免静默返空。
	if v := c.Query("audit_status"); v != "" {
		switch v {
		case model.DramaAuditPending:
			// 草稿(未提交)与"已提交待审"都是 audit_status=pending，靠 status 区分。
			// 「待审核」队列只要真正提交过的(status=reviewing)，排除未提交草稿，避免污染审核列表。
			q = q.Where("audit_status = ? AND status <> ?", v, model.DramaStatusDraft)
		case model.DramaAuditApproved, model.DramaAuditRejected:
			q = q.Where("audit_status = ?", v)
		default:
			response.InvalidParam(c, "audit_status 只能是 pending/approved/rejected")
			return
		}
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
	orderClause := "updated_at desc"
	if v := c.Query("audit_status"); v == model.DramaAuditPending {
		orderClause = "audit_submitted_at desc NULLS LAST, updated_at desc"
	}
	var list []model.Drama
	if err := q.Order(orderClause).
		Offset((page - 1) * pageSize).Limit(pageSize).
		Find(&list).Error; err != nil {
		response.ServerError(c, "查询短剧失败")
		return
	}

	views := make([]gin.H, 0, len(list))
	cats := s.collectCategoryNames(list)
	crts := s.collectCreatorNames(list)

	dramaIDs := make([]uint64, 0, len(list))
	creatorIDs := make([]uint64, 0)
	creatorSeen := map[uint64]bool{}
	for _, d := range list {
		dramaIDs = append(dramaIDs, d.ID)
		if d.CreatorID != nil && !creatorSeen[*d.CreatorID] {
			creatorIDs = append(creatorIDs, *d.CreatorID)
			creatorSeen[*d.CreatorID] = true
		}
	}
	categoriesByDrama := s.collectDramaCategories(dramaIDs)
	contractByDrama := s.collectDramaContractStatus(dramaIDs)
	publishAccountsByCreator := s.collectCreatorPublishAccounts(creatorIDs)

	// 本页各剧标题在全表是否重名，给运营标记（剧名非唯一，财务/统计需用短剧ID区分）。
	titles := make([]string, 0, len(list))
	for _, d := range list {
		titles = append(titles, d.Title)
	}
	dupTitleCount := s.collectDuplicateTitleCounts(titles)

	for _, d := range list {
		categoryName := ""
		if d.CategoryID != nil {
			categoryName = cats[*d.CategoryID]
		}
		creatorName := ""
		var publishAccounts []gin.H
		if d.CreatorID != nil {
			creatorName = crts[*d.CreatorID]
			publishAccounts = publishAccountsByCreator[*d.CreatorID]
		}
		if publishAccounts == nil {
			publishAccounts = []gin.H{}
		}
		categories := categoriesByDrama[d.ID]
		if categories == nil {
			categories = []gin.H{}
		}
		view := adminDramaListItemView(
			d, categoryName, creatorName, categories, publishAccounts, contractByDrama[d.ID],
		)
		// 同名总数>1 才标记；附同名总数，前端可提示「N 部同名」。
		if n := dupTitleCount[d.Title]; n > 1 {
			view["has_duplicate_title"] = true
			view["duplicate_title_count"] = n
		} else {
			view["has_duplicate_title"] = false
		}
		views = append(views, view)
	}
	response.OK(c, pageResp(views, page, pageSize, total))
}

// collectDuplicateTitleCounts 批量统计给定标题在全表的出现次数，只返回出现 >1 的（重名）。
func (s *Server) collectDuplicateTitleCounts(titles []string) map[string]int {
	out := map[string]int{}
	if len(titles) == 0 {
		return out
	}
	var rows []struct {
		Title string
		Cnt   int
	}
	s.db.Model(&model.Drama{}).
		Select("title, count(*) as cnt").
		Where("title IN ?", titles).
		Group("title").
		Having("count(*) > 1").
		Scan(&rows)
	for _, r := range rows {
		out[r.Title] = r.Cnt
	}
	return out
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
	// 2026-07-03 加：权属文件多图上限校验（与创作者端同口径）
	if req.CopyrightFileURLs != nil && len(*req.CopyrightFileURLs) > 10 {
		response.InvalidParam(c, "权属文件最多 10 张")
		return
	}
	drama := model.Drama{
		Title:        *req.Title,
		Status:       model.DramaStatusDraft,
		FreeEpisodes: 0,
		PriceCents:   0,
		// 分维度状态初始化为 pending，与 audit_status 默认值一致，避免中台分维度审核列显示成空（—）。
		AuditStatus:        model.DramaAuditPending,
		ContentAuditStatus: model.DramaAuditPending,
		VideoAuditStatus:   model.DramaAuditPending,
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
	// 2026-07-03 加：权属文件多图（admin 端创建时支持）
	// Create 路径走 struct + serializer:json，直接赋值 []string 即可
	if req.CopyrightFileURLs != nil {
		drama.CopyrightFileURLs = *req.CopyrightFileURLs
	}
	if err := s.db.Create(&drama).Error; err != nil {
		response.ServerError(c, "创建短剧失败")
		return
	}
	view := dramaAdminView(drama, s.nameOfCategory(drama.CategoryID), s.nameOfCreator(drama.CreatorID))
	s.attachTitleDuplicateWarning(view, drama.Title, drama.ID)
	response.OK(c, view)
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
	// 2026-07-06 加：管理中台剧详情也补 covers / characters（之前只创作者端详情有，
	// 导致管理中台点开剧详情只看到 cover_url 一张图，看不到 drama_covers 多图封面）
	view["covers"], view["characters"] = s.loadDramaExtras(drama.ID)
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
	// 2026-07-03 加：权属文件多图上限校验（与创作者端同口径，update 路径也要拦）
	if req.CopyrightFileURLs != nil && len(*req.CopyrightFileURLs) > 10 {
		response.InvalidParam(c, "权属文件最多 10 张")
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
	// 2026-07-03 加：权属文件多图（admin 端可直接改）
	// null = 不改；[] = 清空；[url1,url2,...] = 整体替换
	// GORM serializer:json 只管 SELECT 反序列化，UPDATE 写入需要手动 marshal 成 JSON 字符串
	if req.CopyrightFileURLs != nil {
		b, _ := json.Marshal(*req.CopyrightFileURLs)
		updates["copyright_file_urls"] = string(b)
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
		// 一键整体驳回：两维度同置驳回(同一原因)，与派生总状态一致；细分驳回走 /audit。
		"content_audit_status": model.DramaAuditRejected,
		"content_audit_reason": req.Reason,
		"video_audit_status":   model.DramaAuditRejected,
		"video_audit_reason":   req.Reason,
		"reviewer_id":          reviewerID,
		"reviewed_at":          now,
	}
	// 驳回时的 status 反推：
	//   - published        → offline（已上线 + 驳回必须立即下架）
	//   - awaiting_publish → draft（撤回已通过的审核，剧从发布队列退出）
	//   - draft / offline 不动
	switch drama.Status {
	case model.DramaStatusPublished:
		updates["status"] = model.DramaStatusOffline
	case model.DramaStatusAwaitingPublish, model.DramaStatusReviewing:
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

// adminApproveDrama —— 管理员审核通过。
// 推进 status 进发布队列：draft / offline → awaiting_publish；已经在 awaiting_publish
// 或 published 状态的不动（幂等再审）。具体的上架动作由创作者 / admin 显式 publish。
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
		// 一键整体通过：资料 + 视频两维度同时置通过，与派生总状态一致。
		"content_audit_status": model.DramaAuditApproved,
		"content_audit_reason": "",
		"video_audit_status":   model.DramaAuditApproved,
		"video_audit_reason":   "",
		"reviewer_id":          reviewerID,
		"reviewed_at":          now,
	}
	// 通过审核 → 进入"待上架"。已经在发布队列 / 已上架的不再动 status，保持幂等。
	if drama.Status == model.DramaStatusDraft || drama.Status == model.DramaStatusReviewing || drama.Status == model.DramaStatusOffline {
		updates["status"] = model.DramaStatusAwaitingPublish
	}
	// 审核通过只动短剧状态；合同不再在此自动置 signed —— 合同以管理员上传的签署版 PDF 为准。
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

// adminAuditDrama —— 分批审核：按维度(content=资料内容 / video=视频内容)分别通过/驳回。
// POST /admin/dramas/:id/audit  body {dimension, action(approve/reject), reason}
// 写入该维度后重算派生总状态(audit_status)：两维度全通过→approved→awaiting_publish+签约+通知；
// 任一驳回→rejected+状态回退+通知(带各维度原因)；否则 pending(继续待审)。合同维度本期未纳入派生。
type adminAuditDramaRequest struct {
	Dimension string `json:"dimension"`
	Action    string `json:"action"`
	Reason    string `json:"reason"`
}

func (s *Server) adminAuditDrama(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var req adminAuditDramaRequest
	_ = c.ShouldBindJSON(&req)
	if req.Dimension != model.DramaAuditDimensionContent && req.Dimension != model.DramaAuditDimensionVideo {
		response.InvalidParam(c, "dimension 只能是 content / video")
		return
	}
	var dimStatus string
	switch req.Action {
	case "approve":
		dimStatus = model.DramaAuditApproved
		req.Reason = "" // 通过不留原因
	case "reject":
		dimStatus = model.DramaAuditRejected
	default:
		response.InvalidParam(c, "action 只能是 approve / reject")
		return
	}
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

	dimUpdates := map[string]interface{}{}
	if req.Dimension == model.DramaAuditDimensionContent {
		drama.ContentAuditStatus, drama.ContentAuditReason = dimStatus, req.Reason
		dimUpdates["content_audit_status"], dimUpdates["content_audit_reason"] = dimStatus, req.Reason
	} else {
		drama.VideoAuditStatus, drama.VideoAuditReason = dimStatus, req.Reason
		dimUpdates["video_audit_status"], dimUpdates["video_audit_reason"] = dimStatus, req.Reason
	}

	var outcome dramaAuditOutcome
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Drama{}).Where("id = ?", id).Updates(dimUpdates).Error; err != nil {
			return err
		}
		o, err := s.recomputeDramaAuditTx(tx, &drama, reviewerID, now)
		if err != nil {
			return err
		}
		outcome = o
		return nil
	})
	if err != nil {
		response.ServerError(c, "审核失败")
		return
	}
	s.db.First(&drama, id)
	if outcome.NotifyTitle != "" && drama.CreatorID != nil {
		s.sendNotification(*drama.CreatorID, outcome.NotifyTitle, outcome.NotifyContent, "")
	}
	response.OK(c, dramaAdminView(drama, s.nameOfCategory(drama.CategoryID), s.nameOfCreator(drama.CreatorID)))
}

type dramaAuditOutcome struct {
	Overall       string
	NotifyTitle   string
	NotifyContent string
}

// recomputeDramaAuditTx 据 drama 的资料/视频维度状态重算总 audit_status，并在 tx 内推进 status + 合同。
// 调用前 drama 的 *AuditStatus/*AuditReason 须为最新值。空维度按 pending 处理。
func (s *Server) recomputeDramaAuditTx(tx *gorm.DB, drama *model.Drama, reviewerID uint64, now time.Time) (dramaAuditOutcome, error) {
	norm := func(v string) string {
		if v == "" {
			return model.DramaAuditPending
		}
		return v
	}
	content, video := norm(drama.ContentAuditStatus), norm(drama.VideoAuditStatus)

	var overall, reason string
	switch {
	case content == model.DramaAuditRejected || video == model.DramaAuditRejected:
		overall = model.DramaAuditRejected
		var parts []string
		if content == model.DramaAuditRejected {
			parts = append(parts, "资料："+orDefaultStr(drama.ContentAuditReason, "未通过"))
		}
		if video == model.DramaAuditRejected {
			parts = append(parts, "视频："+orDefaultStr(drama.VideoAuditReason, "未通过"))
		}
		reason = strings.Join(parts, "；")
	case content == model.DramaAuditApproved && video == model.DramaAuditApproved:
		overall = model.DramaAuditApproved
	default:
		overall = model.DramaAuditPending
	}

	updates := map[string]interface{}{
		"audit_status": overall,
		"audit_reason": reason,
		"reviewer_id":  reviewerID,
		"reviewed_at":  now,
	}
	switch overall {
	case model.DramaAuditApproved:
		if drama.Status == model.DramaStatusDraft || drama.Status == model.DramaStatusReviewing || drama.Status == model.DramaStatusOffline {
			updates["status"] = model.DramaStatusAwaitingPublish
		}
	case model.DramaAuditRejected:
		switch drama.Status {
		case model.DramaStatusPublished:
			updates["status"] = model.DramaStatusOffline
		case model.DramaStatusAwaitingPublish, model.DramaStatusReviewing:
			updates["status"] = model.DramaStatusDraft
		}
	}
	if err := tx.Model(&model.Drama{}).Where("id = ?", drama.ID).Updates(updates).Error; err != nil {
		return dramaAuditOutcome{}, err
	}

	out := dramaAuditOutcome{Overall: overall}
	switch overall {
	case model.DramaAuditApproved:
		// 审核通过不再自动置合同 signed —— 合同以管理员上传的签署版 PDF 为准。
		out.NotifyTitle = "作品审核通过"
		out.NotifyContent = "您的作品《" + drama.Title + "》已审核通过，可以发布上架。"
	case model.DramaAuditRejected:
		out.NotifyTitle = "作品审核未通过"
		out.NotifyContent = "您的作品《" + drama.Title + "》审核未通过，请修改后重新提交。"
		if reason != "" {
			out.NotifyContent += "驳回原因：" + reason
		}
	}
	return out, nil
}

func orDefaultStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
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

// duplicateTitleIDs 返回与 title 同名的其它短剧 id（排除 excludeID 自己）。
// 用于建剧后给出「重名告警」：剧名不是唯一键，财务/收益导入靠短剧ID 区分，重名需运营留意。
func (s *Server) duplicateTitleIDs(title string, excludeID uint64) []uint64 {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil
	}
	var ids []uint64
	s.db.Model(&model.Drama{}).Where("title = ? AND id <> ?", title, excludeID).
		Order("id").Limit(20).Pluck("id", &ids)
	return ids
}

// attachTitleDuplicateWarning 若有同名剧，往视图塞 title_duplicate_warning（不阻断创建）。
func (s *Server) attachTitleDuplicateWarning(view gin.H, title string, selfID uint64) {
	if dups := s.duplicateTitleIDs(title, selfID); len(dups) > 0 {
		view["title_duplicate_warning"] = gin.H{
			"message":   "已存在同名短剧，剧名非唯一标识；财务收益导入 / 统计请用「短剧ID」区分。",
			"drama_ids": dups,
		}
	}
}

func (s *Server) creatorExists(id uint64) bool {
	var cnt int64
	s.db.Model(&model.Creator{}).Where("id = ?", id).Count(&cnt)
	return cnt > 0
}
