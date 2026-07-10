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

	// 查已发行平台
	dramaIDs := make([]uint64, 0, len(dramas))
	for _, d := range dramas {
		dramaIDs = append(dramaIDs, d.ID)
	}
	releasedPlatforms := map[uint64][]string{}
	if len(dramaIDs) > 0 {
		var dds []model.DistributorDrama
		s.db.Where("drama_id IN ? AND status IN ?", dramaIDs, []string{"authorized", "active"}).Find(&dds)
		for _, dd := range dds {
			releasedPlatforms[dd.DramaID] = append(releasedPlatforms[dd.DramaID], parsePlatforms(dd.Platforms)...)
		}
	}

	list := make([]gin.H, 0, len(dramas))
	distID := middleware.CurrentID(c)
	for _, d := range dramas {
		platforms := releasedPlatforms[d.ID]
		available := getAvailablePlatforms(platforms)
		claimable := d.Status == "published" && len(available) > 0 && s.isDistributorVerified(distID)
		list = append(list, gin.H{
			"id":                  d.ID,
			"title":               d.Title,
			"cover_url":           d.CoverURL,
			"episode_count":       d.TotalEpisodes,
			"price_cents":         d.PriceCents,
			"available_platforms": available,
			"released_platforms":  uniqueStrings(platforms),
			"claimable":           claimable,
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

	// 查已发行平台
	var dds []model.DistributorDrama
	s.db.Where("drama_id = ? AND status IN ?", id, []string{"authorized", "active"}).Find(&dds)
	released := []string{}
	for _, dd := range dds {
		released = append(released, parsePlatforms(dd.Platforms)...)
	}
	available := getAvailablePlatforms(released)

	// 保证金计算预览
	distID := middleware.CurrentID(c)
	depositExamples := map[string]int64{}
	for _, p := range available {
		depositExamples[p] = s.calcDepositAmount(drama, []string{p})
	}
	if len(available) >= 2 {
		depositExamples["all"] = s.calcDepositAmount(drama, available)
	}

	response.OK(c, gin.H{
		"id":                  drama.ID,
		"title":               drama.Title,
		"cover_url":           drama.CoverURL,
		"episode_count":       drama.TotalEpisodes,
		"price_cents":         drama.PriceCents,
		"description":         drama.Description,
		"available_platforms": available,
		"released_platforms":  uniqueStrings(released),
		"deposit_rule": gin.H{
			"base_cents":      s.calcDepositAmount(drama, []string{model.PlatformDouyin}),
			"platform_rate":   "每增加一个平台 +15%",
			"deposit_examples": depositExamples,
		},
		"claimable": drama.Status == "published" && len(available) > 0 && s.isDistributorVerified(distID),
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

// getAvailablePlatforms 返回未发行的平台
func getAvailablePlatforms(released []string) []string {
	all := []string{model.PlatformDouyin, model.PlatformKuaishou, model.PlatformWechatVideo, model.PlatformBilibili}
	releasedMap := map[string]bool{}
	for _, r := range released {
		releasedMap[r] = true
	}
	available := []string{}
	for _, p := range all {
		if !releasedMap[p] {
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

func _publisherDramaUnused() { _ = strconv.Itoa(0) }
