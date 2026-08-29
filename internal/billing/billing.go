package billing

import (
	"errors"
	"fmt"
	"log"
	"math/rand"
	"strconv"
	"time"

	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/payment"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// orderRef 关单后需要联动作废渠道侧支付链接的订单标识。
type orderRef struct {
	OrderNo string
	Method  string
}

// closeChannelOrders 在事务外逐个调渠道关单，作废其支付链接（支付宝 trade.close / 微信 closeorder）。
// 失败只记日志、不阻断本地流程：MarkOrderPaid 已拒绝已关单/过期订单，叠加对账兜底。
func (s *Service) closeChannelOrders(refs []orderRef) {
	for _, r := range refs {
		if r.OrderNo == "" || r.Method == "" {
			continue
		}
		provider, err := s.payments.Get(r.Method)
		if err != nil {
			continue
		}
		if err := provider.CloseOrder(r.OrderNo); err != nil {
			log.Printf("[order] 渠道关单失败 order_no=%s method=%s err=%v", r.OrderNo, r.Method, err)
		}
	}
}

// ChannelAppName APP 内购在 channel_income_daily 中的渠道标识。
const ChannelAppName = "狼之短剧"

var (
	ErrEpisodeNotFound       = errors.New("剧集不存在")
	ErrEpisodeNotReady       = errors.New("剧集尚未就绪，不能下单")
	ErrDramaNotFound         = errors.New("短剧不存在")
	ErrDramaNotAvailable     = errors.New("短剧未上架或已下架，不能下单")
	ErrEpisodeFree           = errors.New("该剧集为免费集，无需支付")
	ErrAlreadyUnlocked       = errors.New("剧集已解锁")
	ErrAmountInvalid         = errors.New("订单金额非法")
	ErrOrderAmountMismatch   = errors.New("支付回调金额与订单金额不一致")
	ErrPaymentMethodMismatch = errors.New("支付回调渠道与订单不一致")
	ErrOrderNotFound         = errors.New("订单不存在")
	ErrOrderNotPaid          = errors.New("订单未支付或不可用于解锁")
	ErrOrderExpired          = errors.New("订单已过期，不能再标记已支付")
	ErrOrderEpisodeMatch     = errors.New("订单与剧集不匹配")
	ErrOrderNotOwned         = errors.New("订单不属于当前用户")
	// 退款相关错误。
	ErrRefundAmountInvalid = errors.New("退款金额非法(必须 > 0 且 <= 剩余可退金额)")
	ErrRefundNotAllowed    = errors.New("订单状态不允许退款(仅 paid / partial_refunded 可退)")
	ErrRefundNoRequired    = errors.New("退款单号必填")
)

// Service 支付/订单/分账核心：保证所有写余额、写解锁的操作都在事务内 + creators 行锁。
type Service struct {
	db       *gorm.DB
	cfg      config.Config
	payments *payment.Registry
}

func New(db *gorm.DB, cfg config.Config, payments *payment.Registry) *Service {
	return &Service{db: db, cfg: cfg, payments: payments}
}

// effectiveFreeEpisodes 返回当前生效的免费集数，与 handler 层同名口径完全一致
// （2026-06 会议定）：免费集数统一读全局配置 pricing.free_episodes、改一次即时对所有剧生效；
// dramas.free_episodes 列保留但暂不参与计费判定。将来做单剧定制时在此叠加 drama 覆盖。
// 传入 db 以便在事务内（quoteBatch）用 tx 读，事务外用 s.db。
func (s *Service) effectiveFreeEpisodes(db *gorm.DB, _ model.Drama) int {
	var gc model.GlobalConfig
	if err := db.First(&gc, "key = ?", model.ConfigKeyFreeEpisodes).Error; err != nil {
		return 0
	}
	n, err := strconv.Atoi(gc.Value)
	if err != nil {
		return 0
	}
	return n
}

// effectivePriceCents 返回当前生效的每集单价（分）。
// 优先用 drama 自身 price_cents；若为 0 则回退到全局默认 pricing.price_cents。
// 与 effectiveFreeEpisodes 口径一致：全局配置改一次即时对所有剧生效。
func (s *Service) effectivePriceCents(db *gorm.DB, drama model.Drama) int64 {
	if drama.PriceCents > 0 {
		return drama.PriceCents
	}
	var gc model.GlobalConfig
	if err := db.First(&gc, "key = ?", model.ConfigKeyPriceCents).Error; err != nil {
		return 0
	}
	v, err := strconv.ParseInt(gc.Value, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// CreateOrder 单集下单（仅当次支付，不保留 / 不复用待支付订单）：
//  1. 用户对同一 episode 已支付 → 返回已解锁。
//  2. 关闭该用户该集所有旧的待支付订单（不复用），再创建一个全新的当次订单。
//
// 同一次点击的重复提交由端上 Idempotency-Key + idempotencyMiddleware（Redis）兜强幂等；
// 这里只保证"每次下单都是一次干净的当次支付"，不会恢复历史待支付。
type CreateOrderOutcome struct {
	AlreadyUnlocked bool
	Order           *model.Order
	PayParams       payment.PrepayParams
}

func (s *Service) CreateOrder(userID uint64, dramaID, episodeID uint64, productID *uint64, method, payScene, clientIP string) (*CreateOrderOutcome, error) {
	if method != model.PaymentMethodWechat && method != model.PaymentMethodAlipay {
		return nil, payment.ErrUnsupportedMethod
	}

	var ep model.Episode
	if err := s.db.First(&ep, episodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrEpisodeNotFound
		}
		return nil, err
	}
	if ep.DramaID != dramaID {
		return nil, ErrOrderEpisodeMatch
	}
	if ep.Status != model.EpisodeStatusReady {
		return nil, ErrEpisodeNotReady
	}
	var drama model.Drama
	if err := s.db.First(&drama, dramaID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDramaNotFound
		}
		return nil, err
	}
	if drama.Status != model.DramaStatusPublished {
		return nil, ErrDramaNotAvailable
	}
	if ep.EpisodeNo <= s.effectiveFreeEpisodes(s.db, drama) {
		return nil, ErrEpisodeFree
	}

	// 1. 已解锁直接返回
	var unlock model.EpisodeUnlock
	if err := s.db.Where("user_id = ? AND episode_id = ?", userID, episodeID).First(&unlock).Error; err == nil {
		return &CreateOrderOutcome{AlreadyUnlocked: true}, nil
	}

	// 仅当次支付：关闭旧待支付 + 创建全新当次订单（事务 + advisory lock 防并发重复下单）
	now := time.Now()
	var order model.Order
	var closedRefs []orderRef
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?, ?)", int64(userID), int64(episodeID)).Error; err != nil {
			return err
		}

		var unlock model.EpisodeUnlock
		if err := tx.Where("user_id = ? AND episode_id = ?", userID, episodeID).First(&unlock).Error; err == nil {
			return ErrAlreadyUnlocked
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		// 不复用历史待支付：关闭该用户该集所有旧的 pending 订单，保证最多一个 live pending（当次）。
		// 也腾出部分唯一索引 idx_orders_user_episode_pending，使下面新建当次单不冲突。
		// 先查出来（带渠道）供事务提交后联动渠道关单，作废其支付链接。
		var olds []model.Order
		if err := tx.Select("order_no", "payment_method").
			Where("user_id = ? AND episode_id = ? AND status = ?", userID, episodeID, model.OrderStatusPending).
			Find(&olds).Error; err != nil {
			return err
		}
		if len(olds) > 0 {
			if err := tx.Model(&model.Order{}).
				Where("user_id = ? AND episode_id = ? AND status = ?", userID, episodeID, model.OrderStatusPending).
				Update("status", model.OrderStatusClosed).Error; err != nil {
				return err
			}
			for _, o := range olds {
				closedRefs = append(closedRefs, orderRef{OrderNo: o.OrderNo, Method: o.PaymentMethod})
			}
		}

		amount := s.effectivePriceCents(tx, drama)
		if productID != nil && *productID > 0 {
			var prod model.Product
			if err := tx.First(&prod, *productID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrAmountInvalid
				}
				return err
			}
			if prod.Status != model.StatusActive || prod.Type != model.ProductTypeEpisodeUnlock {
				return ErrAmountInvalid
			}
			if prod.PriceCents > 0 {
				amount = prod.PriceCents
			}
		}
		if amount <= 0 {
			return ErrAmountInvalid
		}

		expiredAt := now.Add(s.cfg.OrderPendingTTL)
		order = model.Order{
			OrderNo:       generateOrderNo(),
			UserID:        userID,
			ProductID:     productID,
			DramaID:       dramaID,
			EpisodeID:     episodeID,
			AmountCents:   amount,
			PaymentMethod: method,
			Status:        model.OrderStatusPending,
			ExpiredAt:     &expiredAt,
		}
		return tx.Create(&order).Error
	})
	if err != nil {
		if errors.Is(err, ErrAlreadyUnlocked) {
			return &CreateOrderOutcome{AlreadyUnlocked: true}, nil
		}
		return nil, err
	}
	// 事务已提交：联动作废旧待支付订单的渠道支付链接。
	s.closeChannelOrders(closedRefs)
	return s.attachPayParams(&order, method, payScene, clientIP)
}

