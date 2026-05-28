package handler

import "github.com/gin-gonic/gin"

// appListCategories —— APP 端获取剧集分类列表（公开，无需登录）。
// 用于首页 / 列表页的分类 tab 渲染、按 `?category_id=` 过滤剧集列表的下拉来源。
// 逻辑与 admin 完全一致，直接复用 adminListCategories；分类只是目录元数据、不含敏感
// 信息，匿名访客也要能浏览，所以挂在公开 `app` 组、不挂 appAuth。
func (s *Server) appListCategories(c *gin.Context) {
	s.adminListCategories(c)
}
