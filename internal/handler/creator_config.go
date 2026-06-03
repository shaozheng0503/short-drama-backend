package handler

import (
	"github.com/gin-gonic/gin"

	"ai-drama-platform/internal/response"
)

// creatorGetPricingConfig —— 创作者侧读取全局价格默认值（免费集数 / 单价分）。
// 用于建剧 / 改剧表单预填默认值；服务端建剧已会自动用 pricingDefaults() 兜底
// (creator_drama.go)，本接口只是把同一份默认值暴露给前端展示。
// 逻辑与 admin 完全一致，直接复用 adminGetPricingConfig；只读，不开放写接口
// (写仍是 super 超管专属)。
func (s *Server) creatorGetPricingConfig(c *gin.Context) {
	s.adminGetPricingConfig(c)
}

// creatorGetCoverSpecs —— 漫剧封面上传规格清单。
// 对应需求「封面（可多个，前端上传前需裁剪）：后端返回列表，前端限制比例/分辨率/大小/格式」。
// 后端只下发规格常量；比例/分辨率校验在前端（拿得到图片宽高+做裁剪），后端不读图片字节。
// 上传签名接口 POST /creator/uploads/image-sign 会按 formats 校验后缀（含 bmp）。
func (s *Server) creatorGetCoverSpecs(c *gin.Context) {
	response.OK(c, gin.H{
		"max_count":   dramaMaxCovers, // 每部剧最多封面数
		"max_size_mb": 5,
		"specs": []gin.H{
			{
				"ratio":       "7:10",
				"ratio_w":     7,
				"ratio_h":     10,
				"min_width":   350,
				"min_height":  500,
				"max_size_mb": 5,
				"formats":     []string{"jpg", "jpeg", "png", "bmp"},
			},
			{
				"ratio":       "2:3",
				"ratio_w":     2,
				"ratio_h":     3,
				"min_width":   480,
				"min_height":  720,
				"max_size_mb": 5,
				"formats":     []string{"jpg", "jpeg", "png"},
			},
		},
	})
}
