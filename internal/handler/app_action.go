package handler

import (
	"errors"
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 点赞是单集级：操作 episodes/:id，同时维护 episodes.like_count 与 dramas.like_count（全剧总赞）。
func (s *Server) appLikeEpisode(c *gin.Context) {
	s.appToggleEpisodeLike(c, true)
}

func (s *Server) appUnlikeEpisode(c *gin.Context) {
	s.appToggleEpisodeLike(c, false)
}

// 收藏是整剧级：操作 dramas/:id，episode_id=0。
func (s *Server) appFavoriteDrama(c *gin.Context) {
	s.appToggleFavorite(c, true)
}

func (s *Server) appUnfavoriteDrama(c *gin.Context) {
	s.appToggleFavorite(c, false)
}

func (s *Server) appShareDrama(c *gin.Context) {
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
	// channel 选填，仅作埋点参考，不落明细表（分享明细量大且无需逐条留存）。
	_ = c.ShouldBindJSON(&struct {
		Channel string `json:"channel"`
	}{})
	// 去重：同一用户对同一剧在窗口内只计一次，防刷高 share_count + 写放大。
	counted := s.allowShareCountByUser(middleware.CurrentID(c), id)
	if counted {
		if err := s.db.Model(&model.Drama{}).Where("id = ?", id).
			UpdateColumn("share_count", gorm.Expr("share_count + ?", 1)).Error; err != nil {
			response.ServerError(c, "记录分享失败")
			return
		}
	}
	var refreshed model.Drama
	s.db.Select("share_count").First(&refreshed, id)
	response.OK(c, gin.H{"shared": true, "share_counted": counted, "share_count": refreshed.ShareCount})
}

func (s *Server) appListFavorites(c *gin.Context) {
	uid := middleware.CurrentID(c)
	page, pageSize := paginate(c)

	base := s.db.Table("user_actions").
		Where("user_actions.user_id = ? AND user_actions.action = ?", uid, model.ActionFavorite).
		Joins("JOIN dramas ON dramas.id = user_actions.drama_id").
		Where("dramas.status = ?", model.DramaStatusPublished)

	var total int64
	base.Session(&gorm.Session{}).Count(&total)

	var dramas []model.Drama
	if err := base.Select("dramas.*").
		Order("user_actions.created_at desc").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Scan(&dramas).Error; err != nil {
		response.ServerError(c, "查询收藏失败")
		return
	}

	response.OK(c, pageResp(dramaCardList(dramas, s.effectiveFreeEpisodes(model.Drama{})), page, pageSize, total))
}

// appListLikes —— 「我的点赞」：当前用户点赞过的剧集（集级，每集一条，按点赞时间倒序）。
// 剧集本身无封面，故 cover_url 用所属剧的主封面（对齐红果「返回每一集的封面即可」）；
// like_count 为该集的点赞数。短剧/漫剧/高光/预告 等内容类型筛选暂无字段支撑，先返回全部。
func (s *Server) appListLikes(c *gin.Context) {
	uid := middleware.CurrentID(c)
	page, pageSize := paginate(c)

	base := s.db.Table("user_actions").
		Where("user_actions.user_id = ? AND user_actions.action = ? AND user_actions.episode_id > 0", uid, model.ActionLike).
		Joins("JOIN episodes ON episodes.id = user_actions.episode_id").
		Joins("JOIN dramas ON dramas.id = user_actions.drama_id").
		Where("dramas.status = ?", model.DramaStatusPublished)

	var total int64
	base.Session(&gorm.Session{}).Count(&total)

	var rows []struct {
		EpisodeID        uint64
		EpisodeNo        int
		EpisodeLikeCount int64
		DramaID          uint64
		DramaTitle       string
		CoverURL         string
		LikedAt          time.Time
	}
	if err := base.
		Select(`episodes.id AS episode_id, episodes.episode_no AS episode_no,
			episodes.like_count AS episode_like_count, dramas.id AS drama_id,
			dramas.title AS drama_title, dramas.cover_url AS cover_url,
			user_actions.created_at AS liked_at`).
		Order("user_actions.created_at desc").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Scan(&rows).Error; err != nil {
		response.ServerError(c, "查询点赞失败")
		return
	}

	list := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		list = append(list, gin.H{
			"episode_id": r.EpisodeID,
			"episode_no": r.EpisodeNo,
			"drama_id":   r.DramaID,
			"title":      r.DramaTitle,
			"cover_url":  r.CoverURL,
			"like_count": r.EpisodeLikeCount,
			"liked_at":   r.LikedAt,
		})
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

// appToggleFavorite 收藏 / 取消收藏（整剧级，episode_id=0）。
func (s *Server) appToggleFavorite(c *gin.Context, enable bool) {
	uid := middleware.CurrentID(c)
	dramaID := parseUint(c.Param("id"))
	if dramaID == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}

	var drama model.Drama
	if err := s.db.First(&drama, dramaID).Error; err != nil {
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

	err := s.db.Transaction(func(tx *gorm.DB) error {
		if enable {
			rec := model.UserAction{UserID: uid, DramaID: dramaID, EpisodeID: 0, Action: model.ActionFavorite}
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rec)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected > 0 {
				return tx.Model(&model.Drama{}).Where("id = ?", dramaID).
					UpdateColumn("favorite_count", gorm.Expr("favorite_count + ?", 1)).Error
			}
		} else {
			res := tx.Where("user_id = ? AND drama_id = ? AND episode_id = 0 AND action = ?", uid, dramaID, model.ActionFavorite).
				Delete(&model.UserAction{})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected > 0 {
				return tx.Model(&model.Drama{}).Where("id = ? AND favorite_count > 0", dramaID).
					UpdateColumn("favorite_count", gorm.Expr("favorite_count - ?", 1)).Error
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		response.ServerError(c, "操作失败")
		return
	}

	var refreshed model.Drama
	s.db.Select("favorite_count").First(&refreshed, dramaID)
	response.OK(c, gin.H{"favorited": enable, "favorite_count": refreshed.FavoriteCount})
}

// appToggleEpisodeLike 点赞 / 取消点赞（单集级）。同步该集 episodes.like_count
// 与该剧 dramas.like_count（全剧总赞 = 各集之和）。
func (s *Server) appToggleEpisodeLike(c *gin.Context, enable bool) {
	uid := middleware.CurrentID(c)
	episodeID := parseUint(c.Param("id"))
	if episodeID == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}

	var ep model.Episode
	if err := s.db.First(&ep, episodeID).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "剧集不存在")
			return
		}
		response.ServerError(c, "查询剧集失败")
		return
	}
	var drama model.Drama
	if err := s.db.First(&drama, ep.DramaID).Error; err != nil {
		response.ServerError(c, "查询短剧失败")
		return
	}
	if drama.Status != model.DramaStatusPublished {
		response.NotFound(c, "短剧未上架")
		return
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		if enable {
			rec := model.UserAction{UserID: uid, DramaID: ep.DramaID, EpisodeID: ep.ID, Action: model.ActionLike}
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rec)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected > 0 {
				if err := tx.Model(&model.Episode{}).Where("id = ?", ep.ID).
					UpdateColumn("like_count", gorm.Expr("like_count + ?", 1)).Error; err != nil {
					return err
				}
				return tx.Model(&model.Drama{}).Where("id = ?", ep.DramaID).
					UpdateColumn("like_count", gorm.Expr("like_count + ?", 1)).Error
			}
		} else {
			res := tx.Where("user_id = ? AND episode_id = ? AND action = ?", uid, ep.ID, model.ActionLike).
				Delete(&model.UserAction{})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected > 0 {
				if err := tx.Model(&model.Episode{}).Where("id = ? AND like_count > 0", ep.ID).
					UpdateColumn("like_count", gorm.Expr("like_count - ?", 1)).Error; err != nil {
					return err
				}
				return tx.Model(&model.Drama{}).Where("id = ? AND like_count > 0", ep.DramaID).
					UpdateColumn("like_count", gorm.Expr("like_count - ?", 1)).Error
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		response.ServerError(c, "操作失败")
		return
	}

	var refreshed model.Episode
	s.db.Select("like_count").First(&refreshed, episodeID)
	response.OK(c, gin.H{"liked": enable, "like_count": refreshed.LikeCount})
}
