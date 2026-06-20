package handler

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// dramaCardColumns 卡片 / feed 渲染实际用到的最小列集（见 dramaCardView），
// 用来给列表查询做 SELECT 收窄：dramas 表有一堆申报级宽列（description/alias/制作机构/承诺函URL…），
// SELECT * 会把这些全读进来再丢掉。列表/推荐流是高频接口，只取卡片要的列省 I/O 与 GC。
var dramaCardColumns = []string{
	"id", "title", "description", "cover_url",
	"total_episodes", "play_count", "like_count", "share_count",
}

// cardDramasByIDs 按给定 id 顺序、用窄列回查卡片数据，并保持入参顺序（feed/分页顺序不能乱）。
// 配合「id 投影排序 + 主键回查」：排序阶段只排 id，不把 5KB 宽行拖进排序。
func (s *Server) cardDramasByIDs(ids []uint64) []model.Drama {
	if len(ids) == 0 {
		return nil
	}
	var rows []model.Drama
	s.db.Select(dramaCardColumns).Where("id IN ?", ids).Find(&rows)
	byID := make(map[uint64]model.Drama, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	out := make([]model.Drama, 0, len(ids))
	for _, id := range ids {
		if d, ok := byID[id]; ok {
			out = append(out, d)
		}
	}
	return out
}

func (s *Server) appHome(c *gin.Context) {
	// 首页按短视频信息流返回：前端直接拿 recommend_dramas 渲染上下滑视频流。
	// 随机推荐：先对 id 投影做 ORDER BY RANDOM()（只排 id、不排 5KB 宽行），再按主键回查窄列卡片。
	// 规模化下避免「全表宽行随机排序」（实测 20k 剧宽行随机排序 16ms）；始终实时、无缓存陈旧。
	var ids []uint64
	s.db.Model(&model.Drama{}).
		Where("status = ?", model.DramaStatusPublished).
		Order("RANDOM()").
		Limit(10).
		Pluck("id", &ids)

	response.OK(c, gin.H{
		"recommend_dramas": s.homeFeedDramaList(s.cardDramasByIDs(ids)),
	})
}

// appTheater —— GET /v1/app/theater
// 剧场页（图文推荐流）：与 /app/dramas 筛选列表区分，每次刷新可换一批推荐顺序。
// 不传 seed 时服务端生成新 seed（下拉刷新即新推荐）；翻页时带上 recommend_seed 保持顺序稳定。
func (s *Server) appTheater(c *gin.Context) {
	page, pageSize := paginate(c)
	seed := strings.TrimSpace(c.Query("seed"))
	if seed == "" {
		seed = newRecommendSeed()
	}

	q := s.db.Model(&model.Drama{}).Where("status = ?", model.DramaStatusPublished)
	if v := strings.TrimSpace(c.Query("audience")); v != "" {
		q = q.Where("audience = ?", v)
	}

	var total int64
	q.Count(&total)

	// id 投影排序：只对 id 做 md5 seed 洗牌排序（窄元组），分页取本页 id，再按主键回查窄列卡片。
	// 避免把全表宽行拖进 md5 排序（实测 20k 剧宽行 md5 排序 30ms）。seed 固定 → 翻页顺序稳定。
	var ids []uint64
	if err := q.Order(gorm.Expr("md5(dramas.id::text || ?) ASC", seed)).
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Pluck("id", &ids).Error; err != nil {
		response.ServerError(c, "查询剧场推荐失败")
		return
	}

	views := s.dramaCardListWithTags(s.cardDramasByIDs(ids))
	data := pageResp(views, page, pageSize, total)
	data["recommend_seed"] = seed
	response.OK(c, data)
}

func newRecommendSeed() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "theater"
	}
	return hex.EncodeToString(b)
}

func (s *Server) dramaCardListWithTags(list []model.Drama) []gin.H {
	views := dramaCardList(list, s.effectiveFreeEpisodes(model.Drama{}))
	ids := make([]uint64, len(list))
	for i, d := range list {
		ids[i] = d.ID
	}
	tagMap := s.collectDramaTagNames(ids)
	for i, d := range list {
		tags := tagMap[d.ID]
		if tags == nil {
			tags = []string{}
		}
		views[i]["tags"] = tags
	}
	return views
}