func (s *Service) attachPayParams(order *model.Order, method, payScene, clientIP string) (*CreateOrderOutcome, error) {
	provider, err := s.payments.Get(method)
	if err != nil {
		return nil, err
	}
	params, err := provider.Prepay(payment.PrepayInput{
		OrderNo:     order.OrderNo,
		AmountCents: order.AmountCents,
		Subject:     fmt.Sprintf("剧集解锁 #%d", order.EpisodeID),
		UserID:      order.UserID,
		Scene:       payScene,
		ClientIP:    clientIP,
		// 第三方支付有效期：早于本地关单时间（PaymentExpire < OrderPendingTTL），防"已关单仍可支付"资损。
		ExpireAt: time.Now().Add(s.cfg.PaymentExpire),
	})
	if err != nil {
		return nil, err
	}
	if order.PaymentMethod != method {
		order.PaymentMethod = method
		s.db.Model(order).Update("payment_method", method)
	}
	return &CreateOrderOutcome{Order: order, PayParams: params}, nil
}

// SingleQuote 单集购买试算：将要支付的金额 + 是否已解锁 + 是否免费。
// 供 app 在拉起支付前展示「实付金额」，免费/已解锁不视为错误，用标志位告知。
type SingleQuote struct {
	DramaID         uint64
	EpisodeID       uint64
	AmountCents     int64
	AlreadyUnlocked bool
	IsFree          bool
}

