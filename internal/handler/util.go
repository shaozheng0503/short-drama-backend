package handler

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"ai-drama-platform/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

func isNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

func paginate(c *gin.Context) (page, pageSize int) {
	page, _ = strconv.Atoi(c.Query("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ = strconv.Atoi(c.Query("page_size"))
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return page, pageSize
}

func parseUint(s string) uint64 {
	n, _ := strconv.ParseUint(s, 10, 64)
	return n
}

// parseInt64 解析 int64（金额等可能为负/超出 uint64 的场景；解析失败返 0）。
func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// parseUintList 解析逗号分隔的正整数列表（去重、忽略 0/非法项）。
func parseUintList(s string) []uint64 {
	out := make([]uint64, 0)
	seen := map[uint64]bool{}
	for _, part := range strings.Split(s, ",") {
		if v := parseUint(strings.TrimSpace(part)); v > 0 && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func nowTimePtr() *time.Time {
	t := time.Now()
	return &t
}

// dramaIDFromPath 兼容 :drama_id / :id 两种命名（路由分组里都可能用）。
func dramaIDFromPath(c *gin.Context) uint64 {
	if v := c.Param("drama_id"); v != "" {
		return parseUint(v)
	}
	return parseUint(c.Param("id"))
}

func pageResp(list interface{}, page, pageSize int, total int64) gin.H {
	hasMore := int64(page*pageSize) < total
	return gin.H{
		"list":      list,
		"page":      page,
		"page_size": pageSize,
		"total":     total,
		"has_more":  hasMore,
	}
}

// freeEpisodes 由调用方传入"当前生效"的免费集数（统一走全局配置，见 effectiveFreeEpisodes），
// 不再直接读 d.FreeEpisodes，避免卡片展示与播放/计费判定口径不一致。
func dramaCardView(d model.Drama, freeEpisodes int) gin.H {
	return gin.H{
		"id":             d.ID,
		"title":          d.Title,
		"description":    d.Description,
		"cover_url":      d.CoverURL,
		"total_episodes": d.TotalEpisodes,
		"free_episodes":  freeEpisodes,
		"play_count":     d.PlayCount,
		"like_count":     d.LikeCount,
		"share_count":    d.ShareCount,
	}
}

func dramaAdminView(d model.Drama, categoryName, creatorName string) gin.H {
	view := gin.H{
		"id":             d.ID,
		"title":          d.Title,
		"description":    d.Description,
		"cover_url":      d.CoverURL,
		"category_id":    d.CategoryID,
		"category_name":  categoryName,
		"creator_id":     d.CreatorID,
		"creator_name":   creatorName,
		"total_episodes": d.TotalEpisodes,
		"free_episodes":  d.FreeEpisodes,
		"price_cents":    d.PriceCents,
		"sort_order":     d.SortOrder,
		"status":         d.Status,
		"audit_status":   d.AuditStatus, // 派生总状态：资料✓且视频✓才 approved
		"audit_reason":   d.AuditReason,
		// 分批审核维度（资料内容 / 视频内容），各带状态 + 备注；前端可展示"哪项没过、原因"
		"content_audit_status": d.ContentAuditStatus,
		"content_audit_reason": d.ContentAuditReason,
		"video_audit_status":   d.VideoAuditStatus,
		"video_audit_reason":   d.VideoAuditReason,
		"audit_submitted_at":   d.AuditSubmittedAt,
		"reviewer_id":          d.ReviewerID,
		"reviewed_at":          d.ReviewedAt,
		// 申报级扩展字段
		"is_ai":                 d.IsAI,
		"aigc_tools":            d.AIGCTools,
		"language_id":           d.LanguageID,
		"audience":              d.Audience,
		"alias_paid":            d.AliasPaid,
		"alias_free":            d.AliasFree,
		"production_org":        d.ProductionOrg,
		"producer":              d.Producer,
		"director":              d.Director,
		"screenwriter":          d.Screenwriter,
		"production_cost_cents": d.ProductionCostCents,
		"cost_config_url":       d.CostConfigURL,
		"is_ip_adaptation":      d.IsIPAdaptation,
		// 2026-07-03 改：版权证明多张图
		// nil/空时返回空数组（前端用空兜底，老的单图 URL 已迁移到 []string 字段）
		"copyright_file_urls":   d.CopyrightFileURLs,
		"non_infringement_url":  d.NonInfringementURL,
		"publish_type":          d.PublishType,
		"scheduled_publish_at":  d.ScheduledPublishAt,
		"play_count":            d.PlayCount,
		"like_count":            d.LikeCount,
		"favorite_count":        d.FavoriteCount,
		"share_count":           d.ShareCount,
		"published_at":          d.PublishedAt,
		"created_at":            d.CreatedAt,
		"updated_at":            d.UpdatedAt,
	}
	return view
}

func episodeAdminView(e model.Episode) gin.H {
	return gin.H{
		"id":               e.ID,
		"drama_id":         e.DramaID,
		"episode_no":       e.EpisodeNo,
		"title":            e.Title,
		"vod_file_id":      e.VODFileID,
		"video_url":        e.VideoURL,
		"duration_seconds": e.DurationSeconds,
		"status":           e.Status,
		"vod_synced_at":    e.VODSyncedAt, // v0.13.1：最近一次主动同步 VOD 的时间
		"created_at":       e.CreatedAt,
		"updated_at":       e.UpdatedAt,
	}
}

func episodeAppView(e model.Episode, freeEpisodes int, unlocked, liked bool, commentCount int64) gin.H {
	isFree := e.EpisodeNo <= freeEpisodes
	return gin.H{
		"id":               e.ID,
		"episode_no":       e.EpisodeNo,
		"title":            e.Title,
		"duration_seconds": e.DurationSeconds,
		"is_free":          isFree,
		"is_locked":        !isFree && !unlocked,
		"like_count":       e.LikeCount,
		"liked":            liked,
		"comment_count":    commentCount,
	}
}

func categoryView(c model.Category) gin.H {
	return gin.H{
		"id":         c.ID,
		"type":       c.Type,
		"name":       c.Name,
		"sort_order": c.SortOrder,
		"status":     c.Status,
	}
}
