package handler

import (
	"strings"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

type channelAccountRequest struct {
	Platform    string `json:"platform"`
	AccountUID  string `json:"account_uid"`
	Nickname    string `json:"nickname"`
	AvatarURL   string `json:"avatar_url"`
	HomepageURL string `json:"homepage_url"`
	Status      string `json:"status"`
}

func (s *Server) creatorListChannelAccounts(c *gin.Context) {
	cid := middleware.CurrentID(c)
	var items []model.CreatorChannelAccount
	q := s.db.Where("creator_id = ?", cid)
	if platform := strings.TrimSpace(c.Query("platform")); platform != "" {
		q = q.Where("platform = ?", platform)
	}
	if err := q.Order("created_at desc").Find(&items).Error; err != nil {
		response.ServerError(c, "查询渠道账号失败")
		return
	}
	list := make([]gin.H, 0, len(items))
	for _, item := range items {
		list = append(list, channelAccountView(item))
	}
	response.OK(c, gin.H{"list": list})
}

func (s *Server) creatorCreateChannelAccount(c *gin.Context) {
	cid := middleware.CurrentID(c)
	var req channelAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "platform 与 account_uid 必填")
		return
	}
	if msg := validateChannelAccountRequest(req, true); msg != "" {
		response.InvalidParam(c, msg)
		return
	}
	status := req.Status
	if status == "" {
		status = model.StatusActive
	}
	item := model.CreatorChannelAccount{
		CreatorID:   cid,
		Platform:    strings.TrimSpace(req.Platform),
		AccountUID:  strings.TrimSpace(req.AccountUID),
		Nickname:    strings.TrimSpace(req.Nickname),
		AvatarURL:   strings.TrimSpace(req.AvatarURL),
		HomepageURL: strings.TrimSpace(req.HomepageURL),
		Status:      status,
	}
	if err := s.db.Create(&item).Error; err != nil {
		if isUniqueViolation(err) {
			response.Conflict(c, "该渠道账号已绑定")
			return
		}
		response.ServerError(c, "创建渠道账号失败")
		return
	}
	response.OK(c, channelAccountView(item))
}

func (s *Server) creatorUpdateChannelAccount(c *gin.Context) {
	cid := middleware.CurrentID(c)
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var item model.CreatorChannelAccount
	if err := s.db.Where("id = ? AND creator_id = ?", id, cid).First(&item).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "渠道账号不存在")
			return
		}
		response.ServerError(c, "查询渠道账号失败")
		return
	}
	var req channelAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}
	if msg := validateChannelAccountRequest(req, false); msg != "" {
		response.InvalidParam(c, msg)
		return
	}
	updates := map[string]interface{}{}
	if req.Platform != "" {
		updates["platform"] = strings.TrimSpace(req.Platform)
	}
	if req.AccountUID != "" {
		updates["account_uid"] = strings.TrimSpace(req.AccountUID)
	}
	if req.Nickname != "" {
		updates["nickname"] = strings.TrimSpace(req.Nickname)
	}
	if req.AvatarURL != "" {
		updates["avatar_url"] = strings.TrimSpace(req.AvatarURL)
	}
	if req.HomepageURL != "" {
		updates["homepage_url"] = strings.TrimSpace(req.HomepageURL)
	}
	if req.Status != "" {
		updates["status"] = req.Status
	}
	if len(updates) > 0 {
		if err := s.db.Model(&item).Updates(updates).Error; err != nil {
			if isUniqueViolation(err) {
				response.Conflict(c, "该渠道账号已绑定")
				return
			}
			response.ServerError(c, "更新渠道账号失败")
			return
		}
	}
	s.db.First(&item, id)
	response.OK(c, channelAccountView(item))
}

func (s *Server) creatorDeleteChannelAccount(c *gin.Context) {
	cid := middleware.CurrentID(c)
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	res := s.db.Where("id = ? AND creator_id = ?", id, cid).Delete(&model.CreatorChannelAccount{})
	if res.Error != nil {
		response.ServerError(c, "删除渠道账号失败")
		return
	}
	if res.RowsAffected == 0 {
		response.NotFound(c, "渠道账号不存在")
		return
	}
	response.OK(c, gin.H{"id": id, "deleted": true})
}

