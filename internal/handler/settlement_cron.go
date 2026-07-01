package handler

import (
	"context"
	"log"
	"strconv"
	"time"

	"ai-drama-platform/internal/model"
)

// startSettlementCron —— 月度结算 cron，每月 1 号 02:00 自动生成上个月结算单。
// 启动期用 startupRunSettlement() 立刻补一遍（容器重启时不漏跑一次）。
// 由 Server.StartBackground() 启动一个独立 ticker，每小时醒一次，命中 02:00 才真跑。
// 幂等：和 adminGenerateSettlements 一样对 (creator, period, contract) 查重后跳过，
// 重复跑不会产生重复 settlement。
func (s *Server) startSettlementCron(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		log.Printf("[bg] settlement cron started (每月 1 号 02:00 自动跑)")
		// 启动期先补一遍（容器被重启也不漏）
		s.maybeRunMonthlySettlement(time.Now())
		for {
			select {
			case <-ctx.Done():
				log.Printf("[bg] settlement cron stopped")
				return
			case now := <-ticker.C:
				s.maybeRunMonthlySettlement(now)
			}
		}
	}()
}

func (s *Server) maybeRunMonthlySettlement(now time.Time) {
	// 每月 1 号 02:00 ~ 02:59 触发（小时级 ticker，可能在 02:xx 任意整点命中）
	if now.Day() != 1 || now.Hour() != 2 {
		return
	}
	// 目标：上个月
	prev := now.AddDate(0, -1, 0)
	period := prev.Format("2006-01")
	log.Printf("[bg] monthly settlement tick period=%s", period)
	count, err := s.runSettlementForPeriod(period)
	if err != nil {
		log.Printf("[bg] monthly settlement FAILED period=%s err=%v", period, err)
		return
	}
	log.Printf("[bg] monthly settlement done period=%s count=%d", period, count)
}

// runSettlementForPeriod 跑一次月度结算，复用 adminGenerateSettlements 的核心逻辑。
// 返回写入的 settlement 条数。
func (s *Server) runSettlementForPeriod(period string) (int, error) {
	if _, err := time.Parse("2006-01", period); err != nil {
		return 0, err
	}
	startStr := period + "-01"
	endMonth, _ := time.Parse("2006-01", period)
	endStr := endMonth.AddDate(0, 1, 0).Format("2006-01-02")

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
	created := 0
	for _, a := range aggs {
		contractNo := contractMap[a.CreatorID]
		if contractNo == "" {
			continue
		}
		var existCount int64
		s.db.Model(&model.Settlement{}).Where("creator_id = ? AND period = ? AND contract_no = ?",
			a.CreatorID, period, contractNo).Count(&existCount)
		if existCount > 0 {
			continue
		}
		grossCents := int64(float64(a.IncomeCents) / creatorShareRate)
		platformCents := grossCents - a.IncomeCents
		bizNo := "ST" + now.Format("200601") + "-" + strconv.FormatUint(uint64(now.UnixNano()%10000), 10)
		openedAt := now
		st := model.Settlement{
			SettlementNo:  bizNo,
			CreatorID:     a.CreatorID,
			ContractNo:    contractNo,
			Period:        period,
			GrossCents:    grossCents,
			PlatformCents: platformCents,
			NetCents:      a.IncomeCents,
			Status:        model.SettlementStatusOpen,
			OpenedAt:      &openedAt,
			Remark:        "auto-cron",
		}
		if err := s.db.Create(&st).Error; err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}
