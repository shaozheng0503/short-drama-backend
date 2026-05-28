package handler

import "github.com/gin-gonic/gin"

// creatorGetPricingConfig —— 创作者侧读取全局价格默认值（免费集数 / 单价分）。
// 用于建剧 / 改剧表单预填默认值；服务端建剧已会自动用 pricingDefaults() 兜底
// (creator_drama.go)，本接口只是把同一份默认值暴露给前端展示。
// 逻辑与 admin 完全一致，直接复用 adminGetPricingConfig；只读，不开放写接口
// (写仍是 super 超管专属)。
func (s *Server) creatorGetPricingConfig(c *gin.Context) {
	s.adminGetPricingConfig(c)
}
