package reconcile

import (
	"fmt"
	"sort"
	"time"

	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/model"

	"gorm.io/gorm"
)

type Severity string

const (
	SeverityError Severity = "error"
	SeverityWarn  Severity = "warn"
)

type Issue struct {
	Severity Severity
	Code     string
	Message  string
}

type Report struct {
	CheckedAt          time.Time
	PaidOrderCount     int
	CreatorCount       int
	WithdrawalCount    int
	StatsRowCount      int
	MissingUnlockCount int
	Issues             []Issue
}

func (r Report) HasErrors() bool {
	for _, issue := range r.Issues {
		if issue.Severity == SeverityError {
			return true
		}
	}
	return false
}

func Run(db *gorm.DB, cfg config.Config, now time.Time) (Report, error) {
	report := Report{CheckedAt: now}

	if cfg.CreatorShareRate < 0 || cfg.CreatorShareRate > 1 {
		report.Issues = append(report.Issues, Issue{
			Severity: SeverityError,
			Code:     "invalid_creator_share_rate",
			Message:  fmt.Sprintf("CREATOR_SHARE_RATE 必须在 0~1 之间，当前值 %.4f", cfg.CreatorShareRate),
		})
	}

	var paidOrders []paidOrderRow
	if err := db.Table("orders AS o").
		Select("o.id, o.order_no, o.user_id, o.drama_id, o.episode_id, o.amount_cents, o.paid_at, d.creator_id").
		Joins("LEFT JOIN dramas AS d ON d.id = o.drama_id").
		Where("o.status = ?", model.OrderStatusPaid).
		Find(&paidOrders).Error; err != nil {
		return report, err
	}
	report.PaidOrderCount = len(paidOrders)

	expectedByCreator := map[uint64]int64{}
	expectedByStat := map[statKey]int64{}
	for _, order := range paidOrders {
		if order.PaidAt == nil {
			report.Issues = append(report.Issues, Issue{
				Severity: SeverityError,
				Code:     "paid_order_missing_paid_at",
				Message:  fmt.Sprintf("已支付订单 %s 缺少 paid_at", order.OrderNo),
			})
		}
		if order.CreatorID == nil {
			continue
		}
		// 2026-08-29 修复（中-3）：对账口径与入账口径统一为整数 BP
		creatorAmount := model.IncomeFromGrossBP(order.AmountCents, model.ShareRateToBP(cfg.CreatorShareRate))
		if creatorAmount <= 0 {
			continue
		}
		expectedByCreator[*order.CreatorID] += creatorAmount
		if order.PaidAt != nil {
			key := statKey{
				CreatorID: *order.CreatorID,
				DramaID:   order.DramaID,
				StatDate:  order.PaidAt.Format("2006-01-02"),
			}
			expectedByStat[key] += creatorAmount
		}
	}

	missingUnlocks, err := findMissingUnlocks(db)
	if err != nil {
		return report, err
	}
	report.MissingUnlockCount = len(missingUnlocks)
	for _, row := range missingUnlocks {
		report.Issues = append(report.Issues, Issue{
			Severity: SeverityError,
			Code:     "paid_order_missing_unlock",
			Message:  fmt.Sprintf("已支付订单 %s 缺少 user_id=%d episode_id=%d 的解锁记录", row.OrderNo, row.UserID, row.EpisodeID),
		})
	}

	duplicatePaidOrders, err := findDuplicatePaidOrders(db)
	if err != nil {
		return report, err
	}
	for _, row := range duplicatePaidOrders {
		report.Issues = append(report.Issues, Issue{
			Severity: SeverityError,
			Code:     "duplicate_paid_orders",
			Message:  fmt.Sprintf("user_id=%d episode_id=%d 存在 %d 笔已支付订单", row.UserID, row.EpisodeID, row.OrderCount),
		})
	}

	var creators []model.Creator
	if err := db.Find(&creators).Error; err != nil {
		return report, err
	}
	report.CreatorCount = len(creators)

	withdrawalsByCreator, paidWithdrawalsByCreator, withdrawalsCount, err := loadWithdrawalSums(db)
	if err != nil {
		return report, err
	}
	report.WithdrawalCount = withdrawalsCount

	for _, creator := range creators {
		expectedIncome := expectedByCreator[creator.ID]
		if creator.TotalIncomeCents != expectedIncome {
			report.Issues = append(report.Issues, Issue{
				Severity: SeverityError,
				Code:     "creator_total_income_mismatch",
				Message:  fmt.Sprintf("creator_id=%d total_income_cents=%d，按已支付订单应为 %d", creator.ID, creator.TotalIncomeCents, expectedIncome),
			})
		}

		expectedFrozen := withdrawalsByCreator[creator.ID]
		if creator.FrozenCents != expectedFrozen {
			report.Issues = append(report.Issues, Issue{
				Severity: SeverityError,
				Code:     "creator_frozen_mismatch",
				Message:  fmt.Sprintf("creator_id=%d frozen_cents=%d，按 pending/approved 提现应为 %d", creator.ID, creator.FrozenCents, expectedFrozen),
			})
		}

		accountedIncome := creator.BalanceCents + creator.FrozenCents + paidWithdrawalsByCreator[creator.ID]
		if creator.TotalIncomeCents != accountedIncome {
			report.Issues = append(report.Issues, Issue{
				Severity: SeverityError,
				Code:     "creator_balance_formula_mismatch",
				Message:  fmt.Sprintf("creator_id=%d 账面不平：total_income=%d，balance+frozen+paid_withdrawals=%d", creator.ID, creator.TotalIncomeCents, accountedIncome),
			})
		}
		if creator.BalanceCents < 0 || creator.FrozenCents < 0 {
			report.Issues = append(report.Issues, Issue{
				Severity: SeverityError,
				Code:     "creator_negative_balance",
				Message:  fmt.Sprintf("creator_id=%d 出现负余额：balance=%d frozen=%d", creator.ID, creator.BalanceCents, creator.FrozenCents),
			})
		}
	}

	var stats []model.CreatorStatsDaily
	if err := db.Find(&stats).Error; err != nil {
		return report, err
	}
	report.StatsRowCount = len(stats)
	actualByStat := map[statKey]int64{}
	for _, stat := range stats {
		key := statKey{CreatorID: stat.CreatorID, DramaID: stat.DramaID, StatDate: stat.StatDate}
		actualByStat[key] = stat.IncomeCents
	}
	for _, key := range sortedStatKeys(expectedByStat) {
		expected := expectedByStat[key]
		if actualByStat[key] != expected {
			report.Issues = append(report.Issues, Issue{
				Severity: SeverityError,
				Code:     "creator_stats_income_mismatch",
				Message:  fmt.Sprintf("creator_stats_daily creator_id=%d drama_id=%d date=%s income_cents=%d，按订单应为 %d", key.CreatorID, key.DramaID, key.StatDate, actualByStat[key], expected),
			})
		}
	}
	for _, key := range sortedStatKeys(actualByStat) {
		if _, ok := expectedByStat[key]; !ok && actualByStat[key] != 0 {
			report.Issues = append(report.Issues, Issue{
				Severity: SeverityWarn,
				Code:     "creator_stats_income_without_paid_order",
				Message:  fmt.Sprintf("creator_stats_daily creator_id=%d drama_id=%d date=%s 有收入 %d，但没有对应已支付订单", key.CreatorID, key.DramaID, key.StatDate, actualByStat[key]),
			})
		}
	}

	sort.Slice(report.Issues, func(i, j int) bool {
		if report.Issues[i].Severity != report.Issues[j].Severity {
			return report.Issues[i].Severity < report.Issues[j].Severity
		}
		if report.Issues[i].Code != report.Issues[j].Code {
			return report.Issues[i].Code < report.Issues[j].Code
		}
		return report.Issues[i].Message < report.Issues[j].Message
	})
	return report, nil
}

