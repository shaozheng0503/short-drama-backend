package handler

import (
	"strconv"
	"time"

	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// getConfigInt 读全局配置整型值，缺省 / 解析失败时返回 def。
func (s *Server) getConfigInt(key string, def int64) int64 {
	var gc model.GlobalConfig
	if err := s.db.First(&gc, "key = ?", key).Error; err != nil {
		return def
	}
	v, err := strconv.ParseInt(gc.Value, 10, 64)
	if err != nil {
		return def
	}
	return v
}

// pricingDefaults 返回全局默认的（免费集数, 每集单价分）。未配置时为 (0, 0)。
// 创作者新建短剧时用它兜底，创作者可在表单里覆盖。
func (s *Server) pricingDefaults() (int, int64) {
	free := s.getConfigInt(model.ConfigKeyFreeEpisodes, 0)
	price := s.getConfigInt(model.ConfigKeyPriceCents, 0)
	return int(free), price
}

// adminGetPricingConfig —— GET /v1/admin/config/pricing
func (s *Server) adminGetPricingConfig(c *gin.Context) {
	free, price := s.pricingDefaults()
	response.OK(c, gin.H{
		"free_episodes": free,
		"price_cents":   price,
	})
}

type pricingConfigRequest struct {
	FreeEpisodes *int   `json:"free_episodes"`
	PriceCents   *int64 `json:"price_cents"`
}

// adminUpdatePricingConfig —— PUT /v1/admin/config/pricing（仅超管）
func (s *Server) adminUpdatePricingConfig(c *gin.Context) {
	var req pricingConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}
	if req.FreeEpisodes != nil && *req.FreeEpisodes < 0 {
		response.InvalidParam(c, "free_episodes 不能为负")
		return
	}
	if req.PriceCents != nil && *req.PriceCents < 0 {
		response.InvalidParam(c, "price_cents 不能为负")
		return
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if req.FreeEpisodes != nil {
			if err := s.setConfigTx(tx, model.ConfigKeyFreeEpisodes, strconv.Itoa(*req.FreeEpisodes)); err != nil {
				return err
			}
		}
		if req.PriceCents != nil {
			if err := s.setConfigTx(tx, model.ConfigKeyPriceCents, strconv.FormatInt(*req.PriceCents, 10)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		response.ServerError(c, "保存配置失败")
		return
	}
	free, price := s.pricingDefaults()
	response.OK(c, gin.H{"free_episodes": free, "price_cents": price})
}

func (s *Server) setConfigTx(tx *gorm.DB, key, value string) error {
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&model.GlobalConfig{Key: key, Value: value, UpdatedAt: time.Now()}).Error
}
