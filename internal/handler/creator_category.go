package handler

import "github.com/gin-gonic/gin"

// creatorListCategories —— 创作者侧获取剧集分类（只读）。
// 创作者建 / 改剧的分类下拉需要它；列表逻辑与 admin 完全一致，直接复用 adminListCategories，
// 区别只是鉴权挂在 creator 组上——避免前端用 creator 身份调 /v1/admin/categories 被中间件
// 以 40301「身份与接口不匹配」拦掉。支持同样的 ?type= 过滤。
func (s *Server) creatorListCategories(c *gin.Context) {
	s.adminListCategories(c)
}
