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

func (s *Service) CreateOrReuseOrder(userID uint64, dramaID, episodeID uint64, productID *uint64, method, payScene, clientIP string) (*CreateOrderOutcome, error) {
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

// RefundOrder 发起退款。
//
//  1. 校验:订单存在、状态是 paid 或 partial_refunded、本次退款 + 已退累计 <= 订单金额。
//  2. 幂等:同 refundNo 重入直接返回当前订单(不再调渠道,避免重复退款)。
//  3. 调 Provider.Refund(支付宝同步、微信异步,Provider 内部已抹平)。
//  4. 事务内:更新订单(累计退款 + 状态 + 退款元数据),按比例回退创作者余额 + 当日聚合;
//     全额退款则把 status 置 refunded、部分退款置 partial_refunded。
//  5. 解锁记录保持不动:产品不在意"退款后还能不能看",运营可单独删除。
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

	// 先在事务外取一次订单做基本校验 + 幂等判断,避免无谓地把渠道调用塞进事务内。
	var order model.Order
	if err := s.db.Where("order_no = ?", orderNo).First(&order).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrOrderNotFound
		}
		return nil, err
	}
	if order.Status != model.OrderStatusPaid && order.Status != model.OrderStatusPartialRefunded {
		return nil, ErrRefundNotAllowed
	}
	// 幂等:相同 refundNo 直接返回当前订单,不再调渠道。
	if order.RefundNo == refundNo {
		return &order, nil
	}
	if order.RefundAmountCents+amountCents > order.AmountCents {
		return nil, ErrRefundAmountInvalid
	}

	// 调渠道(可能是网络长操作,放在事务外)。
	provider, err := s.payments.Get(order.PaymentMethod)
	if err != nil {
		return nil, err
	}
	result, err := provider.Refund(payment.RefundInput{
		OrderNo:         order.OrderNo,
		PlatformTradeNo: order.PlatformTradeNo,
		RefundNo:        refundNo,
		AmountCents:     amountCents,
		Reason:          reason,
	})
	if err != nil {
		return nil, err
	}
	if !result.Success {
		return nil, payment.ErrRefundFailed
	}

	refundedAt := result.RefundedAt
	if refundedAt.IsZero() {
		refundedAt = time.Now()
	}

	// 事务内:更新订单 + 回退创作者收益(按比例)。
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var o model.Order
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("order_no = ?", orderNo).First(&o).Error; err != nil {
			return err
		}
		// 双重校验:并发场景下另一笔退款可能已落库。
		if o.Status != model.OrderStatusPaid && o.Status != model.OrderStatusPartialRefunded {
			return ErrRefundNotAllowed
		}
		if o.RefundNo == refundNo {
			order = o
			return nil
		}
		if o.RefundAmountCents+amountCents > o.AmountCents {
			return ErrRefundAmountInvalid
		}

		newRefundTotal := o.RefundAmountCents + amountCents
		newStatus := model.OrderStatusPartialRefunded
		if newRefundTotal >= o.AmountCents {
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
		if err := tx.Model(&o).Updates(updates).Error; err != nil {
			return err
		}

		// 回退分账:按本次退款额占订单金额的比例从 creator 余额扣回。
		// 兜底:扣回后余额不允许为负(由 DB CHECK 兜底也行,这里先在应用层做防御)。
		var drama model.Drama
		if err := tx.First(&drama, o.DramaID).Error; err != nil {
			return err
		}
		if drama.CreatorID == nil {
			order = o
			order.Status = newStatus
			order.RefundAmountCents = newRefundTotal
			order.RefundedAt = &refundedAt
			order.RefundReason = reason
			order.RefundNo = refundNo
			order.PlatformRefundNo = result.PlatformRefundNo
			return nil
		}
		clawback := int64(float64(amountCents) * s.cfg.CreatorShareRate)
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
					*drama.CreatorID, o.DramaID, statDate).
				Update("income_cents", gorm.Expr("GREATEST(income_cents - ?, 0)", clawback)).Error; err != nil {
				return err
			}
		}

		order = o
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