// QuoteSingle 单集购买试算（只读，不下单）。计价规则与 CreateOrder 完全对齐：
// 默认用 dramas.price_cents；传 product_id 且商品有效价格 > 0 时用商品价覆盖。
// 硬错误（剧集/短剧不存在、不匹配、未就绪、未上架、无单价）；免费集 / 已解锁返回标志位，金额 0。
func (s *Service) QuoteSingle(userID, dramaID, episodeID uint64, productID *uint64) (*SingleQuote, error) {
	var ep model.Episode
	if err := s.db.First(&ep, episodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrEpisodeNotFound
		}
		return nil, err
	}
	if ep.DramaID != dramaID {
		return nil, ErrOrderEpisodeMatch
	}
	if ep.Status != model.EpisodeStatusReady {
		return nil, ErrEpisodeNotReady
	}
	var drama model.Drama
	if err := s.db.First(&drama, dramaID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDramaNotFound
		}
		return nil, err
	}
	if drama.Status != model.DramaStatusPublished {
		return nil, ErrDramaNotAvailable
	}

	q := &SingleQuote{DramaID: dramaID, EpisodeID: episodeID}
	if ep.EpisodeNo <= s.effectiveFreeEpisodes(s.db, drama) {
		q.IsFree = true
		return q, nil
	}

	var unlock model.EpisodeUnlock
	if err := s.db.Where("user_id = ? AND episode_id = ?", userID, episodeID).First(&unlock).Error; err == nil {
		q.AlreadyUnlocked = true
		return q, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	amount := s.effectivePriceCents(s.db, drama)
	if productID != nil && *productID > 0 {
		var prod model.Product
		if err := s.db.First(&prod, *productID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrAmountInvalid
			}
			return nil, err
		}
		if prod.Status != model.StatusActive || prod.Type != model.ProductTypeEpisodeUnlock {
			return nil, ErrAmountInvalid
		}
		if prod.PriceCents > 0 {
			amount = prod.PriceCents
		}
	}
	if amount <= 0 {
		return nil, ErrAmountInvalid
	}
	q.AmountCents = amount
	return q, nil
}

// BatchQuote 选集购买试算：可购买集（剔除免费/已解锁）+ 已解锁集 + 金额。
type BatchQuote struct {
	DramaID           uint64
	BuyableEpisodeIDs []uint64
	AlreadyUnlocked   []uint64
	UnitPriceCents    int64
	AmountCents       int64
}

// quoteBatch 校验并试算选集购买。硬错误（整批拒绝）：集不存在 / 不属于该剧 / 未就绪 / 短剧未发布或无单价。
// 软跳过（不计入可购买）：免费集、已解锁集。可购买 = 付费集 ∩ 未解锁。
func (s *Service) quoteBatch(tx *gorm.DB, userID, dramaID uint64, episodeIDs []uint64) (*BatchQuote, error) {
	seen := map[uint64]bool{}
	ids := make([]uint64, 0, len(episodeIDs))
	for _, id := range episodeIDs {
		if id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, ErrEpisodeNotFound
	}

	var drama model.Drama
	if err := tx.First(&drama, dramaID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDramaNotFound
		}
		return nil, err
	}
	if drama.Status != model.DramaStatusPublished {
		return nil, ErrDramaNotAvailable
	}
	if s.effectivePriceCents(tx, drama) <= 0 {
		return nil, ErrAmountInvalid
	}

	var eps []model.Episode
	if err := tx.Where("id IN ?", ids).Find(&eps).Error; err != nil {
		return nil, err
	}
	if len(eps) != len(ids) {
		return nil, ErrEpisodeNotFound // 有不存在的集
	}
	for _, ep := range eps {
		if ep.DramaID != dramaID {
			return nil, ErrOrderEpisodeMatch
		}
		if ep.Status != model.EpisodeStatusReady {
			return nil, ErrEpisodeNotReady
		}
	}

	// 已解锁集（剔除）
	var unlockedIDs []uint64
	if err := tx.Model(&model.EpisodeUnlock{}).
		Where("user_id = ? AND episode_id IN ?", userID, ids).
		Pluck("episode_id", &unlockedIDs).Error; err != nil {
		return nil, err
	}
	unlockedSet := map[uint64]bool{}
	for _, id := range unlockedIDs {
		unlockedSet[id] = true
	}
	freeEp := s.effectiveFreeEpisodes(tx, drama)
	freeSet := map[uint64]bool{}
	for _, ep := range eps {
		if ep.EpisodeNo <= freeEp {
			freeSet[ep.ID] = true
		}
	}
	buyable := make([]uint64, 0, len(ids))
	for _, id := range ids {
		if !unlockedSet[id] && !freeSet[id] { // 付费且未解锁才买
			buyable = append(buyable, id)
		}
	}
	return &BatchQuote{
		DramaID:           dramaID,
		BuyableEpisodeIDs: buyable,
		AlreadyUnlocked:   unlockedIDs,
		UnitPriceCents:    s.effectivePriceCents(tx, drama),
		AmountCents:       int64(len(buyable)) * s.effectivePriceCents(tx, drama),
	}, nil
}

// QuoteBatch 选集购买试算（只读，不下单），供前端展示价格 + 哪些已拥有。
func (s *Service) QuoteBatch(userID, dramaID uint64, episodeIDs []uint64) (*BatchQuote, error) {
	return s.quoteBatch(s.db, userID, dramaID, episodeIDs)
}

