package handler

import (
	"encoding/json"
	"strconv"
	"strings"
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

// === AIGC 创作工具列表配置 ===

// 默认 AIGC 工具列表（无配置时回落）。
var defaultAIGCTools = []string{"即梦", "小云雀", "可灵", "海螺", "Sora", "Runway", "其他"}

// aigcTools 读取已配置的 AIGC 工具列表；未配置 / 解析失败时回落默认值。
func (s *Server) aigcTools() []string {
	var gc model.GlobalConfig
	if err := s.db.First(&gc, "key = ?", model.ConfigKeyAIGCTools).Error; err != nil {
		return defaultAIGCTools
	}
	var tools []string
	if err := json.Unmarshal([]byte(gc.Value), &tools); err != nil || len(tools) == 0 {
		return defaultAIGCTools
	}
	return tools
}

// getAIGCTools —— GET /v1/common/aigc-tools（App / 创作者中台拉取可选工具）。
func (s *Server) getAIGCTools(c *gin.Context) {
	response.OK(c, gin.H{"tools": s.aigcTools()})
}

// === 渠道收益分成比例配置 ===

// channelShareRatioBP 返回某渠道的分成比例基点：先查渠道专属配置，再查默认配置；
// 都没有时返回 (0, false) —— 调用方据此决定是否回落 100% 并给警告。
func (s *Server) channelShareRatioBP(channel string) (int, bool) {
	if v, ok := s.configBP(model.ConfigKeyIncomeSharePrefix + channel); ok {
		return v, true
	}
	if v, ok := s.configBP(model.ConfigKeyIncomeShareDefault); ok {
		return v, true
	}
	return 0, false
}

// configBP 读一个基点整数配置。
func (s *Server) configBP(key string) (int, bool) {
	var gc model.GlobalConfig
	if err := s.db.First(&gc, "key = ?", key).Error; err != nil {
		return 0, false
	}
	v, err := strconv.Atoi(strings.TrimSpace(gc.Value))
	if err != nil || v < 0 || v > model.ShareRatioBPFull {
		return 0, false
	}
	return v, true
}

// adminGetIncomeShareConfig —— GET /v1/admin/config/income-share
// 返回默认比例 + 各渠道专属比例（基点）。
func (s *Server) adminGetIncomeShareConfig(c *gin.Context) {
	var gcs []model.GlobalConfig
	s.db.Where("key = ? OR key LIKE ?", model.ConfigKeyIncomeShareDefault, model.ConfigKeyIncomeSharePrefix+"%").Find(&gcs)
	channels := map[string]int{}
	defaultBP := 0
	hasDefault := false
	for _, gc := range gcs {
		v, err := strconv.Atoi(strings.TrimSpace(gc.Value))
		if err != nil {
			continue
		}
		if gc.Key == model.ConfigKeyIncomeShareDefault {
			defaultBP = v
			hasDefault = true
			continue
		}
		channel := strings.TrimPrefix(gc.Key, model.ConfigKeyIncomeSharePrefix)
		if channel != "" {
			channels[channel] = v
		}
	}
	response.OK(c, gin.H{
		"default_bp":  defaultBP,
		"has_default": hasDefault,
		"channels":    channels,
		"note":        "基点：10000=100%，5000=50%。Excel 行内填了比例以行内为准。",
	})
}

type incomeShareConfigRequest struct {
	DefaultBP *int            `json:"default_bp"`
	Channels  map[string]int  `json:"channels"` // 渠道名 -> 基点
}

// adminUpdateIncomeShareConfig —— PUT /v1/admin/config/income-share（仅超管）
func (s *Server) adminUpdateIncomeShareConfig(c *gin.Context) {
	var req incomeShareConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}
	validBP := func(v int) bool { return v >= 0 && v <= model.ShareRatioBPFull }
	if req.DefaultBP != nil && !validBP(*req.DefaultBP) {
		response.InvalidParam(c, "default_bp 须在 0~10000 之间")
		return
	}
	for ch, v := range req.Channels {
		if strings.TrimSpace(ch) == "" {
			response.InvalidParam(c, "渠道名不能为空")
			return
		}
		if !validBP(v) {
			response.InvalidParam(c, "渠道 "+ch+" 的比例须在 0~10000 之间")
			return
		}
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if req.DefaultBP != nil {
			if err := s.setConfigTx(tx, model.ConfigKeyIncomeShareDefault, strconv.Itoa(*req.DefaultBP)); err != nil {
				return err
			}
		}
		for ch, v := range req.Channels {
			if err := s.setConfigTx(tx, model.ConfigKeyIncomeSharePrefix+strings.TrimSpace(ch), strconv.Itoa(v)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		response.ServerError(c, "保存配置失败")
		return
	}
	s.adminGetIncomeShareConfig(c)
}

// adminGetAIGCTools —— GET /v1/admin/config/aigc-tools
func (s *Server) adminGetAIGCTools(c *gin.Context) {
	response.OK(c, gin.H{"tools": s.aigcTools()})
}

type aigcToolsConfigRequest struct {
	Tools []string `json:"tools" binding:"required"`
}

// adminUpdateAIGCTools —— PUT /v1/admin/config/aigc-tools（仅超管）
func (s *Server) adminUpdateAIGCTools(c *gin.Context) {
	var req aigcToolsConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "tools 必填且为字符串数组")
		return
	}
	cleaned := make([]string, 0, len(req.Tools))
	for _, t := range req.Tools {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if len([]rune(t)) > 64 {
			response.InvalidParam(c, "单个工具名不能超过 64 个字")
			return
		}
		cleaned = append(cleaned, t)
	}
	if len(cleaned) == 0 {
		response.InvalidParam(c, "至少配置一个工具")
		return
	}
	raw, err := json.Marshal(cleaned)
	if err != nil {
		response.ServerError(c, "序列化失败")
		return
	}
	if err := s.setConfigTx(s.db, model.ConfigKeyAIGCTools, string(raw)); err != nil {
		response.ServerError(c, "保存配置失败")
		return
	}
	response.OK(c, gin.H{"tools": cleaned})
}

