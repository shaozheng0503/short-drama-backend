// Package seed 给开发 / 联调环境批量灌入短剧、剧集、用户、订单等 mock 数据。
// 仅在 PAYMENT_DEV_MODE=true 或显式调用 /v1/dev/seed 时使用，生产严禁挂载。
package seed

import (
	"errors"
	"fmt"
	"log"
	"time"

	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Result 汇总本次 seed 写入 / 已存在的各模型数量，回给调用方查看效果。
type Result struct {
	Categories  int `json:"categories"`
	DramaTags   int `json:"drama_tags"`
	Creators    int `json:"creators"`
	Dramas      int `json:"dramas"`
	Episodes    int `json:"episodes"`
	Users       int `json:"users"`
	PlayHistory int `json:"play_history"`
	UserActions int `json:"user_actions"`
	Orders      int `json:"orders"`
	Unlocks     int `json:"unlocks"`
	Withdrawals int `json:"withdrawals"`
	Contracts   int `json:"contracts"`
}

// catKey 是 (type, name) 复合键，因为不同维度可能出现相同 name（例如「现代」既可能是背景也可能是别处的标签）。
type catKey struct {
	Type string
	Name string
}

// Run 是 seed 的唯一入口，幂等：已存在的记录按 phone / title 等业务键跳过。
// 写入顺序遵循 categories → creators → users → dramas → episodes → 互动 / 订单 / 资金。
// cfg 用于跑分账（CreatorShareRate）保持与 billing.MarkOrderPaid 一致，
// 避免 mock 订单造出 reconcile 检查不平的账面。
func Run(db *gorm.DB, cfg config.Config) (*Result, error) {
	r := &Result{}
	now := time.Now()

	cats, n, err := seedCategories(db)
	if err != nil {
		return nil, fmt.Errorf("seed categories: %w", err)
	}
	r.Categories = n

	creators, n, err := seedCreators(db, now)
	if err != nil {
		return nil, fmt.Errorf("seed creators: %w", err)
	}
	r.Creators = n

	users, n, err := seedUsers(db)
	if err != nil {
		return nil, fmt.Errorf("seed users: %w", err)
	}
	r.Users = n

	dramas, n, err := seedDramas(db, cats, creators, now)
	if err != nil {
		return nil, fmt.Errorf("seed dramas: %w", err)
	}
	r.Dramas = n

	n, err = seedDramaTags(db, cats, dramas)
	if err != nil {
		return nil, fmt.Errorf("seed drama tags: %w", err)
	}
	r.DramaTags = n

	episodes, n, err := seedEpisodes(db, dramas)
	if err != nil {
		return nil, fmt.Errorf("seed episodes: %w", err)
	}
	r.Episodes = n

	if err := refreshDramaTotals(db, dramas); err != nil {
		return nil, fmt.Errorf("refresh totals: %w", err)
	}

	n, err = seedUserActions(db, users, dramas)
	if err != nil {
		return nil, fmt.Errorf("seed user actions: %w", err)
	}
	r.UserActions = n

	n, err = seedPlayHistory(db, users, dramas, episodes)
	if err != nil {
		return nil, fmt.Errorf("seed play history: %w", err)
	}
	r.PlayHistory = n

	orders, n, err := seedOrders(db, cfg, users, dramas, episodes, now)
	if err != nil {
		return nil, fmt.Errorf("seed orders: %w", err)
	}
	r.Orders = n

	n, err = seedUnlocks(db, orders)
	if err != nil {
		return nil, fmt.Errorf("seed unlocks: %w", err)
	}
	r.Unlocks = n

	n, err = seedContracts(db, creators, dramas, now)
	if err != nil {
		return nil, fmt.Errorf("seed contracts: %w", err)
	}
	r.Contracts = n

	n, err = seedWithdrawals(db, creators, now)
	if err != nil {
		return nil, fmt.Errorf("seed withdrawals: %w", err)
	}
	r.Withdrawals = n

	log.Printf("[seed] done: %+v", r)
	return r, nil
}

// firstOrCreateByUnique 先按 where 条件查，找不到再写 attrs；GORM FirstOrCreate
// 在多次启动时偶发竞态，这里手动包一层避免重复唯一键错误。
func firstOrCreateByUnique[T any](db *gorm.DB, where map[string]interface{}, attrs T) (T, bool, error) {
	var existing T
	err := db.Where(where).First(&existing).Error
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return existing, false, err
	}
	if err := db.Create(&attrs).Error; err != nil {
		return attrs, false, err
	}
	return attrs, true, nil
}

