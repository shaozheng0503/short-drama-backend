package handler

import (
	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

// requireCreatorOwnsDrama 校验路径上的 drama 存在且属于当前登录创作者。
// 不属于的情况统一返 404 而非 403，避免暴露其它 creator 名下 drama_id 的存在性。
// 已写入错误响应时返回 ok=false，调用方必须直接 return。
func (s *Server) requireCreatorOwnsDrama(c *gin.Context, dramaID uint64) (*model.Drama, bool) {
	cid := middleware.CurrentID(c)
	var d model.Drama
	if err := s.db.First(&d, dramaID).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "短剧不存在")
			return nil, false
		}
		response.ServerError(c, "查询短剧失败")
		return nil, false
	}
	if d.CreatorID == nil || *d.CreatorID != cid {
		response.NotFound(c, "短剧不存在")
		return nil, false
	}
	return &d, true
}

// requireCreatorOwnsEpisode 校验剧集存在且其所属剧归当前创作者。
// 返回 episode + 所属 drama，方便 caller 同时拿到 drama.Status 做状态判定。
func (s *Server) requireCreatorOwnsEpisode(c *gin.Context, epID uint64) (*model.Episode, *model.Drama, bool) {
	cid := middleware.CurrentID(c)
	var ep model.Episode
	if err := s.db.First(&ep, epID).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "剧集不存在")
			return nil, nil, false
		}
		response.ServerError(c, "查询剧集失败")
		return nil, nil, false
	}
	var d model.Drama
	if err := s.db.First(&d, ep.DramaID).Error; err != nil {
		response.ServerError(c, "查询所属短剧失败")
		return nil, nil, false
	}
	if d.CreatorID == nil || *d.CreatorID != cid {
		response.NotFound(c, "剧集不存在")
		return nil, nil, false
	}
	return &ep, &d, true
}