// CreateBatchOrder 选集购买：一笔订单覆盖多集（episode_ids 清单），金额 = 可购买集数 × 单价。
// 自动剔除免费 / 已解锁集；可购买为空（全已解锁/全免费）→ AlreadyUnlocked。
func (s *Service) CreateBatchOrder(userID, dramaID uint64, episodeIDs []uint64, method, payScene, clientIP string) (*CreateOrderOutcome, error) {
	if method != model.PaymentMethodWechat && method != model.PaymentMethodAlipay {
		return nil, payment.ErrUnsupportedMethod
	}
	now := time.Now()
	var order model.Order
	var closedRefs []orderRef
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 同一 user+drama 串行，避免并发重复下批量单（叠加端上 Idempotency-Key）。
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?, ?)", int64(userID), int64(dramaID)).Error; err != nil {
			return err
		}
		quote, err := s.quoteBatch(tx, userID, dramaID, episodeIDs)
		if err != nil {
			return err
		}
		if len(quote.BuyableEpisodeIDs) == 0 {
			return ErrAlreadyUnlocked
		}
		// 仅当次支付：关闭该用户该剧旧的待支付批量单（episode_id=0），不复用，再建当次单。
		// 先查出来（带渠道）供事务提交后联动渠道关单。
		var olds []model.Order
		if err := tx.Select("order_no", "payment_method").
			Where("user_id = ? AND drama_id = ? AND episode_id = 0 AND status = ?", userID, dramaID, model.OrderStatusPending).
			Find(&olds).Error; err != nil {
			return err
		}
		if len(olds) > 0 {
			if err := tx.Model(&model.Order{}).
				Where("user_id = ? AND drama_id = ? AND episode_id = 0 AND status = ?", userID, dramaID, model.OrderStatusPending).
				Update("status", model.OrderStatusClosed).Error; err != nil {
				return err
			}
			for _, o := range olds {
				closedRefs = append(closedRefs, orderRef{OrderNo: o.OrderNo, Method: o.PaymentMethod})
			}
		}
		expiredAt := now.Add(s.cfg.OrderPendingTTL)
		order = model.Order{
			OrderNo:       generateOrderNo(),
			UserID:        userID,
			DramaID:       dramaID,
			EpisodeID:     0, // 批量单不绑单集；清单在 EpisodeIDs
			EpisodeIDs:    quote.BuyableEpisodeIDs,
			AmountCents:   quote.AmountCents,
			PaymentMethod: method,
			Status:        model.OrderStatusPending,
			ExpiredAt:     &expiredAt,
		}
		return tx.Create(&order).Error
	})
	if err != nil {
		if errors.Is(err, ErrAlreadyUnlocked) {
			return &CreateOrderOutcome{AlreadyUnlocked: true}, nil
		}
		return nil, err
	}
	// 事务已提交：联动作废旧待支付批量单的渠道支付链接。
	s.closeChannelOrders(closedRefs)
	return s.attachPayParams(&order, method, payScene, clientIP)
}

// MarkOrderPaid 在事务里完成订单 → 解锁 → 分账。
// 重复回调（订单已 paid）幂等返回 nil。
func (s *Service) MarkOrderPaid(orderNo, platformTradeNo, paymentMethod string, amountCents int64, paidAt time.Time) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var order model.Order
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("order_no = ?", orderNo).
			First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrOrderNotFound
			}
			return err
		}
		if order.Status == model.OrderStatusPaid {
			return nil // 幂等
		}
		if order.Status == model.OrderStatusClosed || order.Status == model.OrderStatusRefunded {
			return ErrOrderNotPaid
		}
		// 防御性兜底：pending 但已过期的订单拒绝标记已支付，避免与 close-expired-orders 的竞态窗口。
		if order.ExpiredAt != nil && order.ExpiredAt.Before(paidAt) {
			return ErrOrderExpired
		}
		if paymentMethod != "" && order.PaymentMethod != paymentMethod {
			return ErrPaymentMethodMismatch
		}
		if amountCents != order.AmountCents {
			return ErrOrderAmountMismatch
		}

		// 1. 更新订单
		updates := map[string]interface{}{
			"status":            model.OrderStatusPaid,
			"paid_at":           paidAt,
			"platform_trade_no": platformTradeNo,
		}
		if err := tx.Model(&order).Updates(updates).Error; err != nil {
			return err
		}

		// 2. 写解锁（批量单解锁清单里所有集，单集单解锁 episode_id）。重复 ON CONFLICT DO NOTHING。
		unlockIDs := order.EpisodeIDs
		if len(unlockIDs) == 0 {
			unlockIDs = []uint64{order.EpisodeID}
		}
		for _, epID := range unlockIDs {
			unlock := model.EpisodeUnlock{
				UserID:    order.UserID,
				DramaID:   order.DramaID,
				EpisodeID: epID,
				OrderID:   &order.ID,
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&unlock).Error; err != nil {
				return err
			}
		}

		// 3. 分账（若短剧绑定了创作者）+ 写入渠道收益
		var drama model.Drama
		if err := tx.First(&drama, order.DramaID).Error; err != nil {
			return err
		}

		statDate := paidAt.Format("2006-01-02")
		creatorAmount := int64(0)
		shareRatioBP := 0
		var creatorID uint64
		if drama.CreatorID != nil {
			creatorID = *drama.CreatorID
			// 2026-08-29 修复（中-3）：分成改整数 BP 运算，float 截断（如 0.29→2899）不再发生
			shareRatioBP = model.ShareRateToBP(s.cfg.CreatorShareRate)
			creatorAmount = model.IncomeFromGrossBP(order.AmountCents, shareRatioBP)

			if creatorAmount > 0 {
				// 行锁 + 写余额
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

				// 4. 当日聚合
				stat := model.CreatorStatsDaily{
					CreatorID:   *drama.CreatorID,
					DramaID:     order.DramaID,
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

		// 5. 写入 channel_income_daily（APP 内购自动记录，渠道=狼之短剧）
		// 唯一键 (drama_id, channel, stat_date) 冲突时累加，与 Excel 导入口径一致
		cid := model.ChannelIncomeDaily{
			DramaID:      order.DramaID,
			Channel:      ChannelAppName,
			StatDate:     statDate,
			CreatorID:    creatorID,
			GrossCents:   order.AmountCents,
			ShareRatioBP: shareRatioBP,
			IncomeCents:  creatorAmount,
			BatchNo:      "app_auto",
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
	})
}

type CloseExpiredOrdersResult struct {
	ClosedCount     int64
	OldestExpiredAt *time.Time
	SampleOrderNos  []string
}

// CloseExpiredOrders 把过期 pending 转 closed（定时任务 / 运维命令用）。
func (s *Service) CloseExpiredOrders(now time.Time) (CloseExpiredOrdersResult, error) {
	var result CloseExpiredOrdersResult
	var closedRefs []orderRef
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var orders []model.Order
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "order_no", "payment_method", "expired_at").
			Where("status = ? AND expired_at IS NOT NULL AND expired_at < ?", model.OrderStatusPending, now).
			Order("expired_at asc").
			Find(&orders).Error; err != nil {
			return err
		}
		if len(orders) == 0 {
			return nil
		}

		ids := make([]uint64, 0, len(orders))
		closedRefs = closedRefs[:0]
		for i, order := range orders {
			ids = append(ids, order.ID)
			closedRefs = append(closedRefs, orderRef{OrderNo: order.OrderNo, Method: order.PaymentMethod})
			if i == 0 {
				result.OldestExpiredAt = order.ExpiredAt
			}
			if len(result.SampleOrderNos) < 5 {
				result.SampleOrderNos = append(result.SampleOrderNos, order.OrderNo)
			}
		}

		res := tx.Model(&model.Order{}).
			Where("id IN ?", ids).
			Update("status", model.OrderStatusClosed)
		if res.Error != nil {
			return res.Error
		}
		result.ClosedCount = res.RowsAffected
		return nil
	})
	if err != nil {
		return result, err
	}
	// 事务已提交：联动作废过期订单的渠道支付链接（超时关单同样要让渠道侧付不了）。
	s.closeChannelOrders(closedRefs)
	return result, err
}

