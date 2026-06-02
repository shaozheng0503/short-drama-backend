package handler

import (
	"errors"

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
	if err := s.db.Model(&model.Drama{}).Where("id = ?", id).
		UpdateColumn("share_count", gorm.Expr("share_count + ?", 1)).Error; err != nil {
		response.ServerError(c, "记录分享失败")
		return
	}
	var refreshed model.Drama
	s.db.Select("share_count").First(&refreshed, id)
	response.OK(c, gin.H{"shared": true, "share_count": refreshed.ShareCount})
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

	response.OK(c, pageResp(dramaCardList(dramas), page, pageSize, total))
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
