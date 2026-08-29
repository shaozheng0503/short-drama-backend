package billing

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"ai-drama-platform/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 看广告解锁（穿山甲激励视频 SSV）相关错误。
var (
	ErrAdUnlockDisabled  = errors.New("该剧未开启看广告解锁")
	ErrTicketNotFound    = errors.New("广告解锁凭证不存在")
	ErrTicketNotOwned    = errors.New("广告解锁凭证不属于当前用户")
	ErrTicketExpired     = errors.New("广告解锁凭证已过期")
	ErrTicketNotPending  = errors.New("广告解锁凭证状态不允许该操作")
	ErrAdUnlockNotConfig = errors.New("穿山甲看广告解锁未配置（缺少 SecurityKey）")
	ErrCSJSignInvalid    = errors.New("穿山甲回调验签失败")
	ErrCSJTransDuplicate = errors.New("穿山甲回调流水重复")
	ErrCSJUserMismatch   = errors.New("回调 user_id 与凭证不匹配")
)

// CSJVerifyResult 验签 + 处理结果，供 handler 层决定响应。
type CSJVerifyResult struct {
	// Valid 验签是否通过（穿山甲协议：响应 {"isValid": bool}）。
	Valid bool
	// TicketID 处理成功的凭证（仅日志/排查用）。
	TicketID string
	// Status ticket 终态：rewarded / duplicate。
	Status string
}

// CreateAdUnlockTicket App 创建看广告解锁凭证。
// 前置校验（与下单同口径）：
//   - 剧存在且已上架
//   - 集存在、属于该剧、已就绪
//   - 该剧 admin 已开启 ad_unlock_enabled
//   - 该集非免费集、未解锁（已解锁直接返回 AlreadyUnlocked）
//   - 未配置 SecurityKey 时拒绝创建（联调期可用 AD_UNLOCK_DEV_MODE=true 放行）
func (s *Service) CreateAdUnlockTicket(userID, dramaID, episodeID uint64) (*model.AdUnlockTicket, bool, error) {
	var ep model.Episode
	if err := s.db.First(&ep, episodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, ErrEpisodeNotFound
		}
		return nil, false, err
	}
	if ep.DramaID != dramaID {
		return nil, false, ErrOrderEpisodeMatch
	}
	if ep.Status != model.EpisodeStatusReady {
		return nil, false, ErrEpisodeNotReady
	}
	var drama model.Drama
	if err := s.db.First(&drama, dramaID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, ErrDramaNotFound
		}
		return nil, false, err
	}
	if drama.Status != model.DramaStatusPublished {
		return nil, false, ErrDramaNotAvailable
	}
	// admin 手动开关：未开启（或 NULL 默认关闭）不允许看广告解锁
	if drama.AdUnlockEnabled == nil || !*drama.AdUnlockEnabled {
		return nil, false, ErrAdUnlockDisabled
	}
	if ep.EpisodeNo <= s.effectiveFreeEpisodes(s.db, drama) {
		return nil, false, ErrEpisodeFree
	}
	// 已解锁直接返回（用户可能已付费解锁过同一集）
	var unlock model.EpisodeUnlock
	if err := s.db.Where("user_id = ? AND episode_id = ?", userID, episodeID).First(&unlock).Error; err == nil {
		return nil, true, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	// 生产校验：SecurityKey 必须配置才允许发凭证（dev 模式跳过，便于前端先跑通 UI）
	if s.cfg.CSJSecurityKey == "" && !s.cfg.AdUnlockDevMode {
		return nil, false, ErrAdUnlockNotConfig
	}

	now := time.Now()
	expireAt := now.Add(s.cfg.AdUnlockTicketTTL)
	ticket := model.AdUnlockTicket{
		TicketID:  generateAdTicketID(),
		UserID:    userID,
		DramaID:   dramaID,
		EpisodeID: episodeID,
		Status:    model.AdTicketStatusPending,
		ExpireAt:  &expireAt,
	}
	if err := s.db.Create(&ticket).Error; err != nil {
		return nil, false, err
	}
	return &ticket, false, nil
}