func (s *Server) adminListCreatorChannelAccounts(c *gin.Context) {
	page, pageSize := paginate(c)
	q := s.db.Model(&model.CreatorChannelAccount{})
	if creatorID := parseUint(c.Query("creator_id")); creatorID > 0 {
		q = q.Where("creator_id = ?", creatorID)
	}
	if platform := strings.TrimSpace(c.Query("platform")); platform != "" {
		q = q.Where("platform = ?", platform)
	}
	var total int64
	q.Count(&total)
	var items []model.CreatorChannelAccount
	if err := q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		response.ServerError(c, "查询渠道账号失败")
		return
	}
	list := make([]gin.H, 0, len(items))
	for _, item := range items {
		list = append(list, channelAccountView(item))
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

type adminChannelAccountRequest struct {
	CreatorID uint64 `json:"creator_id"`
	channelAccountRequest
}

func (s *Server) adminCreateChannelAccount(c *gin.Context) {
	var req adminChannelAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "creator_id、platform 与 account_uid 必填")
		return
	}
	if req.CreatorID == 0 {
		response.InvalidParam(c, "creator_id 必填")
		return
	}
	if msg := validateChannelAccountRequest(req.channelAccountRequest, true); msg != "" {
		response.InvalidParam(c, msg)
		return
	}
	var cnt int64
	if err := s.db.Model(&model.Creator{}).Where("id = ?", req.CreatorID).Count(&cnt).Error; err != nil || cnt == 0 {
		response.NotFound(c, "创作者不存在")
		return
	}
	status := req.Status
	if status == "" {
		status = model.StatusActive
	}
	item := model.CreatorChannelAccount{
		CreatorID:   req.CreatorID,
		Platform:    strings.TrimSpace(req.Platform),
		AccountUID:  strings.TrimSpace(req.AccountUID),
		Nickname:    strings.TrimSpace(req.Nickname),
		AvatarURL:   strings.TrimSpace(req.AvatarURL),
		HomepageURL: strings.TrimSpace(req.HomepageURL),
		Status:      status,
	}
	if err := s.db.Create(&item).Error; err != nil {
		if isUniqueViolation(err) {
			response.Conflict(c, "该渠道账号已绑定")
			return
		}
		response.ServerError(c, "创建渠道账号失败")
		return
	}
	response.OK(c, channelAccountView(item))
}

func (s *Server) adminUpdateChannelAccount(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var item model.CreatorChannelAccount
	if err := s.db.First(&item, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "渠道账号不存在")
			return
		}
		response.ServerError(c, "查询渠道账号失败")
		return
	}
	var req channelAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}
	if msg := validateChannelAccountRequest(req, false); msg != "" {
		response.InvalidParam(c, msg)
		return
	}
	updates := map[string]interface{}{}
	if req.Platform != "" {
		updates["platform"] = strings.TrimSpace(req.Platform)
	}
	if req.AccountUID != "" {
		updates["account_uid"] = strings.TrimSpace(req.AccountUID)
	}
	if req.Nickname != "" {
		updates["nickname"] = strings.TrimSpace(req.Nickname)
	}
	if req.AvatarURL != "" {
		updates["avatar_url"] = strings.TrimSpace(req.AvatarURL)
	}
	if req.HomepageURL != "" {
		updates["homepage_url"] = strings.TrimSpace(req.HomepageURL)
	}
	if req.Status != "" {
		updates["status"] = req.Status
	}
	if len(updates) == 0 {
		response.OK(c, channelAccountView(item))
		return
	}
	if err := s.db.Model(&item).Updates(updates).Error; err != nil {
		if isUniqueViolation(err) {
			response.Conflict(c, "该渠道账号已绑定")
			return
		}
		response.ServerError(c, "更新渠道账号失败")
		return
	}
	s.db.First(&item, id)
	response.OK(c, channelAccountView(item))
}

func (s *Server) adminDeleteChannelAccount(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	res := s.db.Delete(&model.CreatorChannelAccount{}, id)
	if res.Error != nil {
		response.ServerError(c, "删除渠道账号失败")
		return
	}
	if res.RowsAffected == 0 {
		response.NotFound(c, "渠道账号不存在")
		return
	}
	response.OK(c, gin.H{"id": id, "deleted": true})
}

func validateChannelAccountRequest(req channelAccountRequest, create bool) string {
	if create && strings.TrimSpace(req.Platform) == "" {
		return "platform 必填"
	}
	if create && strings.TrimSpace(req.AccountUID) == "" {
		return "account_uid 必填"
	}
	if req.Platform != "" && len(req.Platform) > 32 {
		return "platform 过长"
	}
	if req.AccountUID != "" && len(req.AccountUID) > 128 {
		return "account_uid 过长"
	}
	if req.Nickname != "" && runeLen(req.Nickname) > 50 {
		return "nickname 长度不能超过 50 个字符"
	}
	if req.AvatarURL != "" && len(req.AvatarURL) > 512 {
		return "avatar_url 过长"
	}
	if req.HomepageURL != "" && len(req.HomepageURL) > 512 {
		return "homepage_url 过长"
	}
	if req.Status != "" && req.Status != model.StatusActive && req.Status != model.StatusDisabled {
		return "status 只能是 active / disabled"
	}
	return ""
}

func channelAccountView(item model.CreatorChannelAccount) gin.H {
	return gin.H{
		"id":           item.ID,
		"creator_id":   item.CreatorID,
		"platform":     item.Platform,
		"account_uid":  item.AccountUID,
		"nickname":     item.Nickname,
		"avatar_url":   item.AvatarURL,
		"homepage_url": item.HomepageURL,
		"status":       item.Status,
		"created_at":   item.CreatedAt,
		"updated_at":   item.UpdatedAt,
	}
}