// 红果短剧筛选维度：保持用户给定顺序，SortOrder = 数组长度 - index，方便首页按热度排。
// 4 个维度独立写入 categories.type；audience 只两项；background 中将「宫廷荒岛」拆成两项。
//
//nolint:gochecknoglobals // 静态常量表，常驻内存比每次构造便宜
var (
	themeNames = []string{
		"现言", "女性成长", "脑洞", "奇幻", "玄幻", "古言", "战神", "宫斗", "仙侠", "权谋",
		"年代爱情", "种田", "悬疑", "喜剧", "民国爱情", "志怪", "青春", "灵异", "家国情怀", "法律",
		"刑侦", "抗战", "武侠", "民国传奇", "求生", "动作", "科幻", "恐怖", "商战",
	}
	settingNames = []string{
		"打脸虐恋", "大女主", "大男主", "马甲", "重生", "穿越", "系统", "先婚后爱", "家长里短", "小人物",
		"神豪", "破镜重圆", "豪门", "强者回归", "虐恋", "传承觉醒", "异能", "医生", "赘婿逆袭", "强强联合",
		"甜宠", "娱乐圈", "青梅竹马", "神医", "姐弟恋", "追妻火葬场", "玄学", "业界精英", "一见钟情", "福宝",
		"捞偏门", "反派主角", "萌宠", "白月光", "双向救赎", "灵魂互换", "病娇", "方言", "暴富", "黑道",
		"丧失", "特种兵",
	}
	backgroundNames = []string{
		"现代", "都市", "古代", "乡村", "年代", "架空", "职场", "民国", "校园", "宫廷", "荒岛",
	}
	audienceNames = []string{"男频", "女频"} // 注：原文「男率」按红果命名习惯纠正为「男频」
)

// seedCategories 把 4 个维度一次性写进 categories；返回的 map 用 (type,name) 复合键索引。
func seedCategories(db *gorm.DB) (map[catKey]uint64, int, error) {
	dims := []struct {
		Type  string
		Names []string
	}{
		{model.CategoryTypeTheme, themeNames},
		{model.CategoryTypeSetting, settingNames},
		{model.CategoryTypeBackground, backgroundNames},
		{model.CategoryTypeAudience, audienceNames},
	}
	out := map[catKey]uint64{}
	created := 0
	for _, dim := range dims {
		// 排序：第一项最大，往后递减 — 这样首页 ORDER BY sort_order ASC 时（小的先）展示靠前的是热门
		// 等等：appHome 用的是 sort_order asc，所以小值=靠前。给第一项 1，往后增。
		for i, name := range dim.Names {
			c := model.Category{
				Type:      dim.Type,
				Name:      name,
				SortOrder: i + 1,
				Status:    model.StatusActive,
			}
			got, isNew, err := firstOrCreateByUnique(db, map[string]interface{}{
				"type": dim.Type,
				"name": name,
			}, c)
			if err != nil {
				return nil, 0, err
			}
			out[catKey{Type: dim.Type, Name: name}] = got.ID
			if isNew {
				created++
			}
		}
	}
	return out, created, nil
}