// GetAdUnlockTicket App 查询解锁结果：ticket 必须属于当前用户。
// 查询时顺带做懒过期：pending 且已过 expire_at → 置 expired（不返回奖励）。
func (s *Service) GetAdUnlockTicket(userID uint64, ticketID string) (*model.AdUnlockTicket, bool, error) {
	var t model.AdUnlockTicket
	if err := s.db.Where("ticket_id = ?", ticketID).First(&t).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, ErrTicketNotFound
		}
		return nil, false, err
	}
	if t.UserID != userID {
		return nil, false, ErrTicketNotOwned
	}
	// 懒过期：pending 超时 → expired（App 端表现为「已过期，请重新发起」）
	if t.Status == model.AdTicketStatusPending && t.ExpireAt != nil && t.ExpireAt.Before(time.Now()) {
		if err := s.db.Model(&model.AdUnlockTicket{}).
			Where("id = ? AND status = ?", t.ID, model.AdTicketStatusPending).
			Update("status", model.AdTicketStatusExpired).Error; err != nil {
			return nil, false, err
		}
		t.Status = model.AdTicketStatusExpired
	}
	// unlocked：rewarded 状态 或 episode_unlocks 已有记录（历史付费解锁也返回 true，前端直接播）
	unlocked := t.Status == model.AdTicketStatusRewarded
	if !unlocked {
		var cnt int64
		if err := s.db.Model(&model.EpisodeUnlock{}).
			Where("user_id = ? AND episode_id = ?", t.UserID, t.EpisodeID).
			Count(&cnt).Error; err == nil && cnt > 0 {
			unlocked = true
		}
	}
	return &t, unlocked, nil
}

