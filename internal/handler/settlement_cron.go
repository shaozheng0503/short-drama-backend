package handler

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"ai-drama-platform/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 2026-07-06 改：结算周期由「月度」改为「半月度」（吴建棉 7/3 群确认）。
// 每月 15 号 + 月末（每月最后一天）02:00 各跑一次：
//   - 1 号  02:00 → 结算上月 H2（16 日 ~ 上月最后一天）
//   - 16 号 02:00 → 结算本月 H1（1 日 ~ 15 日）
//
// cycleKey 格式：YYYY-MM-H1 / YYYY-MM-H2（例如 2026-07-H1）
// 用于 (creator_id, contract_no, cycle_key) 唯一去重，重复跑 cron 不会生成重复 settlement。
//
// 注意：旧月度 settlement（period 形如 2026-07，cycle_key 为空）和新半月 settlement
// 会在同月共存——创作者端 list 会按 period desc + id desc 全部列出。
// 老数据归档（迁移到新格式）是产品决定，代码层不动。

func (s *Server) startSettlementCron(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		log.Printf("[bg] settlement cron started (auto-run disabled 2026-08-12, waiting for manual trigger via POST /v1/admin/settlements/generate)")
		for {
			select {
			case <-ctx.Done():
				log.Printf("[bg] settlement cron stopped")
				return
			case now := <-ticker.C:
				s.maybeRunHalfMonthSettlement(now)
			}
		}
	}()
}

// maybeRunHalfMonthSettlement 根据当前日期判断是否触发半月结算。
// 触发条件：
//   - day == 1 && hour == 2  → 结算上月 H2（prev.Year()-prev.Month() 的 16 日 ~ 末日）
//   - day == 16 && hour == 2 → 结算本月 H1（当前年-月的 1 日 ~ 15 日）
func (s *Server) maybeRunHalfMonthSettlement(now time.Time) {
	if now.Hour() != 2 {
		return
	}
	var cycleKey string
	var startDate, endDate time.Time
	switch now.Day() {
	case 1:
		// 1 号 → 算上月 H2
		prev := now.AddDate(0, -1, 0)
		cycleKey = fmt.Sprintf("%04d-%02d-H2", prev.Year(), int(prev.Month()))
		// 上月 16 日 ~ 上月最后一天
		firstOfPrev := time.Date(prev.Year(), prev.Month(), 1, 0, 0, 0, 0, time.UTC)
		startDate = firstOfPrev.AddDate(0, 0, 15) // 16 日
		endDate = firstOfPrev.AddDate(0, 1, -1)   // 上月最后一天
	case 16:
		// 16 号 → 算本月 H1
		cycleKey = fmt.Sprintf("%04d-%02d-H1", now.Year(), int(now.Month()))
		firstOfCur := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		startDate = firstOfCur
		endDate = firstOfCur.AddDate(0, 0, 14) // 15 日
	default:
		return
	}
	// 查询范围：[startDate, endDate+1)（半开区间）
	startStr := startDate.Format("2006-01-02")
	endStr := endDate.AddDate(0, 0, 1).Format("2006-01-02")
	// 2026-08-12 改：停 cron 自动执行，改为财务手动触发 POST /v1/admin/settlements/generate
	// 原因：cron 在 02:00 自动跑，如果财务还没导入完收入，结算金额会偏低且无法补录
	log.Printf("[bg] half-month settlement tick cycle=%s range=[%s, %s) — cron auto-run disabled, waiting for manual trigger", cycleKey, startStr, endStr)
}

// runSettlementForCycle 跑一次半月结算——和原 runSettlementForPeriod 思路一致，
// 但用 cycleKey 去重而非 period+contract_no。
// 返回写入的 settlement 条数。

// reverseGrossFromIncome —— 由创作者实得收入反推平台自有毛收入（整数 BP 运算，中-2 修复）。
// 分账正向公式：income = gross * creatorShareBP / 10000，
// 反推：gross = income * 10000 / creatorShareBP（整数除法，无 float 舍入误差）。
// creatorShareBP 非法（<=0 或 >10000）时按 5000（50%）兜底。
func reverseGrossFromIncome(incomeCents, creatorShareBP int64) int64 {
	if incomeCents <= 0 {
		return 0
	}
	if creatorShareBP <= 0 || creatorShareBP > int64(model.ShareRatioBPFull) {
		creatorShareBP = int64(model.ShareRatioBPFull) / 2
	}
	return incomeCents * int64(model.ShareRatioBPFull) / creatorShareBP
}