// seedCreators：初始账面**全部置 0**。
// 之前给固定值（1.2M / 800K 等）造出来的余额根本被任何 mock 订单"兜不住"，
// 跑 reconcile 一查必然报 creator_total_income_mismatch + creator_balance_formula_mismatch。
// 联调时财务数字靠真订单流（/orders → /dev/orders/:order_no/pay）累积更接近生产。
func seedCreators(db *gorm.DB, now time.Time) (map[string]uint64, int, error) {
	defs := []struct {
		Phone        string
		Name         string
		BankName     string
		Last4        string
		VerifyStatus string
	}{
		{"13800000001", "顾导演", "招商银行", "8421", model.CreatorVerifyVerified},
		{"13800000002", "苏编剧", "工商银行", "5566", model.CreatorVerifyVerified},
		{"13800000003", "林制片", "建设银行", "1234", model.CreatorVerifyPending},
	}
	out := map[string]uint64{}
	created := 0
	for _, d := range defs {
		c := model.Creator{
			Phone:            d.Phone,
			Name:             d.Name,
			BankName:         d.BankName,
			BankCardLast4:    d.Last4,
			VerifyStatus:     d.VerifyStatus,
			TotalIncomeCents: 0,
			BalanceCents:     0,
			FrozenCents:      0,
			Status:           model.StatusActive,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		got, isNew, err := firstOrCreateByUnique(db, map[string]interface{}{"phone": d.Phone}, c)
		if err != nil {
			return nil, 0, err
		}
		out[d.Phone] = got.ID
		if isNew {
			created++
		}
	}
	return out, created, nil
}

func seedUsers(db *gorm.DB) (map[string]uint64, int, error) {
	defs := []struct {
		Phone    string
		Nickname string
		Avatar   string
	}{
		{"13900000001", "追剧少女小A", "https://picsum.photos/seed/u1/200"},
		{"13900000002", "深夜剧迷", "https://picsum.photos/seed/u2/200"},
		{"13900000003", "甜宠爱好者", "https://picsum.photos/seed/u3/200"},
	}
	out := map[string]uint64{}
	created := 0
	for _, d := range defs {
		u := model.User{Phone: d.Phone, Nickname: d.Nickname, Avatar: d.Avatar, Status: model.StatusActive}
		got, isNew, err := firstOrCreateByUnique(db, map[string]interface{}{"phone": d.Phone}, u)
		if err != nil {
			return nil, 0, err
		}
		out[d.Phone] = got.ID
		if isNew {
			created++
		}
	}
	return out, created, nil
}

// dramaSpec：每部 mock 剧的元数据 + 红果 4 维标签。
// PrimaryTheme 同时写入 drama.category_id（旧接口靠它做单 FK 筛选）；
// 其它维度只走 drama_tags 关联。
type dramaSpec struct {
	Title         string
	Description   string
	Cover         string
	CreatorPhone  string
	FreeEpisodes  int
	PriceCents    int64
	EpisodeCount  int
	PlayCount     int64
	LikeCount     int64
	FavoriteCount int64
	SortOrder     int

	PrimaryTheme string   // 主题（单选，落 drama.category_id）
	Settings     []string // 设定（多选）
	Backgrounds  []string // 背景（多选）
	Audience     string   // 受众（单选：男频 / 女频）
}

func dramaSpecs() []dramaSpec {
	return []dramaSpec{
		{
			Title: "总裁的逆袭新娘", Description: "落魄千金重生归来，手撕渣男虐渣女，意外结识神秘总裁……",
			Cover: "https://picsum.photos/seed/drama1/600/900", CreatorPhone: "13800000001",
			FreeEpisodes: 3, PriceCents: 990, EpisodeCount: 12,
			PlayCount: 152_300, LikeCount: 18_240, FavoriteCount: 9_120, SortOrder: 100,
			PrimaryTheme: "现言",
			Settings:     []string{"豪门", "重生", "打脸虐恋", "破镜重圆"},
			Backgrounds:  []string{"现代", "都市"},
			Audience:     "女频",
		},
		{
			Title: "权倾朝野之摄政王妃", Description: "穿越成质子，被迫嫁给冷面摄政王，没想到他竟然……",
			Cover: "https://picsum.photos/seed/drama2/600/900", CreatorPhone: "13800000001",
			FreeEpisodes: 2, PriceCents: 1290, EpisodeCount: 16,
			PlayCount: 98_500, LikeCount: 12_300, FavoriteCount: 7_800, SortOrder: 95,
			PrimaryTheme: "古言",
			Settings:     []string{"穿越", "大女主", "先婚后爱", "马甲"},
			Backgrounds:  []string{"古代", "宫廷"},
			Audience:     "女频",
		},
		{
			Title: "深夜便利店凶案", Description: "一家通宵营业的便利店，每个深夜来访的客人都藏着秘密……",
			Cover: "https://picsum.photos/seed/drama3/600/900", CreatorPhone: "13800000002",
			FreeEpisodes: 1, PriceCents: 1990, EpisodeCount: 8,
			PlayCount: 67_200, LikeCount: 9_400, FavoriteCount: 5_100, SortOrder: 80,
			PrimaryTheme: "悬疑",
			Settings:     []string{"小人物", "捞偏门"},
			Backgrounds:  []string{"现代", "都市"},
			Audience:     "男频",
		},
		{
			Title: "外卖小哥竟是隐藏富二代", Description: "白天送外卖，晚上回家继承百亿资产，谁懂这种快乐？",
			Cover: "https://picsum.photos/seed/drama4/600/900", CreatorPhone: "13800000002",
			FreeEpisodes: 5, PriceCents: 590, EpisodeCount: 20,
			PlayCount: 213_500, LikeCount: 26_700, FavoriteCount: 14_200, SortOrder: 120,
			PrimaryTheme: "喜剧",
			Settings:     []string{"小人物", "神豪", "暴富", "强者回归", "传承觉醒"},
			Backgrounds:  []string{"现代", "都市"},
			Audience:     "男频",
		},
		{
			Title: "同桌大人请矜持", Description: "学霸校花×腹黑学神，前桌恋爱，后桌升学，全员甜炸。",
			Cover: "https://picsum.photos/seed/drama5/600/900", CreatorPhone: "13800000001",
			FreeEpisodes: 3, PriceCents: 790, EpisodeCount: 10,
			PlayCount: 88_700, LikeCount: 14_200, FavoriteCount: 8_300, SortOrder: 70,
			PrimaryTheme: "青春",
			Settings:     []string{"青梅竹马", "甜宠", "一见钟情"},
			Backgrounds:  []string{"校园"},
			Audience:     "女频",
		},
		{
			Title: "重回 2008（草稿）", Description: "重生回到 2008，握着未来 17 年的剧本，能不能把人生重写一遍？",
			Cover: "https://picsum.photos/seed/drama6/600/900", CreatorPhone: "13800000002",
			FreeEpisodes: 2, PriceCents: 0, EpisodeCount: 4, // 这部留 draft 状态
			PlayCount: 0, LikeCount: 0, FavoriteCount: 0, SortOrder: 50,
			PrimaryTheme: "年代爱情",
			Settings:     []string{"重生", "小人物"},
			Backgrounds:  []string{"年代"},
			Audience:     "男频",
		},
	}
}

func seedDramas(
	db *gorm.DB,
	cats map[catKey]uint64,
	creators map[string]uint64,
	now time.Time,
) (map[string]uint64, int, error) {
	out := map[string]uint64{}
	created := 0
	for _, spec := range dramaSpecs() {
		themeID := cats[catKey{Type: model.CategoryTypeTheme, Name: spec.PrimaryTheme}]
		creatorID := creators[spec.CreatorPhone]
		status := model.DramaStatusPublished
		var publishedAt *time.Time = ptrTime(now.Add(-time.Duration(spec.SortOrder) * time.Hour))
		if spec.Title == "重回 2008（草稿）" {
			status = model.DramaStatusDraft
			publishedAt = nil
		}
		d := model.Drama{
			Title:         spec.Title,
			Description:   spec.Description,
			CoverURL:      spec.Cover,
			CategoryID:    &themeID,
			CreatorID:     &creatorID,
			TotalEpisodes: spec.EpisodeCount, // 真实值 refreshDramaTotals 再覆盖
			FreeEpisodes:  spec.FreeEpisodes,
			PriceCents:    spec.PriceCents,
			Status:        status,
			PlayCount:     spec.PlayCount,
			LikeCount:     spec.LikeCount,
			FavoriteCount: spec.FavoriteCount,
			SortOrder:     spec.SortOrder,
			PublishedAt:   publishedAt,
		}
		got, isNew, err := firstOrCreateByUnique(db, map[string]interface{}{"title": spec.Title}, d)
		if err != nil {
			return nil, 0, err
		}
		// 兼容旧 seed：若上次跑写的是老 5 类，重跑时把 category_id 校准到新的红果 theme。
		if got.CategoryID == nil || *got.CategoryID != themeID {
			if err := db.Model(&model.Drama{}).Where("id = ?", got.ID).
				Update("category_id", themeID).Error; err != nil {
				return nil, 0, err
			}
		}
		out[spec.Title] = got.ID
		if isNew {
			created++
		}
	}
	return out, created, nil
}

// seedDramaTags：按 spec 把每部剧的 theme + settings + backgrounds + audience 写进 drama_tags。
// drama_tags 主键约束 (drama_id, category_id) 保证重复运行不会写重。
func seedDramaTags(
	db *gorm.DB,
	cats map[catKey]uint64,
	dramas map[string]uint64,
) (int, error) {
	created := 0
	for _, spec := range dramaSpecs() {
		dramaID, ok := dramas[spec.Title]
		if !ok {
			continue
		}
		// 4 维拼成一个 (type,name) 列表，统一写入
		assigns := make([]catKey, 0, 1+len(spec.Settings)+len(spec.Backgrounds)+1)
		assigns = append(assigns, catKey{Type: model.CategoryTypeTheme, Name: spec.PrimaryTheme})
		for _, s := range spec.Settings {
			assigns = append(assigns, catKey{Type: model.CategoryTypeSetting, Name: s})
		}
		for _, b := range spec.Backgrounds {
			assigns = append(assigns, catKey{Type: model.CategoryTypeBackground, Name: b})
		}
		if spec.Audience != "" {
			assigns = append(assigns, catKey{Type: model.CategoryTypeAudience, Name: spec.Audience})
		}

		for _, key := range assigns {
			catID, ok := cats[key]
			if !ok {
				// spec 写错或者维度名漏了 — 直接报错，免得静默丢标签
				return 0, fmt.Errorf("drama tag missing category %s/%s for drama %q", key.Type, key.Name, spec.Title)
			}
			tag := model.DramaTag{DramaID: dramaID, CategoryID: catID}
			_, isNew, err := firstOrCreateByUnique(db, map[string]interface{}{
				"drama_id":    dramaID,
				"category_id": catID,
			}, tag)
			if err != nil {
				return 0, err
			}
			if isNew {
				created++
			}
		}
	}
	return created, nil
}

// sampleVideos 是一组真实可播放的公开 mp4，混 w3.org / test-videos.co.uk / blender / vjs.zencdn.net 几家，
// 避免单一域名挂掉就全瞎。之前用过的 commondatastorage.googleapis.com/gtv-videos-bucket/* 已被设成 403，故弃。
// 全部经过 HEAD 验证 Content-Type=video/mp4 + 200。
var sampleVideos = []struct {
	URL      string
	Duration int // 秒，approx 真实时长
}{
	{"https://media.w3.org/2010/05/sintel/trailer.mp4", 52},
	{"https://media.w3.org/2010/05/bunny/trailer.mp4", 33},
	{"https://media.w3.org/2010/05/video/movie_300.mp4", 6},
	{"https://vjs.zencdn.net/v/oceans.mp4", 46},
	{"https://www.w3schools.com/html/mov_bbb.mp4", 10},
	{"https://test-videos.co.uk/vids/bigbuckbunny/mp4/h264/720/Big_Buck_Bunny_720_10s_1MB.mp4", 10},
	{"https://test-videos.co.uk/vids/sintel/mp4/h264/360/Sintel_360_10s_1MB.mp4", 10},
	{"https://download.blender.org/durian/trailer/sintel_trailer-1080p.mp4", 52},
}

// pickSample 给定剧 id + 集号，确定性地分一支真实视频，避免每次 seed 跳源。
func pickSample(dramaID uint64, episodeNo int) (string, int) {
	idx := (int(dramaID)*31 + episodeNo) % len(sampleVideos)
	if idx < 0 {
		idx += len(sampleVideos)
	}
	s := sampleVideos[idx]
	return s.URL, s.Duration
}

func seedEpisodes(
	db *gorm.DB,
	dramas map[string]uint64,
) (map[uint64][]model.Episode, int, error) {
	out := map[uint64][]model.Episode{}
	created := 0
	for _, spec := range dramaSpecs() {
		dramaID, ok := dramas[spec.Title]
		if !ok {
			continue
		}
		eps := make([]model.Episode, 0, spec.EpisodeCount)
		for i := 1; i <= spec.EpisodeCount; i++ {
			videoURL, duration := pickSample(dramaID, i)
			ep := model.Episode{
				DramaID:         dramaID,
				EpisodeNo:       i,
				Title:           fmt.Sprintf("第%d集", i),
				VODFileID:       fmt.Sprintf("mock-vod-%d-%02d", dramaID, i),
				VideoURL:        videoURL,
				DurationSeconds: duration,
				Status:          model.EpisodeStatusReady,
			}
			got, isNew, err := firstOrCreateByUnique(db, map[string]interface{}{
				"drama_id":   dramaID,
				"episode_no": i,
			}, ep)
			if err != nil {
				return nil, 0, err
			}
			eps = append(eps, got)
			if isNew {
				created++
			}
		}
		out[dramaID] = eps
	}
	return out, created, nil
}

// refreshDramaTotals 用真实剧集行数刷新 total_episodes，避免 spec 与实际不一致。
func refreshDramaTotals(db *gorm.DB, dramas map[string]uint64) error {
	for _, dramaID := range dramas {
		var cnt int64
		if err := db.Model(&model.Episode{}).Where("drama_id = ?", dramaID).Count(&cnt).Error; err != nil {
			return err
		}
		if err := db.Model(&model.Drama{}).Where("id = ?", dramaID).
			Update("total_episodes", cnt).Error; err != nil {
			return err
		}
	}
	return nil
}

// seedUserActions：用户 1 点赞 + 收藏剧 1、剧 4；用户 2 点赞剧 2；用户 3 收藏剧 5。
func seedUserActions(
	db *gorm.DB,
	users map[string]uint64,
	dramas map[string]uint64,
) (int, error) {
	pairs := []struct {
		UserPhone string
		Title     string
		Action    string
	}{
		{"13900000001", "总裁的逆袭新娘", model.ActionLike},
		{"13900000001", "总裁的逆袭新娘", model.ActionFavorite},
		{"13900000001", "外卖小哥竟是隐藏富二代", model.ActionFavorite},
		{"13900000002", "权倾朝野之摄政王妃", model.ActionLike},
		{"13900000003", "同桌大人请矜持", model.ActionFavorite},
	}
	created := 0
	for _, p := range pairs {
		uid, ok1 := users[p.UserPhone]
		did, ok2 := dramas[p.Title]
		if !ok1 || !ok2 {
			continue
		}
		ua := model.UserAction{UserID: uid, DramaID: did, Action: p.Action}
		_, isNew, err := firstOrCreateByUnique(db, map[string]interface{}{
			"user_id":  uid,
			"drama_id": did,
			"action":   p.Action,
		}, ua)
		if err != nil {
			return 0, err
		}
		if isNew {
			created++
		}
	}
	return created, nil
}

// seedPlayHistory：给用户 1 灌入剧 1 的看到第 2 集；用户 2 看到剧 4 第 3 集。
func seedPlayHistory(
	db *gorm.DB,
	users map[string]uint64,
	dramas map[string]uint64,
	episodes map[uint64][]model.Episode,
) (int, error) {
	pairs := []struct {
		UserPhone string
		Title     string
		EpisodeNo int
		Progress  int
	}{
		{"13900000001", "总裁的逆袭新娘", 2, 75},
		{"13900000001", "外卖小哥竟是隐藏富二代", 1, 30},
		{"13900000002", "外卖小哥竟是隐藏富二代", 3, 120},
		{"13900000003", "同桌大人请矜持", 2, 45},
	}
	created := 0
	for _, p := range pairs {
		uid, ok1 := users[p.UserPhone]
		did, ok2 := dramas[p.Title]
		if !ok1 || !ok2 {
			continue
		}
		eps := episodes[did]
		var ep *model.Episode
		for i := range eps {
			if eps[i].EpisodeNo == p.EpisodeNo {
				ep = &eps[i]
				break
			}
		}
		if ep == nil {
			continue
		}
		ph := model.PlayHistory{
			UserID:          uid,
			DramaID:         did,
			EpisodeID:       ep.ID,
			ProgressSeconds: p.Progress,
		}
		_, isNew, err := firstOrCreateByUnique(db, map[string]interface{}{
			"user_id":    uid,
			"episode_id": ep.ID,
		}, ph)
		if err != nil {
			return 0, err
		}
		if isNew {
			created++
		}
	}
	return created, nil
}

// 已写入订单的句柄；后面 seedUnlocks 用 OrderID 关联。
type seededOrder struct {
	Order   model.Order
	DramaID uint64
}

// seedOrders：给用户 1 一笔 paid 订单（用于解锁 + 跑通分账查询），用户 2 一笔 pending 订单。
// paid 订单同步 mirror billing.MarkOrderPaid 的副作用：bump creator total_income +
// balance，写入 creator_stats_daily，保证 reconcile 检查不出 mismatch。
func seedOrders(
	db *gorm.DB,
	cfg config.Config,
	users map[string]uint64,
	dramas map[string]uint64,
	episodes map[uint64][]model.Episode,
	now time.Time,
) ([]seededOrder, int, error) {
	specs := []struct {
		UserPhone string
		Title     string
		EpisodeNo int
		Status    string
		Method    string
		OrderNo   string
		PaidAfter time.Duration
	}{
		{"13900000001", "总裁的逆袭新娘", 4, model.OrderStatusPaid, model.PaymentMethodWechat, "MOCK-PAID-0001", -2 * time.Hour},
		{"13900000002", "外卖小哥竟是隐藏富二代", 6, model.OrderStatusPending, model.PaymentMethodAlipay, "MOCK-PEND-0002", 0},
	}
	out := []seededOrder{}
	created := 0
	for _, s := range specs {
		uid, ok1 := users[s.UserPhone]
		did, ok2 := dramas[s.Title]
		if !ok1 || !ok2 {
			continue
		}
		eps := episodes[did]
		var ep *model.Episode
		for i := range eps {
			if eps[i].EpisodeNo == s.EpisodeNo {
				ep = &eps[i]
				break
			}
		}
		if ep == nil {
			continue
		}
		var drama model.Drama
		if err := db.First(&drama, did).Error; err != nil {
			return nil, 0, err
		}
		expiredAt := ptrTime(now.Add(30 * time.Minute))
		var paidAt *time.Time
		if s.Status == model.OrderStatusPaid {
			paidAt = ptrTime(now.Add(s.PaidAfter))
		}
		ord := model.Order{
			OrderNo:       s.OrderNo,
			UserID:        uid,
			DramaID:       did,
			EpisodeID:     ep.ID,
			AmountCents:   drama.PriceCents,
			PaymentMethod: s.Method,
			Status:        s.Status,
			PaidAt:        paidAt,
			ExpiredAt:     expiredAt,
		}
		if s.Status == model.OrderStatusPaid {
			ord.PlatformTradeNo = "MOCK-TRADE-" + s.OrderNo
		}
		got, isNew, err := firstOrCreateByUnique(db, map[string]interface{}{"order_no": s.OrderNo}, ord)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, seededOrder{Order: got, DramaID: did})
		if isNew {
			created++
			// 仅在本次确实新创建了 paid 订单时，mirror billing.MarkOrderPaid 的副作用：
			// bump creator.total_income + balance + upsert creator_stats_daily。否则
			// reconcile 跑出来必报 creator_total_income_mismatch / creator_stats_income_mismatch。
			if got.Status == model.OrderStatusPaid && drama.CreatorID != nil {
				if err := applySeedPaidOrderToCreator(db, cfg, drama.CreatorID, did, got.AmountCents, paidAt); err != nil {
					return nil, 0, fmt.Errorf("apply seed paid order to creator: %w", err)
				}
			}
		}
	}
	return out, created, nil
}