// === 搜索框推荐 / 热搜词 ===

var defaultHotKeywords = []string{"逆袭", "都市", "甜宠", "悬疑", "古风"}

// hotKeywords 读已配置的热搜词；未配置/解析失败回落默认。
func (s *Server) hotKeywords() []string {
	var gc model.GlobalConfig
	if err := s.db.First(&gc, "key = ?", model.ConfigKeyHotSearch).Error; err != nil {
		return defaultHotKeywords
	}
	var kw []string
	if err := json.Unmarshal([]byte(gc.Value), &kw); err != nil || len(kw) == 0 {
		return defaultHotKeywords
	}
	return kw
}

// getHotSearch —— GET /v1/app/search/hot（搜索框推荐词）。
func (s *Server) getHotSearch(c *gin.Context) {
	response.OK(c, gin.H{"keywords": s.hotKeywords()})
}

// adminGetHotSearch —— GET /v1/admin/config/hot-search
func (s *Server) adminGetHotSearch(c *gin.Context) {
	response.OK(c, gin.H{"keywords": s.hotKeywords()})
}

type hotSearchConfigRequest struct {
	Keywords []string `json:"keywords" binding:"required"`
}

// adminUpdateHotSearch —— PUT /v1/admin/config/hot-search（仅超管）
func (s *Server) adminUpdateHotSearch(c *gin.Context) {
	var req hotSearchConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "keywords 必填且为字符串数组")
		return
	}
	cleaned := make([]string, 0, len(req.Keywords))
	for _, k := range req.Keywords {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if len([]rune(k)) > 32 {
			response.InvalidParam(c, "单个热搜词不能超过 32 个字")
			return
		}
		cleaned = append(cleaned, k)
	}
	if len(cleaned) == 0 {
		response.InvalidParam(c, "至少配置一个热搜词")
		return
	}
	raw, err := json.Marshal(cleaned)
	if err != nil {
		response.ServerError(c, "序列化失败")
		return
	}
	if err := s.setConfigTx(s.db, model.ConfigKeyHotSearch, string(raw)); err != nil {
		response.ServerError(c, "保存配置失败")
		return
	}
	response.OK(c, gin.H{"keywords": cleaned})
}