func (s *Server) runSettlementForCycle(cycleKey, startStr, endStr string) (int, error) {
	type creatorAgg struct {
		CreatorID   uint64
		IncomeCents int64
		PlayCount   int64
	}
	var aggs []creatorAgg
	s.db.Table("creator_stats_daily").
		Select("creator_id, COALESCE(SUM(income_cents),0) AS income_cents, COALESCE(SUM(play_count),0) AS play_count").
		Where("stat_date >= ? AND stat_date < ?", startStr, endStr).
		Group("creator_id").Scan(&aggs)

	// 查询渠道真实毛收入（channel_income_daily），用于计算结算单 gross_cents
	type channelGrossAgg struct {
		CreatorID   uint64
		GrossCents  int64
		IncomeCents int64
	}
	var channelGrossAggs []channelGrossAgg
	s.db.Table("channel_income_daily").
		Select("creator_id, COALESCE(SUM(gross_cents),0) AS gross_cents, COALESCE(SUM(income_cents),0) AS income_cents").
		Where("stat_date >= ? AND stat_date < ?", startStr, endStr).
		Group("creator_id").Scan(&channelGrossAggs)
	channelGrossMap := map[uint64]int64{}  // creator_id → 渠道毛收入
	channelIncomeMap := map[uint64]int64{} // creator_id → 渠道创作者收入
	for _, ca := range channelGrossAggs {
		channelGrossMap[ca.CreatorID] = ca.GrossCents
		channelIncomeMap[ca.CreatorID] = ca.IncomeCents
	}

	// 创作者合同映射：取最新一份
	type cc struct {
		CreatorID  uint64
		ContractNo string
	}
	var ccs []cc
	s.db.Table("contracts").Select("creator_id, contract_no").
		Order("created_at desc").Scan(&ccs)
	contractMap := map[uint64]string{}
	for _, c := range ccs {
		if _, ok := contractMap[c.CreatorID]; !ok {
			contractMap[c.CreatorID] = c.ContractNo
		}
	}

	creatorShareRate := s.cfg.CreatorShareRate
	if creatorShareRate <= 0 || creatorShareRate > 1 {
		// 2026-08-29 修复：兜底从 0.7 改为 0.5，与 config.go 默认值对齐，避免兜底口径与正常口径不一致
		creatorShareRate = 0.5
	}
	// 2026-08-29 修复：反推毛收入改用整数 BP 运算（中-2）
	creatorShareBP := int64(5000)
	if creatorShareRate > 0 && creatorShareRate <= 1 {
		creatorShareBP = int64(math.Round(creatorShareRate * float64(model.ShareRatioBPFull)))
	}

	now := time.Now()
	// period 字段保留：取 YYYY-MM（半月所在自然月），与前端展示"周期：07 月"保持兼容
	// cycle_key 是新字段，做唯一约束
	period := startStr[:7] // "2026-07"
	created := 0
	for _, a := range aggs {
		contractNo := contractMap[a.CreatorID]
		if contractNo == "" {
			// 创作者没有合同记录（短剧在自动创建合同逻辑上线前发布，或收入来自财务导入），
			// 自动创建一份默认合同，避免结算单被静默跳过导致生成 0 笔。
			contractNo = generateContractNo()
			ct := model.Contract{
				CreatorID:  a.CreatorID,
				ContractNo: contractNo,
				Status:     model.ContractStatusPending,
			}
			if err := s.db.Create(&ct).Error; err != nil {
				log.Printf("[settlement] auto-create contract for creator=%d failed: %v, skipping", a.CreatorID, err)
				continue
			}
			log.Printf("[settlement] auto-created contract %s for creator=%d (no existing contract)", contractNo, a.CreatorID)
			contractMap[a.CreatorID] = contractNo
		}
		// 2026-07-06 改：去重键用 cycle_key 而非 period+contract_no
		var existCount int64
		s.db.Model(&model.Settlement{}).Where("creator_id = ? AND cycle_key = ? AND contract_no = ?",
			a.CreatorID, cycleKey, contractNo).Count(&existCount)
		if existCount > 0 {
			continue
		}
		// gross_cents = 渠道真实毛收入 + 平台自有收入反推
		// 渠道毛收入直接取 channel_income_daily.gross_cents
		// 平台自有收入（订单分账）按整数 BP 反推，消除 float 除法舍入误差
		channelGross := channelGrossMap[a.CreatorID]
		channelIncome := channelIncomeMap[a.CreatorID]
		platformIncome := a.IncomeCents - channelIncome // 平台自有收入
		platformGross := int64(0)
		if platformIncome > 0 {
			platformGross = reverseGrossFromIncome(platformIncome, creatorShareBP)
		}
		grossCents := channelGross + platformGross
		platformCents := grossCents - a.IncomeCents
		bizNo := "ST" + now.Format("200601") + "-" + strconv.FormatUint(uint64(now.UnixNano()%10000), 10)
		openedAt := now
		periodRange := startStr + " ~ " + endStr
		st := model.Settlement{
			SettlementNo:  bizNo,
			CreatorID:     a.CreatorID,
			ContractNo:    contractNo,
			Period:        period,    // 兼容字段，YYYY-MM
			CycleKey:      cycleKey,  // 新字段，YYYY-MM-H1/H2
			PeriodRange:   periodRange,
			GrossCents:    grossCents,
			PlatformCents: platformCents,
			NetCents:      a.IncomeCents,
			Status:        model.SettlementStatusOpen,
			OpenedAt:      &openedAt,
			Remark:        "auto-cron-half-month",
		}
		if err := s.db.Transaction(func(tx *gorm.DB) error {
			// 事务内重新查重 + Create，防止并发 cron 或手动生成产生重复结算单
			var existCount int64
			tx.Model(&model.Settlement{}).Where("creator_id = ? AND cycle_key = ? AND contract_no = ?",
				a.CreatorID, cycleKey, contractNo).Count(&existCount)
			if existCount > 0 {
				return nil // 已存在，跳过
			}
			if err := tx.Create(&st).Error; err != nil {
				if isUniqueViolation(err) {
					// settlement_no 唯一索引兜底：并发创建被拦截，跳过
					return nil
				}
				return err
			}
			created++
			// 2026-07-06 加 P1-5：时间线（系统事件）
			s.recordTransition("settlement", st.ID, "", model.SettlementStatusOpen, "system", nil, "系统算账生成结算单（半月度）", map[string]interface{}{
				"cycle_key":    cycleKey,
				"period_range": periodRange,
				"net_cents":    st.NetCents,
			})
			return nil
		}); err != nil {
			return created, err
		}
	}
	return created, nil
}

