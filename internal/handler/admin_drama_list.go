package handler

import (
	"ai-drama-platform/internal/model"

	"github.com/gin-gonic/gin"
)

// adminDramaListItemView 管理中台漫剧列表专用视图（比 dramaAdminView 更贴近表格列）。
func adminDramaListItemView(
	d model.Drama,
	categoryName, creatorName string,
	categories []gin.H,
	publishAccounts []gin.H,
	contractStatus string,
) gin.H {
	auditStatus := d.AuditStatus
	if auditStatus == "" {
		auditStatus = model.DramaAuditApproved
	}
	// 草稿状态：审核维度显示 not_submitted，避免前端误显示"审核中"
	if d.Status == model.DramaStatusDraft {
		auditStatus = "not_submitted"
	}
	contractAudit := contractStatusToAudit(contractStatus)

	publishStatus := "unpublished"
	if d.Status == model.DramaStatusPublished {
		publishStatus = "published"
	}

	canDelete := d.Status == model.DramaStatusDraft

	return gin.H{
		"id":             d.ID,
		"title":          d.Title,
		"cover_url":      d.CoverURL,
		"total_episodes": d.TotalEpisodes,

		"audit_status":       auditStatus,
		"audit_reason":       d.AuditReason,
		"audit_submitted_at": d.AuditSubmittedAt,
		"reviewer_id":        d.ReviewerID,
		"reviewed_at":        d.ReviewedAt,

		"publish_accounts": publishAccounts,

		"publish_status": publishStatus,
		"status":         d.Status,

		// 2026-08-24 加：看广告解锁开关（列表也返回，管理端列表可直接展示/筛选）
		"ad_unlock_enabled": d.AdUnlockEnabled != nil && *d.AdUnlockEnabled,

		"created_at": d.CreatedAt,
		"audience":   d.Audience,

		"contract_status":       contractStatus,
		"contract_audit_status": contractAudit,
		"contract_audit_reason": contractRejectReason(contractStatus),

		"category_id":   d.CategoryID,
		"category_name": categoryName,
		"categories":    categories,

		"creator_id":   d.CreatorID,
		"creator_name": creatorName,

		"can_delete": canDelete,
		"can_edit":   true,
	}
}

func contractStatusToAudit(status string) string {
	switch status {
	case model.ContractStatusSigned:
		return model.DramaAuditApproved
	case model.ContractStatusCancelled:
		return model.DramaAuditRejected
	case model.ContractStatusPending, model.ContractStatusSigning:
		return model.DramaAuditPending
	default:
		return ""
	}
}

func contractRejectReason(status string) string {
	if status == model.ContractStatusCancelled {
		return "合同已作废"
	}
	return ""
}

func (s *Server) collectDramaCategories(dramaIDs []uint64) map[uint64][]gin.H {
	out := map[uint64][]gin.H{}
	if len(dramaIDs) == 0 {
		return out
	}
	var rows []struct {
		DramaID    uint64
		CategoryID uint64
		Name       string
		Type       string
	}
	s.db.Table("drama_tags").
		Select("drama_tags.drama_id, categories.id as category_id, categories.name, categories.type").
		Joins("JOIN categories ON categories.id = drama_tags.category_id").
		Where("drama_tags.drama_id IN ?", dramaIDs).
		Order("categories.type asc, categories.sort_order asc").
		Scan(&rows)
	for _, r := range rows {
		out[r.DramaID] = append(out[r.DramaID], gin.H{
			"id":   r.CategoryID,
			"name": r.Name,
			"type": r.Type,
		})
	}
	return out
}

func (s *Server) collectDramaContractStatus(dramaIDs []uint64) map[uint64]string {
	out := map[uint64]string{}
	if len(dramaIDs) == 0 {
		return out
	}
	var rows []struct {
		DramaID uint64
		Status  string
	}
	s.db.Table("contracts").
		Select("drama_id, status").
		Where("drama_id IN ?", dramaIDs).
		Order("updated_at desc").
		Scan(&rows)
	for _, r := range rows {
		if _, ok := out[r.DramaID]; !ok {
			out[r.DramaID] = r.Status
		}
	}
	return out
}

func (s *Server) collectCreatorPublishAccounts(creatorIDs []uint64) map[uint64][]gin.H {
	out := map[uint64][]gin.H{}
	if len(creatorIDs) == 0 {
		return out
	}
	var accounts []model.CreatorChannelAccount
	s.db.Where("creator_id IN ? AND status = ?", creatorIDs, model.StatusActive).
		Order("platform asc, id asc").
		Find(&accounts)
	for _, a := range accounts {
		out[a.CreatorID] = append(out[a.CreatorID], gin.H{
			"id":           a.ID,
			"platform":     a.Platform,
			"account_uid":  a.AccountUID,
			"nickname":     a.Nickname,
			"avatar_url":   a.AvatarURL,
			"homepage_url": a.HomepageURL,
		})
	}
	return out
}