// CloseExpiredRechargesResult 充值单过期清理结果。
type CloseExpiredRechargesResult struct {
	ClosedCount       int64
	OldestExpiredAt   *time.Time
	SampleRechargeNos []string
}

// CloseExpiredRecharges 把过期 pending 充值单转 closed（定时任务用）。
// 与 CloseExpiredOrders 对称：过期充值单如果不清理，
// 1) 渠道侧支付链接一直有效，发行商可能通过旧链接付款（虽然 webhook 会拒绝，但体验差且渠道侧资源泄漏）；
// 2) 脏数据累积，充值单列表里永远有 pending 的僵尸记录；
// 3) SyncRechargeStatus 对账时会误判（渠道侧已关单但本地还 pending）。
func (s *Service) CloseExpiredRecharges(now time.Time) (CloseExpiredRechargesResult, error) {
	var result CloseExpiredRechargesResult
	var closedRefs []orderRef
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var recharges []model.DistributorRecharge
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "recharge_no", "payment_method", "expired_at").
			Where("status = ? AND expired_at IS NOT NULL AND expired_at < ?", "pending", now).
			Order("expired_at asc").
			Find(&recharges).Error; err != nil {
			return err
		}
		if len(recharges) == 0 {
			return nil
		}

		ids := make([]uint64, 0, len(recharges))
		for i, rc := range recharges {
			ids = append(ids, rc.ID)
			closedRefs = append(closedRefs, orderRef{OrderNo: rc.RechargeNo, Method: rc.PaymentMethod})
			if i == 0 {
				result.OldestExpiredAt = rc.ExpiredAt
			}
			if len(result.SampleRechargeNos) < 5 {
				result.SampleRechargeNos = append(result.SampleRechargeNos, rc.RechargeNo)
			}
		}

		res := tx.Model(&model.DistributorRecharge{}).
			Where("id IN ?", ids).
			Update("status", "closed")
		if res.Error != nil {
			return res.Error
		}
		result.ClosedCount = res.RowsAffected
		return nil
	})
	if err != nil {
		return result, err
	}
	// 事务已提交：联动作废过期充值单的渠道支付链接（与订单侧一致）。
	s.closeChannelOrders(closedRefs)
	return result, err
}

// EnsureOrderUnlocked 用于 POST /v1/app/episodes/:id/unlock：
//   - 订单必须存在、属于该用户、status=paid、episode_id 匹配
//   - 解锁记录可能由 webhook 已经写入，重复调用幂等
func (s *Service) EnsureOrderUnlocked(userID, episodeID uint64, orderNo string) error {
	var order model.Order
	if err := s.db.Where("order_no = ?", orderNo).First(&order).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrOrderNotFound
		}
		return err
	}
	if order.UserID != userID {
		return ErrOrderNotOwned
	}
	if order.EpisodeID != episodeID {
		return ErrOrderEpisodeMatch
	}
	if order.Status != model.OrderStatusPaid {
		return ErrOrderNotPaid
	}
	unlock := model.EpisodeUnlock{
		UserID:    order.UserID,
		DramaID:   order.DramaID,
		EpisodeID: order.EpisodeID,
		OrderID:   &order.ID,
	}
	return s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&unlock).Error
}

