package handler

import (
	"strconv"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

// GET /v1/publisher/dramas —— 剧集广场列表
func (s *Server) publisherListDramas(c *gin.Context) {
	page, pageSize := paginate(c)
	q := s.db.Model(&model.Drama{}).Where("status = ?", "published")
	if v := c.Query("keyword"); v != "" {
		q = q.Where("title ILIKE ?", "%"+v+"%")
	}
	if v := c.Query("platform"); v != "" {
		// 筛选支持该平台的剧——暂不实现复杂筛选，返回全部
		_ = v
	}
	var total int64
	q.Count(&total)
	var dramas []model.Drama
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&dramas)

	// 查已发行 + 审核中认领占用的平台
	dramaIDs := make([]uint64, 0, len(dramas))
	for _, d := range dramas {
		dramaIDs = append(dramaIDs, d.ID)
	}
	occupiedPlatforms := map[uint64]map[string]bool{}
	if len(dramaIDs) > 0 {
		// 已发行
		var dds []model.DistributorDrama
		s.db.Where("drama_id IN ? AND status IN ?", dramaIDs, []string{"authorized", "active"}).Find(&dds)
		for _, dd := range dds {
			if occupiedPlatforms[dd.DramaID] == nil {
				occupiedPlatforms[dd.DramaID] = map[string]bool{}
			}
			for _, p := range parsePlatforms(dd.Platforms) {
				occupiedPlatforms[dd.DramaID][p] = true
			}
		}
		// 审核中的认领
		activeClaimStatuses := []string{
			model.ClaimDepositPending, model.ClaimAuthPending,
			model.ClaimReviewPending, model.ClaimContractPending,
		}
		var apps []model.DistributorApplication
		s.db.Where("drama_id IN ? AND status IN ?", dramaIDs, activeClaimStatuses).Find(&apps)
		for _, app := range apps {
			if occupiedPlatforms[app.DramaID] == nil {
				occupiedPlatforms[app.DramaID] = map[string]bool{}
			}
			for _, p := range parsePlatforms(app.Platforms) {
				occupiedPlatforms[app.DramaID][p] = true
			}
		}
	}

	list := make([]gin.H, 0, len(dramas))
	distID := middleware.CurrentID(c)
	verified := s.isDistributorVerified(distID)
	for _, d := range dramas {
		occupied := occupiedPlatforms[d.ID]
		if occupied == nil {
			occupied = map[string]bool{}
		}
		available := getAvailablePlatformsFromOccupied(occupied)
		// claimable 只表示剧集本身是否可认领（已上架 + 有可发行平台 + 开放发行）
		// 发行商认证状态单独通过 can_claim 表示，前端据此提示"请先认证"
		distributable := d.Status == "published" && len(available) > 0 && isDistributable(d)
		list = append(list, gin.H{
			"id":                  d.ID,
			"title":               d.Title,
			"cover_url":           d.CoverURL,
			"episode_count":       d.TotalEpisodes,
			"price_cents":         d.PriceCents,
			"available_platforms": available,
			"released_platforms":  occupiedKeys(occupied),
			"claimable":           distributable,
			"can_claim":           distributable && verified,
		})
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

// GET /v1/publisher/dramas/:id —— 剧集详情
func (s *Server) publisherGetDrama(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var drama model.Drama
	if err := s.db.First(&drama, id).Error; err != nil {
		response.NotFound(c, "剧集不存在")
		return
	}

	// 查已发行 + 审核中认领占用的平台
	occupied := s.getOccupiedPlatforms(id)
	available := getAvailablePlatformsFromOccupied(occupied)

	// 保证金计算预览
	distID := middleware.CurrentID(c)
	verified := s.isDistributorVerified(distID)
	depositExamples := map[string]int64{}
	for _, p := range available {
		depositExamples[p] = s.calcDepositAmount(drama, []string{p})
	}
	if len(available) >= 2 {
		depositExamples["all"] = s.calcDepositAmount(drama, available)
	}

	distributable := drama.Status == "published" && len(available) > 0 && isDistributable(drama)

	response.OK(c, gin.H{
		"id":                  drama.ID,
		"title":               drama.Title,
		"cover_url":           drama.CoverURL,
		"episode_count":       drama.TotalEpisodes,
		"price_cents":         drama.PriceCents,
		"description":         drama.Description,
		"available_platforms": available,
		"released_platforms":  occupiedKeys(occupied),
		"deposit_rule": gin.H{
			"base_cents":      s.calcDepositAmount(drama, []string{model.PlatformDouyin}),
			"platform_rate":   "每增加一个平台 +15%",
			"deposit_examples": depositExamples,
		},
		"claimable": distributable,
		"can_claim": distributable && verified,
	})
}

// ============================================================
// 辅助函数
// ============================================================

func (s *Server) isDistributorVerified(id uint64) bool {
	var d model.Distributor
	if err := s.db.Select("verify_status").First(&d, id).Error; err != nil {
		return false
	}
	return d.VerifyStatus == model.DistributorVerifyVerified
}

// isDistributable 判断剧集是否开放发行（Distributable 为 nil 或 true 都视为开放）
func isDistributable(d model.Drama) bool {
	return d.Distributable == nil || *d.Distributable
}

// getOccupiedPlatforms 返回指定剧集已被占用的平台集合（含已发行 + 审核中的认领）。
func (s *Server) getOccupiedPlatforms(dramaID uint64) map[string]bool {
	occupied := map[string]bool{}
	if dramaID == 0 {
		return occupied
	}
	// 已发行的（distributor_dramas: authorized/active）
	var dds []model.DistributorDrama
	s.db.Where("drama_id = ? AND status IN ?", dramaID, []string{"authorized", "active"}).Find(&dds)
	for _, dd := range dds {
		for _, p := range parsePlatforms(dd.Platforms) {
			occupied[p] = true
		}
	}
	// 审核中的认领（distributor_applications: deposit_pending/auth_pending/review_pending/contract_pending）
	activeClaimStatuses := []string{
		model.ClaimDepositPending, model.ClaimAuthPending,
		model.ClaimReviewPending, model.ClaimContractPending,
	}
	var apps []model.DistributorApplication
	s.db.Where("drama_id = ? AND status IN ?", dramaID, activeClaimStatuses).Find(&apps)
	for _, app := range apps {
		for _, p := range parsePlatforms(app.Platforms) {
			occupied[p] = true
		}
	}
	return occupied
}

// getAvailablePlatformsFromOccupied 返回未被占用的平台
func getAvailablePlatformsFromOccupied(occupied map[string]bool) []string {
	all := []string{model.PlatformDouyin, model.PlatformKuaishou, model.PlatformWechatVideo, model.PlatformBilibili}
	available := []string{}
	for _, p := range all {
		if !occupied[p] {
			available = append(available, p)
		}
	}
	return available
}

func uniqueStrings(input []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, s := range input {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}

// occupiedKeys 返回 map 中为 true 的 key 列表
func occupiedKeys(m map[string]bool) []string {
	result := []string{}
	for k, v := range m {
		if v {
			result = append(result, k)
		}
	}
	return result
}

func _publisherDramaUnused() { _ = strconv.Itoa(0) }