// HandleCSJRewardCallback 处理穿山甲/GroMore 激励视频服务端奖励验证回调（GET）。
//
// 穿山甲官方协议（服务端奖励验证）：
//   - 参数：user_id（请求广告时 SDK 传入的透传串）、trans_id（平台侧唯一流水）、
//     reward_name、reward_amount、extra、sign
//   - 签名：sign = sha256(SecurityKey + ":" + trans_id) 的 hex 小写
//   - 响应：{"isValid": true} 表示业务已接收，false / 非 200 时穿山甲会重试
//
// GroMore 额外透传 ecpm（string，无数据时 null/空）：本次展示的 eCPM，
// 单次收益（分）= ecpm × 单位换算（CSJ_ECPM_UNIT，默认分）÷ 1000。
// 收益写入 channel_income_daily（渠道=狼之短剧，batch_no=ad_auto），
// 与内购 app_auto 同口径参与创作者分成（CREATOR_SHARE_RATE）与结算。
// ecpm 缺失/解析失败只记日志，不影响解锁主流程。
//
// 幂等设计（三层）：
//  1. trans_id 唯一索引兜底：并发重复回调只落一条
//  2. ticket 状态机：pending → rewarded 单向；重复回调发现已 rewarded 直接返回 isValid=true
//  3. episode_unlocks 唯一索引：最终解锁落库 OnConflict DoNothing
//
// 本方法只返回业务处理结果；HTTP 响应由 handler 层按穿山甲协议组装。
func (s *Service) HandleCSJRewardCallback(userID, transID, rewardName, extra, sign, ecpm string) (CSJVerifyResult, error) {
	// 1. 验签（dev 模式跳过，仅限联调）
	if s.cfg.AdUnlockDevMode {
		log.Printf("[ad-unlock] DEV MODE：跳过穿山甲验签 trans_id=%s", transID)
	} else {
		if s.cfg.CSJSecurityKey == "" {
			return CSJVerifyResult{Valid: false}, ErrAdUnlockNotConfig
		}
		expect := csjSign(s.cfg.CSJSecurityKey, transID)
		if sign == "" || !csjSecureCompare(sign, expect) {
			return CSJVerifyResult{Valid: false}, ErrCSJSignInvalid
		}
	}

	// 2. user_id 即业务透传的 ticket_id：定位凭证
	if userID == "" {
		return CSJVerifyResult{Valid: false}, ErrTicketNotFound
	}
	var t model.AdUnlockTicket
	if err := s.db.Where("ticket_id = ?", userID).First(&t).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return CSJVerifyResult{Valid: false}, ErrTicketNotFound
		}
		return CSJVerifyResult{Valid: false}, err
	}

	// 3. 事务：状态机推进 + 解锁落库（行锁防并发重复回调）
	var result CSJVerifyResult
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 行锁读最新状态
		var locked model.AdUnlockTicket
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&locked, t.ID).Error; err != nil {
			return err
		}
		// 已发奖 → 幂等返回（穿山甲重试场景：直接确认，避免无限重试）
		if locked.Status == model.AdTicketStatusRewarded {
			result = CSJVerifyResult{Valid: true, TicketID: locked.TicketID, Status: model.AdTicketStatusRewarded}
			return nil
		}
		// 非 pending（expired 等）→ 拒绝发奖，但仍回 isValid=false 让穿山甲停止重试
		if locked.Status != model.AdTicketStatusPending {
			result = CSJVerifyResult{Valid: false, TicketID: locked.TicketID, Status: locked.Status}
			return nil
		}
		// 过期校验
		if locked.ExpireAt != nil && locked.ExpireAt.Before(time.Now()) {
			if err := tx.Model(&model.AdUnlockTicket{}).
				Where("id = ?", locked.ID).
				Updates(map[string]interface{}{
					"status":   model.AdTicketStatusExpired,
					"trans_id": transID,
				}).Error; err != nil {
				return err
			}
			result = CSJVerifyResult{Valid: false, TicketID: locked.TicketID, Status: model.AdTicketStatusExpired}
			return nil
		}
		// trans_id 幂等：同流水已绑定到其他 ticket → 拒绝（唯一索引兜底并发）
		var dupCnt int64
		if err := tx.Model(&model.AdUnlockTicket{}).
			Where("trans_id = ? AND id <> ?", transID, locked.ID).
			Count(&dupCnt).Error; err != nil {
			return err
		}
		if dupCnt > 0 {
			result = CSJVerifyResult{Valid: false, Status: model.AdTicketStatusDuplicate}
			return nil
		}

		// 4. 发奖：解锁落库（与付费解锁同一张表，OrderID 留空）+ ticket 置 rewarded
		now := time.Now()
		unlock := model.EpisodeUnlock{
			UserID:    locked.UserID,
			DramaID:   locked.DramaID,
			EpisodeID: locked.EpisodeID,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&unlock).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.AdUnlockTicket{}).
			Where("id = ?", locked.ID).
			Updates(map[string]interface{}{
				"status":      model.AdTicketStatusRewarded,
				"trans_id":    transID,
				"rewarded_at": now,
			}).Error; err != nil {
			return err
		}

		// 5. 广告收益入账：ecpm → 单次收益（分），写入 channel_income_daily
		//    （渠道=狼之短剧，batch_no=ad_auto，与内购 app_auto 同口径参与分成与结算）
		//    任何收益侧失败不回滚解锁——解锁是主流程，收益只是记账。
		if cents := s.parseCSJEcpmCents(ecpm); cents > 0 {
			if err := s.recordAdIncome(tx, locked.DramaID, cents, now); err != nil {
				log.Printf("[ad-unlock] 广告收益入账失败（不影响解锁）ticket=%s trans_id=%s ecpm=%s err=%v",
					locked.TicketID, transID, ecpm, err)
			}
		} else if ecpm != "" {
			log.Printf("[ad-unlock] ecpm 解析为 0 或非法（跳过记账）ticket=%s trans_id=%s ecpm=%q unit=%s",
				locked.TicketID, transID, ecpm, s.cfg.CSJEcpmUnit)
		}
		result = CSJVerifyResult{Valid: true, TicketID: locked.TicketID, Status: model.AdTicketStatusRewarded}
		return nil
	})
	if err != nil {
		return CSJVerifyResult{Valid: false}, err
	}
	return result, nil
}

// parseCSJEcpmCents 把回调 ecpm 换算成单次有效展示收益（分）。
// 单次收益 = eCPM ÷ 1000；eCPM 单位由 CSJ_ECPM_UNIT 控制：
//   - "fen"（默认）：SDK getEcpm() 文档口径，收益（分）= ecpm / 1000
//   - "yuan"：收益（分）= ecpm × 100 / 1000
//
// 解析失败 / 非正值返回 0（调用方跳过记账，不影响解锁）。
func (s *Service) parseCSJEcpmCents(ecpm string) int64 {
	if ecpm == "" || ecpm == "null" || ecpm == "NULL" {
		return 0
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(ecpm), 64)
	if err != nil || v <= 0 {
		return 0
	}
	if strings.EqualFold(s.cfg.CSJEcpmUnit, "yuan") {
		return int64(math.Round(v * 100 / 1000))
	}
	// 默认按分：穿山甲 SDK getEcpm() 口径
	return int64(math.Round(v / 1000))
}