// RefundOrder 发起退款。
//
//  1. 事务内行锁订单（与 MarkOrderPaid 对称），防止并发双重退款导致渠道侧资损。
//  2. 校验:订单存在、状态是 paid 或 partial_refunded、本次退款 + 已退累计 <= 订单金额。
//  3. 幂等:同 refundNo 重入直接返回当前订单(不再调渠道,避免重复退款)。
//  4. 调 Provider.Refund(支付宝同步、微信异步,Provider 内部已抹平)。
//  5. 事务内:更新订单(累计退款 + 状态 + 退款元数据),按比例回退创作者余额 + 当日聚合;
//     全额退款则把 status 置 refunded、部分退款置 partial_refunded。
//  6. 解锁记录保持不动:产品不在意"退款后还能不能看",运营可单独删除。
//
// 注意:多次部分退款时,refund_no/platform_refund_no/refund_reason/refunded_at 只记录最近一次;
// 累计金额走 refund_amount_cents。完整退款流水若后续需要审计,再加 refund_history 表。
func (s *Service) RefundOrder(orderNo, refundNo string, amountCents int64, reason string) (*model.Order, error) {
	if refundNo == "" {
		return nil, ErrRefundNoRequired
	}
	if amountCents <= 0 {
		return nil, ErrRefundAmountInvalid
	}

	var order model.Order
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 行锁订单，防止并发双重退款（与 MarkOrderPaid 对称）。
		// 渠道退款调用在行锁保护内，确保并发退款请求串行化，
		// 第二个请求拿到锁后读到已更新的 RefundNo/状态，被幂等或金额校验拒绝。
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("order_no = ?", orderNo).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrOrderNotFound
			}
			return err
		}
		// 状态校验
		if order.Status != model.OrderStatusPaid && order.Status != model.OrderStatusPartialRefunded {
			return ErrRefundNotAllowed
		}
		// 幂等:相同 refundNo 直接返回,不再调渠道。
		if order.RefundNo == refundNo {
			return nil
		}
		// 金额校验（基于锁定后的最新数据）
		if order.RefundAmountCents+amountCents > order.AmountCents {
			return ErrRefundAmountInvalid
		}

		// 调渠道退款（在行锁保护内，确保并发退款串行化）。
		provider, err := s.payments.Get(order.PaymentMethod)
		if err != nil {
			return err
		}
		result, err := provider.Refund(payment.RefundInput{
			OrderNo:         order.OrderNo,
			PlatformTradeNo: order.PlatformTradeNo,
			RefundNo:        refundNo,
			AmountCents:     amountCents,
			Reason:          reason,
		})
		if err != nil {
			return err
		}
		if !result.Success {
			return payment.ErrRefundFailed
		}

		refundedAt := result.RefundedAt
		if refundedAt.IsZero() {
			refundedAt = time.Now()
		}

		newRefundTotal := order.RefundAmountCents + amountCents
		newStatus := model.OrderStatusPartialRefunded
		if newRefundTotal >= order.AmountCents {
			newStatus = model.OrderStatusRefunded
		}
		updates := map[string]interface{}{
			"status":              newStatus,
			"refund_amount_cents": newRefundTotal,
			"refunded_at":         refundedAt,
			"refund_reason":       reason,
			"refund_no":           refundNo,
			"platform_refund_no":  result.PlatformRefundNo,
		}
		if err := tx.Model(&order).Updates(updates).Error; err != nil {
			return err
		}

		// 回退分账:按本次退款额占订单金额的比例从 creator 余额扣回。
		// 兜底:扣回后余额不允许为负(由 DB CHECK 兜底也行,这里先在应用层做防御)。
		var drama model.Drama
		if err := tx.First(&drama, order.DramaID).Error; err != nil {
			return err
		}
		if drama.CreatorID == nil {
			// 回退 channel_income_daily（APP 内购渠道收益）
			refundStatDate := refundedAt.Format("2006-01-02")
			tx.Model(&model.ChannelIncomeDaily{}).
				Where("drama_id = ? AND channel = ? AND stat_date = ?",
					order.DramaID, ChannelAppName, refundStatDate).
				Updates(map[string]interface{}{
					"gross_cents": gorm.Expr("GREATEST(gross_cents - ?, 0)", amountCents),
				})
			order.Status = newStatus
			order.RefundAmountCents = newRefundTotal
			order.RefundedAt = &refundedAt
			order.RefundReason = reason
			order.RefundNo = refundNo
			order.PlatformRefundNo = result.PlatformRefundNo
		return nil
	}
	// 2026-08-29 修复（中-3）：退款追回也走整数 BP，与入账口径一致
	clawback := model.IncomeFromGrossBP(amountCents, model.ShareRateToBP(s.cfg.CreatorShareRate))
		if clawback > 0 {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				First(&model.Creator{}, *drama.CreatorID).Error; err != nil {
				return err
			}
			// total_income_cents 是累计概念,这里同步扣回保持口径与"实际入袋"一致。
			if err := tx.Model(&model.Creator{}).
				Where("id = ?", *drama.CreatorID).
				Updates(map[string]interface{}{
					"total_income_cents": gorm.Expr("GREATEST(total_income_cents - ?, 0)", clawback),
					"balance_cents":      gorm.Expr("GREATEST(balance_cents - ?, 0)", clawback),
				}).Error; err != nil {
				return err
			}

			// 当日聚合按"退款发生当天"回退,与下单/支付当天聚合方向一致;
			// 若回退导致负数则置 0,避免账面诡异。
			statDate := refundedAt.Format("2006-01-02")
			if err := tx.Model(&model.CreatorStatsDaily{}).
				Where("creator_id = ? AND drama_id = ? AND stat_date = ?",
					*drama.CreatorID, order.DramaID, statDate).
				Update("income_cents", gorm.Expr("GREATEST(income_cents - ?, 0)", clawback)).Error; err != nil {
				return err
			}
		}

		// 回退 channel_income_daily（APP 内购渠道收益）
		refundStatDate := refundedAt.Format("2006-01-02")
		if err := tx.Model(&model.ChannelIncomeDaily{}).
			Where("drama_id = ? AND channel = ? AND stat_date = ?",
				order.DramaID, ChannelAppName, refundStatDate).
			Updates(map[string]interface{}{
				"gross_cents":  gorm.Expr("GREATEST(gross_cents - ?, 0)", amountCents),
				"income_cents": gorm.Expr("GREATEST(income_cents - ?, 0)", clawback),
			}).Error; err != nil {
			return err
		}

		order.Status = newStatus
		order.RefundAmountCents = newRefundTotal
		order.RefundedAt = &refundedAt
		order.RefundReason = reason
		order.RefundNo = refundNo
		order.PlatformRefundNo = result.PlatformRefundNo
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &order, nil
}

