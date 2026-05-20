package handler

import (
	"strings"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

func (s *Server) appHome(c *gin.Context) {
	var categories []model.Category
	s.db.Where("status = ?", model.StatusActive).
		Order("sort_order asc, id asc").
		Limit(20).
		Find(&categories)

	categoryViews := make([]gin.H, 0, len(categories))
	for _, cat := range categories {
		categoryViews = append(categoryViews, gin.H{"id": cat.ID, "name": cat.Name})
	}

	var recommend []model.Drama
	s.db.Where("status = ?", model.DramaStatusPublished).
		Order("sort_order desc, published_at desc, id desc").
		Limit(10).
		Find(&recommend)

	var hot []model.Drama
	s.db.Where("status = ?", model.DramaStatusPublished).
		Order("play_count desc, id desc").
		Limit(10).
		Find(&hot)

	response.OK(c, gin.H{
		"categories":        categoryViews,
		"recommend_dramas":  dramaCardList(recommend),
		"hot_dramas":        dramaCardList(hot),
	})
}

func (s *Server) appListDramas(c *gin.Context) {
	page, pageSize := paginate(c)
	q := s.db.Model(&model.Drama{}).Where("status = ?", model.DramaStatusPublished)

	if v := c.Query("category_id"); v != "" {
		if id := parseUint(v); id > 0 {
			q = q.Where("category_id = ?", id)
		}
	}

	switch c.DefaultQuery("sort", "new") {
	case "hot":
		q = q.Order("play_count desc, id desc")
	default:
		q = q.Order("published_at desc, id desc")
	}

	var total int64
	q.Count(&total)
	var list []model.Drama
	if err := q.Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		response.ServerError(c, "查询短剧列表失败")
		return
	}
	response.OK(c, pageResp(dramaCardList(list), page, pageSize, total))
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
		"free_episodes":  drama.FreeEpisodes,
		"price_cents":    drama.PriceCents,
		"play_count":     drama.PlayCount,
		"like_count":     drama.LikeCount,
		"favorite_count": drama.FavoriteCount,
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
	if uid := optionalAppUserID(c, s); uid > 0 {
		var unlocks []model.EpisodeUnlock
		s.db.Select("episode_id").
			Where("user_id = ? AND drama_id = ?", uid, drama.ID).
			Find(&unlocks)
		for _, u := range unlocks {
			unlocked[u.EpisodeID] = true
		}
	}

	views := make([]gin.H, 0, len(episodes))
	for _, ep := range episodes {
		views = append(views, episodeAppView(ep, drama.FreeEpisodes, unlocked[ep.ID]))
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
	q.Order("play_count desc, id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list)
	response.OK(c, pageResp(dramaCardList(list), page, pageSize, total))
}

func dramaCardList(items []model.Drama) []gin.H {
	out := make([]gin.H, 0, len(items))
	for _, d := range items {
		out = append(out, dramaCardView(d))
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