func (s *Server) homeFeedDramaList(dramas []model.Drama) []gin.H {
	out := make([]gin.H, 0, len(dramas))
	if len(dramas) == 0 {
		return out
	}

	dramaIDs := make([]uint64, 0, len(dramas))
	for _, drama := range dramas {
		dramaIDs = append(dramaIDs, drama.ID)
	}

	var episodes []model.Episode
	s.db.Where("drama_id IN ? AND status = ?", dramaIDs, model.EpisodeStatusReady).
		Order("drama_id asc, episode_no asc").
		Find(&episodes)

	firstEpisodeByDrama := map[uint64]model.Episode{}
	for _, episode := range episodes {
		if _, ok := firstEpisodeByDrama[episode.DramaID]; !ok {
			firstEpisodeByDrama[episode.DramaID] = episode
		}
	}

	// 批量统计各首集的评论数（评论软删，Model(&Comment{}) 自动带 deleted_at IS NULL）。
	// 首页是有界小列表（≤10 集），读时 COUNT 即可，无需维护冗余计数列。
	firstEpisodeIDs := make([]uint64, 0, len(firstEpisodeByDrama))
	for _, episode := range firstEpisodeByDrama {
		firstEpisodeIDs = append(firstEpisodeIDs, episode.ID)
	}
	commentCountByEpisode := s.commentCountByEpisodeIDs(firstEpisodeIDs)

	// 免费集数统一走全局配置（与播放/计费同口径），列表里每张卡用同一个值。
	freeEp := s.effectiveFreeEpisodes(model.Drama{})
	for _, drama := range dramas {
		view := dramaCardView(drama, freeEp)
		if episode, ok := firstEpisodeByDrama[drama.ID]; ok {
			view["first_episode"] = gin.H{
				"id":               episode.ID,
				"episode_no":       episode.EpisodeNo,
				"title":            episode.Title,
				"play_url":         episode.VideoURL,
				"duration_seconds": episode.DurationSeconds,
				"comment_count":    commentCountByEpisode[episode.ID],
			}
		} else {
			view["first_episode"] = nil
		}
		out = append(out, view)
	}
	return out
}

// commentCountByEpisodeIDs 批量统计每个剧集的评论数，返回 episode_id -> 评论数。
// 没有评论的剧集不会出现在 map 中，调用方取零值即可（map 取不到默认 0）。
func (s *Server) commentCountByEpisodeIDs(episodeIDs []uint64) map[uint64]int64 {
	counts := make(map[uint64]int64, len(episodeIDs))
	if len(episodeIDs) == 0 {
		return counts
	}
	var rows []struct {
		EpisodeID uint64
		Cnt       int64
	}
	s.db.Model(&model.Comment{}).
		Select("episode_id, count(*) as cnt").
		Where("episode_id IN ?", episodeIDs).
		Group("episode_id").
		Scan(&rows)
	for _, r := range rows {
		counts[r.EpisodeID] = r.Cnt
	}
	return counts
}

func (s *Server) appListDramas(c *gin.Context) {
	page, pageSize := paginate(c)
	q := s.db.Model(&model.Drama{}).Where("status = ?", model.DramaStatusPublished)

	if v := c.Query("category_id"); v != "" {
		if id := parseUint(v); id > 0 {
			q = q.Where("category_id = ?", id)
		}
	}
	// 多维筛选：category_ids 支持「主题/设定/背景/受众」任意维度的分类多选（逗号分隔），
	// 语义为 AND——剧必须同时命中所有所选分类（按 drama_tags 关联）。
	if catIDs := parseUintList(c.Query("category_ids")); len(catIDs) > 0 {
		q = q.Where(
			"id IN (SELECT drama_id FROM drama_tags WHERE category_id IN ? GROUP BY drama_id HAVING COUNT(DISTINCT category_id) = ?)",
			catIDs, len(catIDs),
		)
	}
	// 男女频：直接按 dramas.audience 字段筛（男频/女频/通用）。
	if v := strings.TrimSpace(c.Query("audience")); v != "" {
		q = q.Where("audience = ?", v)
	}
	if v := c.Query("language_id"); v != "" {
		if id := parseUint(v); id > 0 {
			q = q.Where("language_id = ?", id)
		}
	} else if v := c.Query("dialect_id"); v != "" {
		// 兼容旧参数：方言筛选已合并为 language_id
		if id := parseUint(v); id > 0 {
			q = q.Where("language_id = ?", id)
		}
	}

	switch c.DefaultQuery("sort", "new") {
	case "hot": // 热度/推荐
		q = q.Order("play_count desc, id desc")
	default: // 时间（最新）
		q = q.Order("published_at desc, id desc")
	}

	var total int64
	q.Count(&total)
	var list []model.Drama
	// 收窄列：只取卡片渲染要的字段，不读 description 外的一堆申报级宽列。
	if err := q.Select(dramaCardColumns).Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		response.ServerError(c, "查询短剧列表失败")
		return
	}
	// 筛选列表也返回 tags，便于复用卡片组件；推荐流请用 GET /app/theater。
	views := s.dramaCardListWithTags(list)
	response.OK(c, pageResp(views, page, pageSize, total))
}

