package handler

import (
	"strings"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

const commentMaxLen = 1000

type createCommentRequest struct {
	Content   string  `json:"content"`
	EpisodeID *uint64 `json:"episode_id"` // 选填：传了=对该集发集评，不传=对整剧发剧评
}

// appListComments —— 评论列表。匿名也能看（OpenAPI 规定 security: bearerAuth 或空），
// 用户信息按 user_id 批量查；user 已删 / 已禁仍展示，但 nickname/avatar 退到默认。
// 区分剧评 / 集评：带 ?episode_id= 时只返回该集的集评；不带时只返回整剧的剧评（episode_id 为空）。
func (s *Server) appListComments(c *gin.Context) {
	dramaID := parseUint(c.Param("id"))
	if dramaID == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	// 校验剧存在且已上架（防爬未上架剧的评论）
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

	// episode_id 过滤：有值=集评，无值=剧评。
	q := s.db.Model(&model.Comment{}).Where("drama_id = ?", dramaID)
	if v := parseUint(c.Query("episode_id")); v > 0 {
		if !s.episodeBelongsToDrama(v, dramaID) {
			response.InvalidParam(c, "episode_id 与 drama_id 不匹配")
			return
		}
		q = q.Where("episode_id = ?", v)
	} else {
		q = q.Where("episode_id IS NULL")
	}

	page, pageSize := paginate(c)
	var total int64
	q.Count(&total)

	var comments []model.Comment
	if err := q.
		Order("created_at desc").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&comments).Error; err != nil {
		response.ServerError(c, "查询评论失败")
		return
	}

	// 批量拉评论所属用户，避免 N+1。
	userMap := s.collectCommentUsers(comments)
	views := make([]gin.H, 0, len(comments))
	for _, cm := range comments {
		views = append(views, commentView(cm, userMap[cm.UserID]))
	}
	response.OK(c, pageResp(views, page, pageSize, total))
}

// appCreateComment —— 发评论，要求 APP 用户登录态。
func (s *Server) appCreateComment(c *gin.Context) {
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

	var req createCommentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		response.InvalidParam(c, "content 不能为空")
		return
	}
	// 用 rune 计长度而非 byte，中文按字符算。
	if rl := len([]rune(content)); rl > commentMaxLen {
		response.InvalidParam(c, "content 过长（最多 1000 字）")
		return
	}

	// 集评：传了 episode_id 就校验该集属于本剧，存为集评；否则为剧评。
	var episodeID *uint64
	if req.EpisodeID != nil && *req.EpisodeID > 0 {
		if !s.episodeBelongsToDrama(*req.EpisodeID, dramaID) {
			response.InvalidParam(c, "episode_id 与 drama_id 不匹配")
			return
		}
		episodeID = req.EpisodeID
	}

	uid := middleware.CurrentID(c)
	cm := model.Comment{
		DramaID:   dramaID,
		EpisodeID: episodeID,
		UserID:    uid,
		Content:   content,
	}
	if err := s.db.Create(&cm).Error; err != nil {
		response.ServerError(c, "发表评论失败")
		return
	}

	// 把当前用户回填给响应，前端可直接渲染不用再拉一次。
	var u model.User
	_ = s.db.Select("id, nickname, avatar").First(&u, uid).Error
	response.OK(c, commentView(cm, &u))
}

// collectCommentUsers 批量拉评论用户；返回 user_id → *User（避免 nil 解引用）
func (s *Server) collectCommentUsers(comments []model.Comment) map[uint64]*model.User {
	out := map[uint64]*model.User{}
	if len(comments) == 0 {
		return out
	}
	ids := make([]uint64, 0, len(comments))
	seen := map[uint64]bool{}
	for _, cm := range comments {
		if !seen[cm.UserID] {
			ids = append(ids, cm.UserID)
			seen[cm.UserID] = true
		}
	}
	var users []model.User
	if err := s.db.Select("id, nickname, avatar").Where("id IN ?", ids).Find(&users).Error; err != nil {
		return out
	}
	for i := range users {
		out[users[i].ID] = &users[i]
	}
	return out
}

// episodeBelongsToDrama 校验剧集存在且归属该剧。
func (s *Server) episodeBelongsToDrama(episodeID, dramaID uint64) bool {
	var cnt int64
	s.db.Model(&model.Episode{}).Where("id = ? AND drama_id = ?", episodeID, dramaID).Count(&cnt)
	return cnt > 0
}

func commentView(cm model.Comment, u *model.User) gin.H {
	uv := gin.H{"id": cm.UserID, "nickname": "", "avatar": ""}
	if u != nil {
		uv["nickname"] = u.Nickname
		uv["avatar"] = u.Avatar
	}
	return gin.H{
		"id":         cm.ID,
		"episode_id": cm.EpisodeID, // null=剧评，有值=集评
		"user":       uv,
		"content":    cm.Content,
		"created_at": cm.CreatedAt,
	}
}