// recordAdIncome 把单次广告收益写入 channel_income_daily，并按内购同口径做创作者分成。
// 事务内执行：唯一键 (drama_id, channel, stat_date) 冲突时累加。
func (s *Service) recordAdIncome(tx *gorm.DB, dramaID uint64, grossCents int64, now time.Time) error {
	var drama model.Drama
	if err := tx.First(&drama, dramaID).Error; err != nil {
		return err
	}
	statDate := now.Format("2006-01-02")
	creatorAmount := int64(0)
	shareRatioBP := 0
	var creatorID uint64
	if drama.CreatorID != nil {
		creatorID = *drama.CreatorID
		// 2026-08-29 修复（中-3）：分成改整数 BP 运算，与 MarkOrderPaid 口径一致
		shareRatioBP = model.ShareRateToBP(s.cfg.CreatorShareRate)
		creatorAmount = model.IncomeFromGrossBP(grossCents, shareRatioBP)

		if creatorAmount > 0 {
			// 行锁 + 写余额（与 MarkOrderPaid 相同模式）
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				First(&model.Creator{}, *drama.CreatorID).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.Creator{}).
				Where("id = ?", *drama.CreatorID).
				Updates(map[string]interface{}{
					"total_income_cents": gorm.Expr("total_income_cents + ?", creatorAmount),
					"balance_cents":      gorm.Expr("balance_cents + ?", creatorAmount),
				}).Error; err != nil {
				return err
			}
			// 当日聚合
			stat := model.CreatorStatsDaily{
				CreatorID:   *drama.CreatorID,
				DramaID:     dramaID,
				StatDate:    statDate,
				IncomeCents: creatorAmount,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "creator_id"}, {Name: "drama_id"}, {Name: "stat_date"}},
				DoUpdates: clause.Assignments(map[string]interface{}{
					"income_cents": gorm.Expr("creator_stats_daily.income_cents + ?", creatorAmount),
				}),
			}).Create(&stat).Error; err != nil {
				return err
			}
		}
	}

	// channel_income_daily：渠道=狼之短剧，batch_no=ad_auto
	cid := model.ChannelIncomeDaily{
		DramaID:      dramaID,
		Channel:      ChannelAppName,
		StatDate:     statDate,
		CreatorID:    creatorID,
		GrossCents:   grossCents,
		ShareRatioBP: shareRatioBP,
		IncomeCents:  creatorAmount,
		BatchNo:      "ad_auto",
	}
	if err := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "drama_id"}, {Name: "channel"}, {Name: "stat_date"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"gross_cents":  gorm.Expr("channel_income_daily.gross_cents + EXCLUDED.gross_cents"),
			"income_cents": gorm.Expr("channel_income_daily.income_cents + EXCLUDED.income_cents"),
		}),
	}).Create(&cid).Error; err != nil {
		return err
	}
	return nil
}

// csjSign 穿山甲服务端奖励验证签名：sha256(SecurityKey + ":" + trans_id) → hex 小写。
func csjSign(securityKey, transID string) string {
	h := sha256.Sum256([]byte(securityKey + ":" + transID))
	return hex.EncodeToString(h[:])
}

// csjSecureCompare 恒时比较签名，防时序侧信道逐字节猜解。
func csjSecureCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// generateAdTicketID 生成穿山甲广告解锁凭证号：AD + 时间戳 + 随机数。
// 注意：穿山甲 SDK 的 user_id 透传字段要求非空字符串，故凭证必须无 "-"（避免
// 部分网关/SDK 对特殊字符做 URL 编码后回调回来对不上）。
func generateAdTicketID() string {
	now := time.Now()
	return fmt.Sprintf("AD%s%04d%05d", now.Format("20060102150405"), now.Nanosecond()/1000%10000, rand.Intn(100000))
}
