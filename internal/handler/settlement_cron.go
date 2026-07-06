package handler

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"ai-drama-platform/internal/model"
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
		log.Printf("[bg] settlement cron started (每月 1 号 02:00 算上月 H2，每月 16 号 02:00 算本月 H1)")
		// 启动期先补一遍（容器被重启也不漏）
		s.maybeRunHalfMonthSettlement(time.Now())
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
	log.Printf("[bg] half-month settlement tick cycle=%s range=[%s, %s)", cycleKey, startStr, endStr)
	count, err := s.runSettlementForCycle(cycleKey, startStr, endStr)
	if err != nil {
		log.Printf("[bg] half-month settlement FAILED cycle=%s err=%v", cycleKey, err)
		return
	}
	log.Printf("[bg] half-month settlement done cycle=%s count=%d", cycleKey, count)
}

// runSettlementForCycle 跑一次半月结算——和原 runSettlementForPeriod 思路一致，
// 但用 cycleKey 去重而非 period+contract_no。
// 返回写入的 settlement 条数。
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
		creatorShareRate = 0.7
	}

	now := time.Now()
	// period 字段保留：取 YYYY-MM（半月所在自然月），与前端展示"周期：07 月"保持兼容
	// cycle_key 是新字段，做唯一约束
	period := startStr[:7] // "2026-07"
	created := 0
	for _, a := range aggs {
		contractNo := contractMap[a.CreatorID]
		if contractNo == "" {
			continue
		}
		// 2026-07-06 改：去重键用 cycle_key 而非 period+contract_no
		var existCount int64
		s.db.Model(&model.Settlement{}).Where("creator_id = ? AND cycle_key = ? AND contract_no = ?",
			a.CreatorID, cycleKey, contractNo).Count(&existCount)
		if existCount > 0 {
			continue
		}
		grossCents := int64(float64(a.IncomeCents) / creatorShareRate)
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
		if err := s.db.Create(&st).Error; err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}