// applySeedPaidOrderToCreator 复刻 billing.MarkOrderPaid 的分账副作用，专给 seed
// 用：不走完整事务（seed 数据本身就是大批量幂等写）、不锁行（无并发）、不解锁
// （seedUnlocks 单独处理）。只关心账面一致：
//   - creator.total_income += share、creator.balance += share
//   - creator_stats_daily 当日聚合 +share
func applySeedPaidOrderToCreator(db *gorm.DB, cfg config.Config, creatorID *uint64, dramaID uint64, amountCents int64, paidAt *time.Time) error {
	if creatorID == nil || paidAt == nil {
		return nil
	}
	share := int64(float64(amountCents) * cfg.CreatorShareRate)
	if share <= 0 {
		return nil
	}
	if err := db.Model(&model.Creator{}).
		Where("id = ?", *creatorID).
		Updates(map[string]interface{}{
			"total_income_cents": gorm.Expr("total_income_cents + ?", share),
			"balance_cents":      gorm.Expr("balance_cents + ?", share),
		}).Error; err != nil {
		return err
	}
	stat := model.CreatorStatsDaily{
		CreatorID:   *creatorID,
		DramaID:     dramaID,
		StatDate:    paidAt.Format("2006-01-02"),
		IncomeCents: share,
	}
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "creator_id"}, {Name: "drama_id"}, {Name: "stat_date"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"income_cents": gorm.Expr("creator_stats_daily.income_cents + ?", share),
		}),
	}).Create(&stat).Error
}