// DevRefundOrder 沙箱退款 mock 端点专用：与 RefundOrder 逻辑完全对称（行锁 + 状态/金额/幂等校验 + 分账回退），
// 唯一区别是跳过渠道侧退款调用，直接当作退款成功。用于 PAYMENT_DEV_MODE=true 时退款链路可测性：
// 沙箱配了真实支付宝凭证时，prepay 走真实渠道但订单可能通过 /v1/dev/orders/:no/pay mock 支付，
// 退款走真实 AlipayProvider 会被拒（DEV-MOCK 流水号不存在），此方法绕过渠道侧完成退款状态机测试。
func (s *Service) DevRefundOrder(orderNo, refundNo string, amountCents int64, reason string) (*model.Order, error) {
	if refundNo == "" {
		return nil, ErrRefundNoRequired
	}
	if amountCents <= 0 {
		return nil, ErrRefundAmountInvalid
	}

	var order model.Order
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("order_no = ?", orderNo).First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrOrderNotFound
			}
			return err
		}
		if order.Status != model.OrderStatusPaid && order.Status != model.OrderStatusPartialRefunded {
			return ErrRefundNotAllowed
		}
		if order.RefundNo == refundNo {
			return nil
		}
		if order.RefundAmountCents+amountCents > order.AmountCents {
			return ErrRefundAmountInvalid
		}

		// 跳过渠道退款调用，直接 mock 退款成功（与 DevProvider.Refund 对称）。
		refundedAt := time.Now()
		platformRefundNo := "DEV-REFUND-" + refundNo

		newRefundTotal := order.RefundAmountCents + amountCents
		newStatus := model.OrderStatusPartialRefunded
		if newRefundTotal >= order.AmountCents {
			newStatus = model.OrderStatusRefunded
		}
		updates := map[string]interface{}{
			"status":              newStatus,
			"refund_amount_cents": newRefundTotal,
			"refunded_at":         refundedAt,
			"refund_reason":       reason,
			"refund_no":           refundNo,
			"platform_refund_no":  platformRefundNo,
		}
		if err := tx.Model(&order).Updates(updates).Error; err != nil {
			return err
		}

		var drama model.Drama
		if err := tx.First(&drama, order.DramaID).Error; err != nil {
			return err
		}
		if drama.CreatorID == nil {
			// 回退 channel_income_daily（APP 内购渠道收益）
			refundStatDate := refundedAt.Format("2006-01-02")
			tx.Model(&model.ChannelIncomeDaily{}).
				Where("drama_id = ? AND channel = ? AND stat_date = ?",
					order.DramaID, ChannelAppName, refundStatDate).
				Updates(map[string]interface{}{
					"gross_cents": gorm.Expr("GREATEST(gross_cents - ?, 0)", amountCents),
				})
			order.Status = newStatus
			order.RefundAmountCents = newRefundTotal
			order.RefundedAt = &refundedAt
			order.RefundReason = reason
			order.RefundNo = refundNo
			order.PlatformRefundNo = platformRefundNo
		return nil
	}
	// 2026-08-29 修复（中-3）：退款追回也走整数 BP，与入账口径一致
	clawback := model.IncomeFromGrossBP(amountCents, model.ShareRateToBP(s.cfg.CreatorShareRate))
		if clawback > 0 {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				First(&model.Creator{}, *drama.CreatorID).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.Creator{}).
				Where("id = ?", *drama.CreatorID).
				Updates(map[string]interface{}{
					"total_income_cents": gorm.Expr("GREATEST(total_income_cents - ?, 0)", clawback),
					"balance_cents":      gorm.Expr("GREATEST(balance_cents - ?, 0)", clawback),
				}).Error; err != nil {
				return err
			}
			statDate := refundedAt.Format("2006-01-02")
			if err := tx.Model(&model.CreatorStatsDaily{}).
				Where("creator_id = ? AND drama_id = ? AND stat_date = ?",
					*drama.CreatorID, order.DramaID, statDate).
				Update("income_cents", gorm.Expr("GREATEST(income_cents - ?, 0)", clawback)).Error; err != nil {
				return err
			}
		}

		// 回退 channel_income_daily（APP 内购渠道收益）
		refundStatDate := refundedAt.Format("2006-01-02")
		if err := tx.Model(&model.ChannelIncomeDaily{}).
			Where("drama_id = ? AND channel = ? AND stat_date = ?",
				order.DramaID, ChannelAppName, refundStatDate).
			Updates(map[string]interface{}{
				"gross_cents":  gorm.Expr("GREATEST(gross_cents - ?, 0)", amountCents),
				"income_cents": gorm.Expr("GREATEST(income_cents - ?, 0)", clawback),
			}).Error; err != nil {
			return err
		}

		order.Status = newStatus
		order.RefundAmountCents = newRefundTotal
		order.RefundedAt = &refundedAt
		order.RefundReason = reason
		order.RefundNo = refundNo
		order.PlatformRefundNo = platformRefundNo
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &order, nil
}

// SyncOrderStatus 主动查渠道侧订单状态,回写本地。用于 webhook 丢失/延迟时的兜底。
//
//   - 渠道侧 paid + 本地 pending  → 走 MarkOrderPaid(完整链路:解锁 + 分账)
//   - 渠道侧 closed + 本地 pending → 标记 closed
//   - 其它一致情况 → no-op
//   - 渠道侧状态比本地"更早"(例如本地已 paid 但渠道返回 pending)→ 不动,等下次回调
//
// 返回最新本地订单。
func (s *Service) SyncOrderStatus(orderNo string) (*model.Order, error) {
	var order model.Order
	if err := s.db.Where("order_no = ?", orderNo).First(&order).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrOrderNotFound
		}
		return nil, err
	}
	provider, err := s.payments.Get(order.PaymentMethod)
	if err != nil {
		return nil, err
	}
	state, err := provider.QueryOrder(orderNo)
	if err != nil {
		return nil, err
	}

	switch state.Status {
	case payment.StatusPaid:
		if order.Status == model.OrderStatusPending {
			paidAt := time.Now()
			if state.PaidAt != nil {
				paidAt = *state.PaidAt
			}
			amount := state.AmountCents
			if amount == 0 {
				amount = order.AmountCents // 渠道侧未返回金额时用本地金额做对账
			}
			if err := s.MarkOrderPaid(orderNo, state.PlatformTradeNo, order.PaymentMethod, amount, paidAt); err != nil {
				return nil, err
			}
		}
	case payment.StatusClosed:
		if order.Status == model.OrderStatusPending {
			if err := s.db.Model(&model.Order{}).
				Where("order_no = ? AND status = ?", orderNo, model.OrderStatusPending).
				Update("status", model.OrderStatusClosed).Error; err != nil {
				return nil, err
			}
		}
	}

	// 重新读一次返回最新状态。
	if err := s.db.Where("order_no = ?", orderNo).First(&order).Error; err != nil {
		return nil, err
	}
	return &order, nil
}

