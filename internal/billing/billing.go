package billing

import (
	"errors"
	"fmt"
	"math/rand"
	"time"

	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/payment"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

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

// CreateOrReuseOrder 实现"业务幂等"：
//  1. 用户对同一 episode 已支付 → 返回已解锁。
//  2. 已有 pending 且未过期订单 → 复用并刷新支付参数。
//  3. 其余创建新订单。
//
// 调用方可同时在 Header 里携带 Idempotency-Key，目前只 log 用于排查（Redis 接入后再做强幂等）。
type CreateOrderOutcome struct {
	AlreadyUnlocked bool
	Order           *model.Order
	PayParams       payment.PrepayParams
}

func (s *Service) CreateOrReuseOrder(userID uint64, dramaID, episodeID uint64, productID *uint64, method string) (*CreateOrderOutcome, error) {
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
	if ep.EpisodeNo <= drama.FreeEpisodes {
		return nil, ErrEpisodeFree
	}

	// 1. 已解锁直接返回
	var unlock model.EpisodeUnlock
	if err := s.db.Where("user_id = ? AND episode_id = ?", userID, episodeID).First(&unlock).Error; err == nil {
		return &CreateOrderOutcome{AlreadyUnlocked: true}, nil
	}

	// 2. 找 pending 未过期订单复用 / 3. 创建新订单（事务 + advisory lock 防并发重复下单）
	now := time.Now()
	var order model.Order
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

		var pending model.Order
		err := tx.Where("user_id = ? AND episode_id = ? AND status = ? AND (expired_at IS NULL OR expired_at > ?)",
			userID, episodeID, model.OrderStatusPending, now).
			Order("created_at desc").
			First(&pending).Error
		if err == nil {
			order = pending
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		amount := drama.PriceCents
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
	return s.attachPayParams(&order, method)
}

func (s *Service) attachPayParams(order *model.Order, method string) (*CreateOrderOutcome, error) {
	provider, err := s.payments.Get(method)
	if err != nil {
		return nil, err
	}
	params, err := provider.Prepay(payment.PrepayInput{
		OrderNo:     order.OrderNo,
		AmountCents: order.AmountCents,
		Subject:     fmt.Sprintf("剧集解锁 #%d", order.EpisodeID),
		UserID:      order.UserID,
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

		// 2. 写解锁（重复 ON CONFLICT DO NOTHING）
		unlock := model.EpisodeUnlock{
			UserID:    order.UserID,
			DramaID:   order.DramaID,
			EpisodeID: order.EpisodeID,
			OrderID:   &order.ID,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&unlock).Error; err != nil {
			return err
		}

		// 3. 分账（若短剧绑定了创作者）
		var drama model.Drama
		if err := tx.First(&drama, order.DramaID).Error; err != nil {
			return err
		}
		if drama.CreatorID == nil {
			return nil
		}
		creatorAmount := int64(float64(order.AmountCents) * s.cfg.CreatorShareRate)
		if creatorAmount <= 0 {
			return nil
		}

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
		statDate := paidAt.Format("2006-01-02")
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
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var orders []model.Order
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "order_no", "expired_at").
			Where("status = ? AND expired_at IS NOT NULL AND expired_at < ?", model.OrderStatusPending, now).
			Order("expired_at asc").
			Find(&orders).Error; err != nil {
			return err
		}
		if len(orders) == 0 {
			return nil
		}

		ids := make([]uint64, 0, len(orders))
		for i, order := range orders {
			ids = append(ids, order.ID)
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

func generateOrderNo() string {
	now := time.Now()
	return fmt.Sprintf("%s%04d%05d", now.Format("20060102150405"), now.Nanosecond()/1000%10000, rand.Intn(100000))
}