// collectDramaTagNames 批量取每部剧的分类标签名（drama_tags 关联），用于剧场页卡片「类型」展示。
// 顺序：题材(theme)优先，再 setting / background / audience，各维度内按 sort_order——
// 这样前端截断前 N 个时拿到的是最能描述剧的题材标签（对齐红果卡片「战斗/奇幻/古代」那种）。
func (s *Server) collectDramaTagNames(dramaIDs []uint64) map[uint64][]string {
	out := map[uint64][]string{}
	if len(dramaIDs) == 0 {
		return out
	}
	var rows []struct {
		DramaID uint64
		Name    string
	}
	s.db.Table("drama_tags").
		Select("drama_tags.drama_id AS drama_id, categories.name AS name").
		Joins("JOIN categories ON categories.id = drama_tags.category_id").
		Where("drama_tags.drama_id IN ?", dramaIDs).
		Order(`drama_tags.drama_id,
			CASE categories.type WHEN 'theme' THEN 1 WHEN 'setting' THEN 2 WHEN 'background' THEN 3 ELSE 4 END,
			categories.sort_order`).
		Scan(&rows)
	// 同名标签去重（一部剧可能在题材+背景都标了同名，如「年代」/「民国」）；
	// rows 已按题材优先排序，保留首次出现即保留题材那条。
	seen := map[uint64]map[string]bool{}
	for _, r := range rows {
		if seen[r.DramaID] == nil {
			seen[r.DramaID] = map[string]bool{}
		}
		if seen[r.DramaID][r.Name] {
			continue
		}
		seen[r.DramaID][r.Name] = true
		out[r.DramaID] = append(out[r.DramaID], r.Name)
	}
	return out
}

func (s *Server) appDramaDetail(c *gin.Context) {
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
	if drama.Status != model.DramaStatusPublished {
		response.NotFound(c, "短剧未上架")
		return
	}

	var categoryView *gin.H
	if drama.CategoryID != nil {
		var cat model.Category
		if err := s.db.First(&cat, *drama.CategoryID).Error; err == nil {
			view := gin.H{"id": cat.ID, "name": cat.Name}
			categoryView = &view
		}
	}

	resp := gin.H{
		"id":             drama.ID,
		"title":          drama.Title,
		"description":    drama.Description,
		"cover_url":      drama.CoverURL,
		"category":       categoryView,
		"total_episodes": drama.TotalEpisodes,
		"free_episodes":  s.effectiveFreeEpisodes(drama),
		"price_cents":    drama.PriceCents,
		"language_id":    drama.LanguageID,
		"play_count":     drama.PlayCount,
		"like_count":     drama.LikeCount,
		"favorite_count": drama.FavoriteCount,
		"share_count":    drama.ShareCount,
		"is_liked":       false,
		"is_favorited":   false,
		"last_watch":     nil,
	}

	// 登录后扩展 like/favorite/last_watch；APP 详情页面 spec 允许匿名
	if uid := optionalAppUserID(c, s); uid > 0 {
		liked := s.userHasAction(uid, drama.ID, model.ActionLike)
		fav := s.userHasAction(uid, drama.ID, model.ActionFavorite)
		resp["is_liked"] = liked
		resp["is_favorited"] = fav

		var last model.PlayHistory
		if err := s.db.Where("user_id = ? AND drama_id = ?", uid, drama.ID).
			Order("updated_at desc").
			First(&last).Error; err == nil {
			var ep model.Episode
			if err := s.db.First(&ep, last.EpisodeID).Error; err == nil {
				resp["last_watch"] = gin.H{
					"episode_id":       ep.ID,
					"episode_no":       ep.EpisodeNo,
					"progress_seconds": last.ProgressSeconds,
				}
			}
		}
	}

	response.OK(c, resp)
}

