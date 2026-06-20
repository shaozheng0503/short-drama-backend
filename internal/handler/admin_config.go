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

// effectiveFreeEpisodes 返回某部剧"当前生效"的免费集数（播放/计费判定的唯一口径）。
// 现行口径（2026-06 会议定）：统一读全局配置、改一次即时对所有剧（含已上架）生效；
// dramas.free_episodes 列保留但暂不参与判定（建剧时仍会写入，作为留存/未来用）。
// 将来要做单剧定制：drama 列有值则覆盖、为空跟随全局，只改这一处即可。
func (s *Server) effectiveFreeEpisodes(_ model.Drama) int {
	free, _ := s.pricingDefaults()
	return free
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

// aigcToolItem —— 单个可选 AIGC 创作工具：名称 + logo + 是否平台内置（自研）。
// Builtin 用于标记公司自研工具（如 LaguClaw），前端可加「自研」角标、置顶展示。
type aigcToolItem struct {
	Name    string `json:"name"`
	LogoURL string `json:"logo_url"`
	Builtin bool   `json:"builtin"`
}

// builtinAIGCTools 平台自研智能体，始终保证出现在工具列表里（公司要求 LaguClaw 必须展示，
// 即便历史配置里没有它）。logo 留空，由超管在「中台 → AIGC 工具配置」PUT 一次填入上线版 logo 后即以配置为准。
var builtinAIGCTools = []aigcToolItem{
	{Name: "LaguClaw", Builtin: true},
}

// 默认 AIGC 工具列表（无配置时回落）。
var defaultAIGCToolItems = []aigcToolItem{
	{Name: "LaguClaw", Builtin: true},
	{Name: "即梦"},
	{Name: "小云雀"},
	{Name: "可灵"},
	{Name: "海螺"},
	{Name: "Sora"},
	{Name: "Runway"},
	{Name: "其他"},
}

// aigcToolItems 读取已配置的 AIGC 工具列表（含 logo），并保证自研工具(LaguClaw)始终在列、置顶。
// 兼容两种存储格式：新版对象数组 [{name,logo_url,builtin}]；旧版纯字符串数组 ["即梦",...]。
// 未配置 / 解析失败时回落默认值。
func (s *Server) aigcToolItems() []aigcToolItem {
	return ensureBuiltinTools(s.aigcToolItemsRaw())
}

func (s *Server) aigcToolItemsRaw() []aigcToolItem {
	var gc model.GlobalConfig
	if err := s.db.First(&gc, "key = ?", model.ConfigKeyAIGCTools).Error; err != nil {
		return defaultAIGCToolItems
	}
	raw := strings.TrimSpace(gc.Value)
	// 新版：对象数组
	var items []aigcToolItem
	if err := json.Unmarshal([]byte(raw), &items); err == nil && len(items) > 0 && items[0].Name != "" {
		return items
	}
	// 旧版：纯字符串数组，平滑兼容历史配置
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err == nil && len(names) > 0 {
		out := make([]aigcToolItem, 0, len(names))
		for _, n := range names {
			out = append(out, aigcToolItem{Name: n})
		}
		return out
	}
	return defaultAIGCToolItems
}

// ensureBuiltinTools 把缺失的自研工具置顶补进列表（按名称大小写不敏感判重）。
// 若配置里已有同名项（比如超管已为 LaguClaw 配了 logo），保留配置项、不重复注入。
func ensureBuiltinTools(items []aigcToolItem) []aigcToolItem {
	for i := len(builtinAIGCTools) - 1; i >= 0; i-- {
		b := builtinAIGCTools[i]
		found := false
		for _, it := range items {
			if strings.EqualFold(strings.TrimSpace(it.Name), b.Name) {
				found = true
				break
			}
		}
		if !found {
			items = append([]aigcToolItem{b}, items...)
		}
	}
	return items
}

// getAIGCTools —— GET /v1/common/aigc-tools（App / 创作者中台拉取可选工具）。
// 同时返回 tools（名称数组，老客户端）与 items（含 logo / builtin，新客户端渲染图标）。
func (s *Server) getAIGCTools(c *gin.Context) {
	items := s.aigcToolItems()
	response.OK(c, gin.H{"tools": toolNames(items), "items": items})
}

func toolNames(items []aigcToolItem) []string {
	names := make([]string, 0, len(items))
	for _, it := range items {
		names = append(names, it.Name)
	}
	return names
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
	DefaultBP *int           `json:"default_bp"`
	Channels  map[string]int `json:"channels"` // 渠道名 -> 基点
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
	items := s.aigcToolItems()
	response.OK(c, gin.H{"tools": toolNames(items), "items": items})
}

// aigcToolsConfigRequest 兼容两种入参：
//   - items：对象数组 [{name, logo_url, builtin}]，可配 logo / 自研标记（新版，推荐）。
//   - tools：纯名称数组 ["即梦",...]（老版，无 logo）。
//
// 两者都传时以 items 为准；都不传 / 清洗后为空则报错。
type aigcToolsConfigRequest struct {
	Items []aigcToolItem `json:"items"`
	Tools []string       `json:"tools"`
}

// adminUpdateAIGCTools —— PUT /v1/admin/config/aigc-tools（仅超管）
func (s *Server) adminUpdateAIGCTools(c *gin.Context) {
	var req aigcToolsConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法：需 items 对象数组或 tools 字符串数组")
		return
	}
	// 统一归一到对象数组：优先 items，其次把 tools 名称数组升格成对象。
	raw := req.Items
	if len(raw) == 0 {
		for _, n := range req.Tools {
			raw = append(raw, aigcToolItem{Name: n})
		}
	}
	cleaned := make([]aigcToolItem, 0, len(raw))
	seen := map[string]bool{}
	for _, it := range raw {
		name := strings.TrimSpace(it.Name)
		if name == "" {
			continue
		}
		if len([]rune(name)) > 64 {
			response.InvalidParam(c, "单个工具名不能超过 64 个字")
			return
		}
		logo := strings.TrimSpace(it.LogoURL)
		if len(logo) > 512 {
			response.InvalidParam(c, "logo_url 过长（最多 512 字符）")
			return
		}
		if seen[name] { // 同名去重，保留首次出现
			continue
		}
		seen[name] = true
		cleaned = append(cleaned, aigcToolItem{Name: name, LogoURL: logo, Builtin: it.Builtin})
	}
	if len(cleaned) == 0 {
		response.InvalidParam(c, "至少配置一个工具")
		return
	}
	encoded, err := json.Marshal(cleaned)
	if err != nil {
		response.ServerError(c, "序列化失败")
		return
	}
	if err := s.setConfigTx(s.db, model.ConfigKeyAIGCTools, string(encoded)); err != nil {
		response.ServerError(c, "保存配置失败")
		return
	}
	// 响应与 GET 口径一致：补上始终展示的自研工具。
	effective := ensureBuiltinTools(cleaned)
	response.OK(c, gin.H{"tools": toolNames(effective), "items": effective})
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
