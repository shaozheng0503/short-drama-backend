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
	distID := middleware.CurrentID(c)

	// 解析筛选参数
	// claimable=true：只返回剧本身可认领的（已上架 + 有可发行平台 + 开放发行）
	// can_claim=true：只返回当前发行商当前可发起认领的（claimable + 已认证）
	// can_claim 优先级高于 claimable（can_claim 已隐含 claimable）
	wantClaimable := c.Query("claimable") == "true"
	wantCanClaim := c.Query("can_claim") == "true"

	// 基础查询
	q := s.db.Model(&model.Drama{}).Where("status = ?", "published")
	if v := c.Query("keyword"); v != "" {
		q = q.Where("title ILIKE ?", "%"+v+"%")
	}
	if v := c.Query("platform"); v != "" {
		// 筛选支持该平台的剧——暂不实现复杂筛选，返回全部
		_ = v
	}

	// claimable/can_claim 需要排除「全平台已被占用」的剧。
	// 全平台占用 = 该剧所有 4 个平台都出现在 distributor_dramas(authorized/active)
	//   或 distributor_applications(审核中) 的 platforms 里。
	if wantClaimable || wantCanClaim {
		// 查出当前页候选剧（不分页，只取 ID）被占用的平台集合。
		// 为了分页准确，需要先过滤掉「全平台占用」的剧，再分页。
		// 但「全平台占用」的判定在 SQL 里较复杂（需要解析 platforms 数组），
		// 这里采用 Go 层过滤的方式：查出所有候选 ID → 算 occupied → 过滤 → 分页。
		// 数据量可控（已上架剧不会太多），性能可接受。

		// 1. 先查所有候选剧的 ID + 基本字段（不分页）
		var allDramas []model.Drama
		q.Order("created_at desc").Find(&allDramas)

		// 2. 批量查 occupied
		dramaIDs := make([]uint64, 0, len(allDramas))
		for _, d := range allDramas {
			dramaIDs = append(dramaIDs, d.ID)
		}
		occupiedPlatforms := s.batchGetOccupiedPlatforms(dramaIDs)

		// 3. 过滤
		verified := s.isDistributorVerified(distID)
		filtered := make([]model.Drama, 0, len(allDramas))
		for _, d := range allDramas {
			occupied := occupiedPlatforms[d.ID]
			available := getAvailablePlatformsFromOccupied(occupied)
			distributable := d.Status == "published" && len(available) > 0 && isDistributable(d)

			if wantCanClaim {
				if distributable && verified {
					filtered = append(filtered, d)
				}
			} else if wantClaimable {
				if distributable {
					filtered = append(filtered, d)
				}
			}
		}

		// 4. 分页
		total := int64(len(filtered))
		start := (page - 1) * pageSize
		if start >= len(filtered) {
			filtered = []model.Drama{}
		} else {
			end := start + pageSize
			if end > len(filtered) {
				end = len(filtered)
			}
			filtered = filtered[start:end]
		}

		list := s.buildPublisherDramaList(filtered, occupiedPlatforms, distID, verified)
		response.OK(c, pageResp(list, page, pageSize, total))
		return
	}

	// 无 claimable/can_claim 筛选：原有逻辑
	var total int64
	q.Count(&total)
	var dramas []model.Drama
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&dramas)

	dramaIDs := make([]uint64, 0, len(dramas))
	for _, d := range dramas {
		dramaIDs = append(dramaIDs, d.ID)
	}
	occupiedPlatforms := s.batchGetOccupiedPlatforms(dramaIDs)
	verified := s.isDistributorVerified(distID)
	list := s.buildPublisherDramaList(dramas, occupiedPlatforms, distID, verified)
	response.OK(c, pageResp(list, page, pageSize, total))
}