type paidOrderRow struct {
	ID          uint64
	OrderNo     string
	UserID      uint64
	DramaID     uint64
	EpisodeID   uint64
	AmountCents int64
	PaidAt      *time.Time
	CreatorID   *uint64
}

type missingUnlockRow struct {
	OrderNo   string
	UserID    uint64
	EpisodeID uint64
}

type duplicatePaidOrderRow struct {
	UserID     uint64
	EpisodeID  uint64
	OrderCount int64
}

type statKey struct {
	CreatorID uint64
	DramaID   uint64
	StatDate  string
}

func findMissingUnlocks(db *gorm.DB) ([]missingUnlockRow, error) {
	var rows []missingUnlockRow
	err := db.Table("orders AS o").
		Select("o.order_no, o.user_id, o.episode_id").
		Joins("LEFT JOIN episode_unlocks AS eu ON eu.user_id = o.user_id AND eu.episode_id = o.episode_id").
		Where("o.status = ? AND eu.id IS NULL", model.OrderStatusPaid).
		Order("o.id asc").
		Find(&rows).Error
	return rows, err
}

func findDuplicatePaidOrders(db *gorm.DB) ([]duplicatePaidOrderRow, error) {
	var rows []duplicatePaidOrderRow
	err := db.Table("orders").
		Select("user_id, episode_id, COUNT(*) AS order_count").
		Where("status = ?", model.OrderStatusPaid).
		Group("user_id, episode_id").
		Having("COUNT(*) > 1").
		Order("user_id asc, episode_id asc").
		Scan(&rows).Error
	return rows, err
}

func loadWithdrawalSums(db *gorm.DB) (map[uint64]int64, map[uint64]int64, int, error) {
	var withdrawals []model.Withdrawal
	if err := db.Find(&withdrawals).Error; err != nil {
		return nil, nil, 0, err
	}

	frozenByCreator := map[uint64]int64{}
	paidByCreator := map[uint64]int64{}
	for _, withdrawal := range withdrawals {
		switch withdrawal.Status {
		case model.WithdrawalStatusPending, model.WithdrawalStatusApproved:
			frozenByCreator[withdrawal.CreatorID] += withdrawal.AmountCents
		case model.WithdrawalStatusPaid:
			paidByCreator[withdrawal.CreatorID] += withdrawal.AmountCents
		}
	}
	return frozenByCreator, paidByCreator, len(withdrawals), nil
}

func sortedStatKeys(values map[statKey]int64) []statKey {
	keys := make([]statKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].CreatorID != keys[j].CreatorID {
			return keys[i].CreatorID < keys[j].CreatorID
		}
		if keys[i].DramaID != keys[j].DramaID {
			return keys[i].DramaID < keys[j].DramaID
		}
		return keys[i].StatDate < keys[j].StatDate
	})
	return keys
}