// recalcOpenSettlementsForDateRange 重算指定日期范围内受影响的 open 状态结算单。
// 2026-08-12 新增：财务补导收入后，已生成的 open 结算单金额会偏低，需按最新 creator_stats_daily 重新汇总。
// 对于非 open 状态（invoiced/paid/void）的结算单，不更新，计入 blocked。
// 返回 (已补录结算单数, 被阻塞结算单数, error)
func (s *Server) recalcOpenSettlementsForDateRange(tx *gorm.DB, statDates []string) (int, int, error) {
	if len(statDates) == 0 {
		return 0, 0, nil
	}

	// 去重并计算受影响的 cycle_keys 及其日期范围
	cycleKeySet := map[string]struct{}{}
	cycleDateRange := map[string][2]string{} // cycleKey → [startStr, endStr)
	for _, ds := range statDates {
		if len(ds) < 10 {
			continue
		}
		year, err1 := strconv.Atoi(ds[:4])
		month, err2 := strconv.Atoi(ds[5:7])
		day, err3 := strconv.Atoi(ds[8:10])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		var half string
		var startDate, endDate time.Time
		firstOfMonth := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		if day <= 15 {
			half = "H1"
			startDate = firstOfMonth
			endDate = firstOfMonth.AddDate(0, 0, 14) // 15日
		} else {
			half = "H2"
			startDate = firstOfMonth.AddDate(0, 0, 15) // 16日
			endDate = firstOfMonth.AddDate(0, 1, -1)    // 月末
		}
		cycleKey := fmt.Sprintf("%04d-%02d-%s", year, month, half)
		if _, exists := cycleKeySet[cycleKey]; exists {
			continue
		}
		cycleKeySet[cycleKey] = struct{}{}
		startStr := startDate.Format("2006-01-02")
		endStr := endDate.AddDate(0, 0, 1).Format("2006-01-02") // 半开区间
		cycleDateRange[cycleKey] = [2]string{startStr, endStr}
	}

	supplemented := 0
	blocked := 0
	now := time.Now()

	creatorShareRate := s.cfg.CreatorShareRate
	if creatorShareRate <= 0 || creatorShareRate > 1 {
		// 2026-08-29 修复：兜底从 0.7 改为 0.5，与 config.go 默认值对齐
		creatorShareRate = 0.5
	}
	// 2026-08-29 修复：反推毛收入改用整数 BP 运算（中-2）
	creatorShareBP := int64(5000)
	if creatorShareRate > 0 && creatorShareRate <= 1 {
		creatorShareBP = int64(math.Round(creatorShareRate * float64(model.ShareRatioBPFull)))
	}

	for cycleKey := range cycleKeySet {
		dateRange := cycleDateRange[cycleKey]
		startStr, endStr := dateRange[0], dateRange[1]

		// 查找该周期的所有结算单
		var settlements []model.Settlement
		if err := tx.Where("cycle_key = ?", cycleKey).Find(&settlements).Error; err != nil {
			return supplemented, blocked, err
		}
		if len(settlements) == 0 {
			continue // 该周期还没有结算单，无需补录
		}

		// 重新聚合 creator_stats_daily
		type creatorAgg struct {
			CreatorID   uint64
			IncomeCents int64
			PlayCount   int64
		}
		var aggs []creatorAgg
		tx.Table("creator_stats_daily").
			Select("creator_id, COALESCE(SUM(income_cents),0) AS income_cents, COALESCE(SUM(play_count),0) AS play_count").
			Where("stat_date >= ? AND stat_date < ?", startStr, endStr).
			Group("creator_id").Scan(&aggs)
		aggMap := map[uint64]creatorAgg{}
		for _, a := range aggs {
			aggMap[a.CreatorID] = a
		}

		// 查询渠道真实毛收入（channel_income_daily）
		type suppChannelAgg struct {
			CreatorID   uint64
			GrossCents  int64
			IncomeCents int64
		}
		var suppChannelAggs []suppChannelAgg
		tx.Table("channel_income_daily").
			Select("creator_id, COALESCE(SUM(gross_cents),0) AS gross_cents, COALESCE(SUM(income_cents),0) AS income_cents").
			Where("stat_date >= ? AND stat_date < ?", startStr, endStr).
			Group("creator_id").Scan(&suppChannelAggs)
		suppChannelGrossMap := map[uint64]int64{}
		suppChannelIncomeMap := map[uint64]int64{}
		for _, ca := range suppChannelAggs {
			suppChannelGrossMap[ca.CreatorID] = ca.GrossCents
			suppChannelIncomeMap[ca.CreatorID] = ca.IncomeCents
		}

		for _, st := range settlements {
			if st.Status != model.SettlementStatusOpen {
				blocked++
				continue
			}

			a, ok := aggMap[st.CreatorID]
			if !ok {
				continue // 该创作者在此周期暂无收益数据
			}

			// gross_cents = 渠道真实毛收入 + 平台自有收入反推
			suppChannelGross := suppChannelGrossMap[a.CreatorID]
			suppChannelIncome := suppChannelIncomeMap[a.CreatorID]
			suppPlatformIncome := a.IncomeCents - suppChannelIncome
			suppPlatformGross := int64(0)
			if suppPlatformIncome > 0 {
				suppPlatformGross = reverseGrossFromIncome(suppPlatformIncome, creatorShareBP)
			}
			newGrossCents := suppChannelGross + suppPlatformGross
			newPlatformCents := newGrossCents - a.IncomeCents
			newNetCents := a.IncomeCents

			// 金额未变化则跳过
			if st.GrossCents == newGrossCents && st.NetCents == newNetCents && st.PlatformCents == newPlatformCents {
				continue
			}

			// 行锁重校验
			var locked model.Settlement
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, st.ID).Error; err != nil {
				return supplemented, blocked, err
			}
			if locked.Status != model.SettlementStatusOpen {
				blocked++
				continue
			}

			oldNet := locked.NetCents
			supplementTag := "；supplemented-" + now.Format("200601021504")
			newRemark := locked.Remark
			if !strings.Contains(newRemark, "supplemented-") {
				newRemark = newRemark + supplementTag
			}

			if err := tx.Model(&locked).Updates(map[string]interface{}{
				"gross_cents":    newGrossCents,
				"platform_cents": newPlatformCents,
				"net_cents":      newNetCents,
				"remark":         newRemark,
			}).Error; err != nil {
				return supplemented, blocked, err
			}

			s.recordTransition("settlement", locked.ID, model.SettlementStatusOpen, model.SettlementStatusOpen, "system", nil,
				fmt.Sprintf("收入补录：净额 %d→%d（差额 %d）", oldNet, newNetCents, newNetCents-oldNet),
				map[string]interface{}{
					"cycle_key":      cycleKey,
					"old_net_cents":  oldNet,
					"new_net_cents":  newNetCents,
					"delta_cents":    newNetCents - oldNet,
				})
			supplemented++
		}

		// ---- 发行商结算单补录 ----
		// 查找该周期的所有发行商结算单
		var distSettlements []model.DistributorSettlement
		if err := tx.Where("cycle_key = ?", cycleKey).Find(&distSettlements).Error; err != nil {
			return supplemented, blocked, err
		}
		if len(distSettlements) == 0 {
			continue // 该周期还没有发行商结算单，无需补录
		}

		// 重新聚合 distributor_income_daily
		type distAgg struct {
			DistributorID uint64
			GrossCents    int64
			IncomeCents   int64
		}
		var distAggs []distAgg
		tx.Table("distributor_income_daily").
			Select("distributor_id, COALESCE(SUM(gross_cents),0) AS gross_cents, COALESCE(SUM(income_cents),0) AS income_cents").
			Where("stat_date >= ? AND stat_date < ?", startStr, endStr).
			Group("distributor_id").Scan(&distAggs)
		distAggMap := map[uint64]distAgg{}
		for _, a := range distAggs {
			distAggMap[a.DistributorID] = a
		}

		for _, dst := range distSettlements {
			if dst.Status != model.DistSettlementPendingPayment {
				blocked++
				continue
			}

			a, ok := distAggMap[dst.DistributorID]
			if !ok {
				continue
			}

			// 2026-08-29 修复（高-2）：去掉 45/55 硬编码，改为数据驱动口径：
			// 发行商实得 = Σ distributor_income_daily.income_cents（行级已按 E 列 BP 折算），
			// 平台分成 = gross - net，与首次生成口径（runDistributorSettlementForCycle）完全一致。
			newGrossCents := a.GrossCents
			newNetCents := a.IncomeCents
			newPlatformCents := newGrossCents - newNetCents

			// 金额未变化则跳过
			if dst.GrossCents == newGrossCents && dst.NetCents == newNetCents && dst.PlatformCents == newPlatformCents {
				continue
			}

			// 行锁重校验
			var locked model.DistributorSettlement
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, dst.ID).Error; err != nil {
				return supplemented, blocked, err
			}
			if locked.Status != model.DistSettlementPendingPayment {
				blocked++
				continue
			}

			oldGross := locked.GrossCents
			supplementTag := "；supplemented-" + now.Format("200601021504")
			newRemark := locked.Remark
			if !strings.Contains(newRemark, "supplemented-") {
				newRemark = newRemark + supplementTag
			}

			// 重新计算应付金额（payable = 平台分成 newPlatformCents - deducted_deposit，与首次生成口径一致）
			payable := newPlatformCents - locked.DeductedDepositCents
			if payable < 0 {
				payable = 0
			}

			if err := tx.Model(&locked).Updates(map[string]interface{}{
				"gross_cents":          newGrossCents,
				"platform_cents":       newPlatformCents,
				"net_cents":            newNetCents,
				"withdrawable_cents":   payable,
				"payable_cents":        payable,
				"remark":               newRemark,
			}).Error; err != nil {
				return supplemented, blocked, err
			}

			s.recordTransition("distributor_settlement", locked.ID, model.DistSettlementPendingPayment, model.DistSettlementPendingPayment, "system", nil,
				fmt.Sprintf("收入补录：总额 %d→%d（差额 %d）", oldGross, newGrossCents, newGrossCents-oldGross),
				map[string]interface{}{
					"cycle_key":        cycleKey,
					"old_gross_cents":  oldGross,
					"new_gross_cents":  newGrossCents,
					"delta_cents":      newGrossCents - oldGross,
				})
			supplemented++
		}
	}

	return supplemented, blocked, nil
}
