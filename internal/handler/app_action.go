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

func (s *Server) appLikeDrama(c *gin.Context) {
	s.appToggleAction(c, model.ActionLike, "like_count", true)
}

func (s *Server) appUnlikeDrama(c *gin.Context) {
	s.appToggleAction(c, model.ActionLike, "like_count", false)
}

func (s *Server) appFavoriteDrama(c *gin.Context) {
	s.appToggleAction(c, model.ActionFavorite, "favorite_count", true)
}

func (s *Server) appUnfavoriteDrama(c *gin.Context) {
	s.appToggleAction(c, model.ActionFavorite, "favorite_count", false)
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
	// MVP：分享只做埋点，不落表。channel 字段读出但不持久化。
	_ = c.ShouldBindJSON(&struct {
		Channel string `json:"channel"`
	}{})
	response.OK(c, gin.H{"shared": true})
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

func (s *Server) appToggleAction(c *gin.Context, action, counter string, enable bool) {
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
			rec := model.UserAction{UserID: uid, DramaID: dramaID, Action: action}
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rec)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected > 0 {
				if err := tx.Model(&model.Drama{}).
					Where("id = ?", dramaID).
					UpdateColumn(counter, gorm.Expr(counter+" + ?", 1)).Error; err != nil {
					return err
				}
			}
		} else {
			res := tx.Where("user_id = ? AND drama_id = ? AND action = ?", uid, dramaID, action).
				Delete(&model.UserAction{})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected > 0 {
				if err := tx.Model(&model.Drama{}).
					Where("id = ? AND "+counter+" > 0", dramaID).
					UpdateColumn(counter, gorm.Expr(counter+" - ?", 1)).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		response.ServerError(c, "操作失败")
		return
	}

	// 重新读最新计数
	var refreshed model.Drama
	s.db.Select("like_count", "favorite_count").First(&refreshed, dramaID)

	switch action {
	case model.ActionLike:
		response.OK(c, gin.H{"liked": enable, "like_count": refreshed.LikeCount})
	case model.ActionFavorite:
		response.OK(c, gin.H{"favorited": enable, "favorite_count": refreshed.FavoriteCount})
	default:
		response.OK(c, gin.H{"ok": true})
	}
}
