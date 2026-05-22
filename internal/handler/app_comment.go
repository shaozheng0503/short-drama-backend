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
	Content string `json:"content"`
}

// appListComments —— 评论列表。匿名也能看（OpenAPI 规定 security: bearerAuth 或空），
// 用户信息按 user_id 批量查；user 已删 / 已禁仍展示，但 nickname/avatar 退到默认。
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

	page, pageSize := paginate(c)
	var total int64
	s.db.Model(&model.Comment{}).Where("drama_id = ?", dramaID).Count(&total)

	var comments []model.Comment
	if err := s.db.Where("drama_id = ?", dramaID).
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

	uid := middleware.CurrentID(c)
	cm := model.Comment{
		DramaID: dramaID,
		UserID:  uid,
		Content: content,
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

func commentView(cm model.Comment, u *model.User) gin.H {
	uv := gin.H{"id": cm.UserID, "nickname": "", "avatar": ""}
	if u != nil {
		uv["nickname"] = u.Nickname
		uv["avatar"] = u.Avatar
	}
	return gin.H{
		"id":         cm.ID,
		"user":       uv,
		"content":    cm.Content,
		"created_at": cm.CreatedAt,
	}
}