// batchGetOccupiedPlatforms 批量查多部剧被占用的平台集合（含已发行 + 审核中认领）
func (s *Server) batchGetOccupiedPlatforms(dramaIDs []uint64) map[uint64]map[string]bool {
	result := map[uint64]map[string]bool{}
	if len(dramaIDs) == 0 {
		return result
	}

	// 已发行的
	var dds []model.DistributorDrama
	s.db.Where("drama_id IN ? AND status IN ?", dramaIDs, []string{"authorized", "active"}).Find(&dds)
	for _, dd := range dds {
		if result[dd.DramaID] == nil {
			result[dd.DramaID] = map[string]bool{}
		}
		for _, p := range parsePlatforms(dd.Platforms) {
			result[dd.DramaID][p] = true
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
		if result[app.DramaID] == nil {
			result[app.DramaID] = map[string]bool{}
		}
		for _, p := range parsePlatforms(app.Platforms) {
			result[app.DramaID][p] = true
		}
	}

	return result
}

// buildPublisherDramaList 构建剧集广场列表响应项
// 2026-07-28 会议：价格对发行商隐藏，仅展示标题/封面/集数/可认领平台
func (s *Server) buildPublisherDramaList(dramas []model.Drama, occupiedPlatforms map[uint64]map[string]bool, distID uint64, verified bool) []gin.H {
	list := make([]gin.H, 0, len(dramas))
	for _, d := range dramas {
		occupied := occupiedPlatforms[d.ID]
		if occupied == nil {
			occupied = map[string]bool{}
		}
		available := getAvailablePlatformsFromOccupied(occupied)
		distributable := d.Status == "published" && len(available) > 0 && isDistributable(d)
		list = append(list, gin.H{
			"id":                  d.ID,
			"title":               d.Title,
			"cover_url":           d.CoverURL,
			"episode_count":       d.TotalEpisodes,
			"available_platforms": available,
			"released_platforms":  occupiedKeys(occupied),
			"claimable":           distributable,
			"can_claim":           distributable && verified,
		})
	}
	return list
}

// GET /v1/publisher/dramas/:id —— 剧集详情
//
// 2026-07-28 会议信息展示规则：
//   - 未认领：仅可看标题、封面、可认领平台、保证金规则；不可看剧情介绍、角色、集数列表
//   - 已认领（审核中/已授权）：可看剧情介绍、角色信息、剧集级数；价格和合同信息隐藏
//
// 设计依据：未授权主体不应提前获取核心内容用于违规上传；已授权发行商不需要看价格和合同。
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

	// 基础信息：所有发行商可见
	v := gin.H{
		"id":                  drama.ID,
		"title":               drama.Title,
		"cover_url":           drama.CoverURL,
		"episode_count":       drama.TotalEpisodes,
		"available_platforms": available,
		"released_platforms":  occupiedKeys(occupied),
		"deposit_rule": gin.H{
			"base_cents":       s.calcDepositAmount(drama, []string{model.PlatformDouyin}),
			"platform_rate":    "每增加一个平台 +15%",
			"duration_minutes":  s.dramaTotalMinutes(drama.ID),
			"tier_rule":        "≤25分钟 400元，≥26分钟 500元，每增一个平台 +15%",
			"deposit_examples":  depositExamples,
		},
		"claimable": distributable,
		"can_claim": distributable && verified,
	}

	// 检查当前发行商是否有该剧的有效认领/授权
	hasClaim := s.distributorHasClaim(distID, id)
	if hasClaim {
		// 已认领（审核中或已授权）：可看剧情介绍、角色信息、集数列表
		v["description"] = drama.Description

		// 角色信息
		var characters []model.DramaCharacter
		s.db.Where("drama_id = ?", id).Order("sort_order asc").Find(&characters)
		v["characters"] = characters

		// 集数列表（简要信息：集号+标题+时长，不含播放地址）
		var episodes []model.Episode
		s.db.Select("id, episode_no, title, duration_seconds, status").
			Where("drama_id = ?", id).Order("episode_no asc").Find(&episodes)
		epList := make([]gin.H, 0, len(episodes))
		for _, ep := range episodes {
			epList = append(epList, gin.H{
				"id":               ep.ID,
				"episode_no":       ep.EpisodeNo,
				"title":            ep.Title,
				"duration_seconds": ep.DurationSeconds,
				"status":           ep.Status,
			})
		}
		v["episodes"] = epList
		// 注意：price_cents 和合同信息不返回——发行商不需要看价格，合同线下对接
	} else {
		v["description"] = ""
		v["characters"] = []interface{}{}
		v["episodes"] = []interface{}{}
	}

	response.OK(c, v)
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

// distributorHasClaim 检查发行商是否对指定剧集有有效认领/授权关系。
// 含已授权发行（distributor_dramas: authorized/active）和审核中认领（distributor_applications: pending 状态）。
// 用于信息展示规则：有认领关系 → 可看剧情/角色/集数；无 → 不可看。
func (s *Server) distributorHasClaim(distributorID, dramaID uint64) bool {
	// 已授权/活跃
	var ddCount int64
	s.db.Model(&model.DistributorDrama{}).
		Where("distributor_id = ? AND drama_id = ? AND status IN ?",
			distributorID, dramaID, []string{model.DistDramaAuthorized, model.DistDramaActive}).
		Count(&ddCount)
	if ddCount > 0 {
		return true
	}
	// 审核中的认领
	var appCount int64
	s.db.Model(&model.DistributorApplication{}).
		Where("distributor_id = ? AND drama_id = ? AND status IN ?",
			distributorID, dramaID, []string{
				model.ClaimDepositPending, model.ClaimAuthPending,
				model.ClaimReviewPending, model.ClaimContractPending,
			}).
		Count(&appCount)
	return appCount > 0
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
	// 审核中的认领（distributor_applications: deposit_pending/authorization_pending/review_pending/contract_pending）
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
	all := []string{model.PlatformDouyin, model.PlatformKuaishou, model.PlatformWechatVideo}
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