// seedUnlocks：根据上面 paid 的订单写入对应 episode_unlocks 行。
func seedUnlocks(db *gorm.DB, orders []seededOrder) (int, error) {
	created := 0
	for _, so := range orders {
		if so.Order.Status != model.OrderStatusPaid {
			continue
		}
		oid := so.Order.ID
		unlock := model.EpisodeUnlock{
			UserID:    so.Order.UserID,
			DramaID:   so.DramaID,
			EpisodeID: so.Order.EpisodeID,
			OrderID:   &oid,
		}
		_, isNew, err := firstOrCreateByUnique(db, map[string]interface{}{
			"user_id":    so.Order.UserID,
			"episode_id": so.Order.EpisodeID,
		}, unlock)
		if err != nil {
			return 0, err
		}
		if isNew {
			created++
		}
	}
	return created, nil
}

func seedContracts(
	db *gorm.DB,
	creators map[string]uint64,
	dramas map[string]uint64,
	now time.Time,
) (int, error) {
	specs := []struct {
		CreatorPhone string
		DramaTitle   string
		ContractNo   string
		Status       string
	}{
		{"13800000001", "总裁的逆袭新娘", "MOCK-CT-0001", model.ContractStatusSigned},
		{"13800000001", "权倾朝野之摄政王妃", "MOCK-CT-0002", model.ContractStatusSigning},
		{"13800000002", "深夜便利店凶案", "MOCK-CT-0003", model.ContractStatusPending},
	}
	created := 0
	for _, s := range specs {
		cid, ok1 := creators[s.CreatorPhone]
		did, ok2 := dramas[s.DramaTitle]
		if !ok1 || !ok2 {
			continue
		}
		dramaID := did
		c := model.Contract{
			CreatorID:   cid,
			DramaID:     &dramaID,
			ContractNo:  s.ContractNo,
			EsignFlowID: "mock-flow-" + s.ContractNo,
			FileURL:     "https://mock.cdn.example.com/contracts/" + s.ContractNo + ".pdf",
			Status:      s.Status,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		_, isNew, err := firstOrCreateByUnique(db, map[string]interface{}{"contract_no": s.ContractNo}, c)
		if err != nil {
			return 0, err
		}
		if isNew {
			created++
		}
	}
	return created, nil
}

func seedWithdrawals(db *gorm.DB, creators map[string]uint64, now time.Time) (int, error) {
	// 不再 seed 假提现：mock 创作者余额已置 0，假提现金额（200K / 100K / 50K）
	// 无法被任何 mock 订单兜底，跑 reconcile 必然报 creator_frozen_mismatch +
	// creator_balance_formula_mismatch。联调要看「提现列表 / 提现审核」效果，
	// 让创作者真的下单累积余额后，调 /v1/creator/withdrawals POST 自己造数据。
	specs := []struct {
		CreatorPhone string
		WithdrawalNo string
		Amount       int64
		Status       string
		BankName     string
		Card4        string
	}{}
	created := 0
	for _, s := range specs {
		cid, ok := creators[s.CreatorPhone]
		if !ok {
			continue
		}
		var paidAt, reviewedAt *time.Time
		if s.Status == model.WithdrawalStatusPaid {
			paidAt = ptrTime(now.Add(-24 * time.Hour))
			reviewedAt = ptrTime(now.Add(-25 * time.Hour))
		} else if s.Status == model.WithdrawalStatusApproved {
			reviewedAt = ptrTime(now.Add(-1 * time.Hour))
		}
		w := model.Withdrawal{
			WithdrawalNo:       s.WithdrawalNo,
			CreatorID:          cid,
			AmountCents:        s.Amount,
			BankNameSnapshot:   s.BankName,
			BankCardNoSnapshot: "****" + s.Card4,
			Status:             s.Status,
			Remark:             "mock seed",
			ReviewedAt:         reviewedAt,
			PaidAt:             paidAt,
		}
		if s.Status == model.WithdrawalStatusPaid {
			w.TransactionNo = "MOCK-TX-" + s.WithdrawalNo
		}
		_, isNew, err := firstOrCreateByUnique(db, map[string]interface{}{"withdrawal_no": s.WithdrawalNo}, w)
		if err != nil {
			return 0, err
		}
		if isNew {
			created++
		}
	}
	return created, nil
}

func ptrTime(t time.Time) *time.Time { return &t }