func generateOrderNo() string {
	now := time.Now()
	return fmt.Sprintf("%s%04d%05d", now.Format("20060102150405"), now.Nanosecond()/1000%10000, rand.Intn(100000))
}

// ErrRechargeNotFound 充值单不存在
var ErrRechargeNotFound = errors.New("充值单不存在")

// ErrRechargeAmountMismatch 充值回调金额不一致
var ErrRechargeAmountMismatch = errors.New("充值回调金额与充值单金额不一致")

// ErrRechargeExpired 充值单已过期，不能再标记已支付
var ErrRechargeExpired = errors.New("充值单已过期，不能再标记已支付")

// ErrRechargeMethodMismatch 充值回调渠道与充值单不一致
var ErrRechargeMethodMismatch = errors.New("充值回调渠道与充值单不一致")

// MarkRechargePaid 押金充值到账：更新充值单状态 → 加余额 → 写流水。
// 重复回调（充值单已 paid）幂等返回 nil。
func (s *Service) MarkRechargePaid(rechargeNo, platformTradeNo, paymentMethod string, amountCents int64, paidAt time.Time) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var rc model.DistributorRecharge
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("recharge_no = ?", rechargeNo).
			First(&rc).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRechargeNotFound
			}
			return err
		}
		if rc.Status == "paid" {
			return nil // 幂等
		}
		// 防御性兜底：过期充值单拒绝标记已支付，与订单策略一致
		if rc.ExpiredAt != nil && rc.ExpiredAt.Before(paidAt) {
			return ErrRechargeExpired
		}
		// 渠道一致性校验：回调渠道必须与充值单创建时指定的渠道一致，
		// 防 A 渠道付款被记到 B 渠道充值单（与订单侧 ErrPaymentMethodMismatch 对称）。
		// 历史数据 PaymentMethod 可能为空，空值跳过校验保持向后兼容。
		if rc.PaymentMethod != "" && rc.PaymentMethod != paymentMethod {
			return ErrRechargeMethodMismatch
		}
		if amountCents != rc.AmountCents {
			return ErrRechargeAmountMismatch
		}

		// 1. 更新充值单
		if err := tx.Model(&rc).Updates(map[string]interface{}{
			"status":            "paid",
			"paid_at":           paidAt,
			"platform_trade_no": platformTradeNo,
		}).Error; err != nil {
			return err
		}

		// 2. 加余额
		var dist model.Distributor
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&dist, rc.DistributorID).Error; err != nil {
			return err
		}
		dist.DepositAvailableCents += rc.AmountCents
		if err := tx.Save(&dist).Error; err != nil {
			return err
		}

		// 3. 写流水
		if err := tx.Create(&model.DistributorDepositTransaction{
			DistributorID:      rc.DistributorID,
			DramaID:            0, // 充值不关联具体剧集
			Type:               model.DepositTxRecharge,
			AmountCents:        rc.AmountCents,
			BalanceAfterCents:  dist.DepositAvailableCents,
			RelatedType:        "recharge",
			RelatedBusinessNo:  rc.RechargeNo,
			Remark:             "押金充值（" + paymentMethod + "）",
		}).Error; err != nil {
			return err
		}
		return nil
	})
}

// SyncRechargeStatus 主动查渠道侧充值单状态,回写本地。用于充值 webhook 丢失/延迟时的兜底,
// 与 SyncOrderStatus 对称:发行商付了钱但 webhook 没到,导致押金余额不增加、无法认领。
//
//   - 渠道侧 paid + 本地 pending  → 走 MarkRechargePaid(完整链路:加余额 + 写流水)
//   - 渠道侧 closed + 本地 pending → 标记 closed(发行商需重新发起充值)
//   - 其它一致情况 → no-op
func (s *Service) SyncRechargeStatus(rechargeNo string) (*model.DistributorRecharge, error) {
	var rc model.DistributorRecharge
	if err := s.db.Where("recharge_no = ?", rechargeNo).First(&rc).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRechargeNotFound
		}
		return nil, err
	}
	provider, err := s.payments.Get(rc.PaymentMethod)
	if err != nil {
		return nil, err
	}
	state, err := provider.QueryOrder(rechargeNo)
	if err != nil {
		return nil, err
	}

	switch state.Status {
	case payment.StatusPaid:
		if rc.Status == "pending" {
			paidAt := time.Now()
			if state.PaidAt != nil {
				paidAt = *state.PaidAt
			}
			amount := state.AmountCents
			if amount == 0 {
				amount = rc.AmountCents // 渠道侧未返回金额时用本地金额做对账
			}
			if err := s.MarkRechargePaid(rechargeNo, state.PlatformTradeNo, rc.PaymentMethod, amount, paidAt); err != nil {
				return nil, err
			}
		}
	case payment.StatusClosed:
		if rc.Status == "pending" {
			if err := s.db.Model(&model.DistributorRecharge{}).
				Where("recharge_no = ? AND status = ?", rechargeNo, "pending").
				Update("status", "closed").Error; err != nil {
				return nil, err
			}
		}
	}

	// 重新读一次返回最新状态。
	if err := s.db.Where("recharge_no = ?", rechargeNo).First(&rc).Error; err != nil {
		return nil, err
	}
	return &rc, nil
}