func (s *Server) appListEpisodes(c *gin.Context) {
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
	if drama.Status != model.DramaStatusPublished {
		response.NotFound(c, "短剧未上架")
		return
	}

	var episodes []model.Episode
	if err := s.db.Where("drama_id = ? AND status = ?", drama.ID, model.EpisodeStatusReady).
		Order("episode_no asc").
		Find(&episodes).Error; err != nil {
		response.ServerError(c, "查询剧集失败")
		return
	}

	unlocked := map[uint64]bool{}
	liked := map[uint64]bool{}
	if uid := optionalAppUserID(c, s); uid > 0 {
		var unlocks []model.EpisodeUnlock
		s.db.Select("episode_id").
			Where("user_id = ? AND drama_id = ?", uid, drama.ID).
			Find(&unlocks)
		for _, u := range unlocks {
			unlocked[u.EpisodeID] = true
		}
		var likedActions []model.UserAction
		s.db.Select("episode_id").
			Where("user_id = ? AND drama_id = ? AND action = ?", uid, drama.ID, model.ActionLike).
			Find(&likedActions)
		for _, a := range likedActions {
			liked[a.EpisodeID] = true
		}
	}

	episodeIDs := make([]uint64, len(episodes))
	for i, ep := range episodes {
		episodeIDs[i] = ep.ID
	}
	commentCountByEpisode := s.commentCountByEpisodeIDs(episodeIDs)

	freeEp := s.effectiveFreeEpisodes(drama)
	views := make([]gin.H, 0, len(episodes))
	for _, ep := range episodes {
		views = append(views, episodeAppView(ep, freeEp, unlocked[ep.ID], liked[ep.ID], commentCountByEpisode[ep.ID]))
	}
	response.OK(c, gin.H{"list": views})
}

func (s *Server) appSearch(c *gin.Context) {
	keyword := strings.TrimSpace(c.Query("q"))
	if keyword == "" {
		response.InvalidParam(c, "q 不能为空")
		return
	}
	page, pageSize := paginate(c)

	like := "%" + keyword + "%"
	q := s.db.Model(&model.Drama{}).
		Where("status = ? AND (title ILIKE ? OR description ILIKE ?)", model.DramaStatusPublished, like, like)
	var total int64
	q.Count(&total)
	var list []model.Drama
	q.Select(dramaCardColumns).Order("play_count desc, id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list)
	response.OK(c, pageResp(dramaCardList(list, s.effectiveFreeEpisodes(model.Drama{})), page, pageSize, total))
}

func dramaCardList(items []model.Drama, freeEpisodes int) []gin.H {
	out := make([]gin.H, 0, len(items))
	for _, d := range items {
		out = append(out, dramaCardView(d, freeEpisodes))
	}
	return out
}

func (s *Server) userHasAction(userID, dramaID uint64, action string) bool {
	var cnt int64
	s.db.Model(&model.UserAction{}).
		Where("user_id = ? AND drama_id = ? AND action = ?", userID, dramaID, action).
		Count(&cnt)
	return cnt > 0
}

// optionalAppUserID 适用于"可匿名、登录后扩展"的接口。
// 这里使用一个轻量解析：如果 Authorization 是合法 APP token 则返回 user id；否则 0。
func optionalAppUserID(c *gin.Context, s *Server) uint64 {
	if id := middleware.CurrentID(c); id > 0 && middleware.CurrentSubject(c) == middleware.SubjectApp {
		return id
	}
	return middleware.TryAppUserID(c, s.cfg)
}
