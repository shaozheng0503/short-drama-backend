// Package main:一次性集成测试,直接连本地 ai_drama DB 跑 billing.RefundOrder / SyncOrderStatus 走真实事务。
// 用法:cd DramaBackend && source .env && go run ./cmd/test-refund
//
// 测试目标订单:MOCK-PAID-0001(990 分 wechat),所属 drama_id=23,creator_id=9。
// 测完会回滚到初始状态(status=paid, refund_amount_cents=0, creator.balance_cents 还原)。
package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"ai-drama-platform/internal/billing"
	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/database"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/payment"

	"gorm.io/gorm"
)

const testOrderNo = "MOCK-PAID-0001"

type checker struct{ failed int }

func (c *checker) ok(name string, cond bool, args ...any) {
	if cond {
		fmt.Printf("  ✓ %s\n", name)
		return
	}
	c.failed++
	fmt.Printf("  ✗ %s %v\n", name, args)
}
func (c *checker) section(name string) { fmt.Printf("\n=== %s ===\n", name) }

func fetchCreator(db *gorm.DB, id uint64) model.Creator {
	var c model.Creator
	if err := db.First(&c, id).Error; err != nil {
		log.Fatalf("fetchCreator(%d): %v", id, err)
	}
	return c
}

func resetOrder(db *gorm.DB, orig model.Order, origCreator model.Creator) {
	// 订单回到 paid + 清退款元数据
	if err := db.Model(&model.Order{}).Where("order_no = ?", orig.OrderNo).
		Updates(map[string]any{
			"status":              model.OrderStatusPaid,
			"paid_at":             orig.PaidAt,
			"platform_trade_no":   orig.PlatformTradeNo,
			"refund_amount_cents": 0,
			"refunded_at":         nil,
			"refund_reason":       "",
			"refund_no":           "",
			"platform_refund_no":  "",
		}).Error; err != nil {
		log.Fatalf("resetOrder updates: %v", err)
	}
	// 强制把 expired_at 推到未来,避免 stats_daily 残留 + 场景跑完后用同一个 expired_at 触发过期防御
	if orig.ExpiredAt != nil {
		if err := db.Model(&model.Order{}).Where("order_no = ?", orig.OrderNo).
			Update("expired_at", orig.ExpiredAt).Error; err != nil {
			log.Fatalf("resetOrder expired_at: %v", err)
		}
	}
	// 创作者余额 + total_income 还原到 snapshot
	if err := db.Model(&model.Creator{}).Where("id = ?", origCreator.ID).
		Updates(map[string]any{
			"balance_cents":      origCreator.BalanceCents,
			"total_income_cents": origCreator.TotalIncomeCents,
		}).Error; err != nil {
		log.Fatalf("resetCreator: %v", err)
	}
}

// resetStatsDaily 把 creator_stats_daily 表上 (creator,drama,date) 这一行还原到 snapshot 值。
// 不存在就插入一条(income=snapIncome)。这样跑完所有场景后表里不会有测试污染。
func resetStatsDaily(db *gorm.DB, creatorID, dramaID uint64, date string, snapIncome int64) {
	if err := db.Exec(`
		INSERT INTO creator_stats_daily (creator_id, drama_id, stat_date, play_count, income_cents, created_at)
		VALUES (?, ?, ?, 0, ?, NOW())
		ON CONFLICT (creator_id, drama_id, stat_date) DO UPDATE SET income_cents = EXCLUDED.income_cents
	`, creatorID, dramaID, date, snapIncome).Error; err != nil {
		log.Fatalf("resetStatsDaily: %v", err)
	}
}

func fetchStatsDailyIncome(db *gorm.DB, creatorID, dramaID uint64, date string) int64 {
	var row model.CreatorStatsDaily
	err := db.Where("creator_id = ? AND drama_id = ? AND stat_date = ?", creatorID, dramaID, date).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0
	}
	if err != nil {
		log.Fatalf("fetchStatsDaily: %v", err)
	}
	return row.IncomeCents
}

func safeStatus(o *model.Order) string {
	if o == nil {
		return "<nil>"
	}
	return o.Status
}
func safeRefundAmt(o *model.Order) int64 {
	if o == nil {
		return -1
	}
	return o.RefundAmountCents
}
func safeRefundNo(o *model.Order) string {
	if o == nil {
		return "<nil>"
	}
	return o.RefundNo
}

func main() {
	cfg := config.Load()
	db, err := database.Connect(cfg)
	if err != nil {
		log.Fatalf("db.Connect: %v", err)
	}
	reg := payment.NewRegistry(cfg)
	svc := billing.New(db, cfg, reg)

	// === 测前快照 ===
	var origOrder model.Order
	if err := db.Where("order_no = ?", testOrderNo).First(&origOrder).Error; err != nil {
		log.Fatalf("找不到测试订单 %s: %v", testOrderNo, err)
	}
	var drama model.Drama
	if err := db.First(&drama, origOrder.DramaID).Error; err != nil {
		log.Fatalf("找不到 drama %d: %v", origOrder.DramaID, err)
	}
	if drama.CreatorID == nil {
		log.Fatalf("drama %d 没绑定 creator", drama.ID)
	}
	origCreator := fetchCreator(db, *drama.CreatorID)
	today := time.Now().Format("2006-01-02")
	statsDailySnap := fetchStatsDailyIncome(db, *drama.CreatorID, drama.ID, today)
	fmt.Printf("快照:order=%s status=%s amount=%d, creator=%d balance=%d total_income=%d, share=%.2f, stats_daily(%s)=%d\n",
		origOrder.OrderNo, origOrder.Status, origOrder.AmountCents,
		origCreator.ID, origCreator.BalanceCents, origCreator.TotalIncomeCents, cfg.CreatorShareRate, today, statsDailySnap)

	// 保险:测前回滚到干净状态
	resetOrder(db, origOrder, origCreator)
	resetStatsDaily(db, *drama.CreatorID, drama.ID, today, statsDailySnap)
	share := cfg.CreatorShareRate
	clawback := func(amt int64) int64 { return int64(float64(amt) * share) }

	c := &checker{}
	defer func() {
		fmt.Println("\n=== 清理:回滚订单/创作者/当日聚合到测前状态 ===")
		resetOrder(db, origOrder, origCreator)
		resetStatsDaily(db, *drama.CreatorID, drama.ID, today, statsDailySnap)
		final := fetchCreator(db, *drama.CreatorID)
		finalStats := fetchStatsDailyIncome(db, *drama.CreatorID, drama.ID, today)
		fmt.Printf("清理后 creator.balance=%d total_income=%d stats_daily=%d (期望 %d / %d / %d)\n",
			final.BalanceCents, final.TotalIncomeCents, finalStats,
			origCreator.BalanceCents, origCreator.TotalIncomeCents, statsDailySnap)
		fmt.Printf("\n最终结果:%d 个断言失败\n", c.failed)
		if c.failed > 0 {
			os.Exit(1)
		}
	}()

	// === 场景 1:部分退款 500 ===
	c.section("场景 1:部分退款 500 分")
	o1, err := svc.RefundOrder(testOrderNo, "TEST-REF-A", 500, "测试部分退款")
	c.ok("不报错", err == nil, "err=", err)
	c.ok("status=partial_refunded", o1 != nil && o1.Status == model.OrderStatusPartialRefunded, "got", safeStatus(o1))
	c.ok("refund_amount_cents=500", o1 != nil && o1.RefundAmountCents == 500, "got", safeRefundAmt(o1))
	c.ok("refund_no=TEST-REF-A", o1 != nil && o1.RefundNo == "TEST-REF-A", "got", safeRefundNo(o1))
	c.ok("platform_refund_no 非空", o1 != nil && o1.PlatformRefundNo != "")
	c.ok("refunded_at 非 nil", o1 != nil && o1.RefundedAt != nil)
	cAfter1 := fetchCreator(db, *drama.CreatorID)
	exp1 := clawback(500)
	c.ok(fmt.Sprintf("creator.balance -= %d", exp1),
		cAfter1.BalanceCents == origCreator.BalanceCents-exp1,
		"got", cAfter1.BalanceCents, "want", origCreator.BalanceCents-exp1)
	c.ok(fmt.Sprintf("creator.total_income -= %d", exp1),
		cAfter1.TotalIncomeCents == origCreator.TotalIncomeCents-exp1,
		"got", cAfter1.TotalIncomeCents)

	// === 场景 2:同号幂等 ===
	c.section("场景 2:同 refund_no=TEST-REF-A 重入(幂等)")
	o2, err := svc.RefundOrder(testOrderNo, "TEST-REF-A", 500, "重入")
	c.ok("不报错", err == nil, "err=", err)
	c.ok("status 仍 partial_refunded", o2 != nil && o2.Status == model.OrderStatusPartialRefunded)
	c.ok("refund_amount_cents 仍 500", o2 != nil && o2.RefundAmountCents == 500)
	cAfter2 := fetchCreator(db, *drama.CreatorID)
	c.ok("creator 余额未再扣", cAfter2.BalanceCents == cAfter1.BalanceCents,
		"got", cAfter2.BalanceCents, "want", cAfter1.BalanceCents)

	// === 场景 3:超额退款拒绝 ===
	c.section("场景 3:超额退 999 分(剩余可退仅 490)")
	o3, err := svc.RefundOrder(testOrderNo, "TEST-REF-OVER", 999, "超额")
	c.ok("ErrRefundAmountInvalid", errors.Is(err, billing.ErrRefundAmountInvalid), "err=", err)
	c.ok("不返回 order", o3 == nil)
	cAfter3 := fetchCreator(db, *drama.CreatorID)
	c.ok("creator 余额未变", cAfter3.BalanceCents == cAfter2.BalanceCents)

	// === 场景 4:全额退剩余 490 ===
	c.section("场景 4:再退 490 分(累计达 990 全退)")
	o4, err := svc.RefundOrder(testOrderNo, "TEST-REF-B", 490, "退剩余")
	c.ok("不报错", err == nil, "err=", err)
	c.ok("status=refunded", o4 != nil && o4.Status == model.OrderStatusRefunded, "got", safeStatus(o4))
	c.ok("refund_amount_cents=990", o4 != nil && o4.RefundAmountCents == 990)
	c.ok("refund_no=TEST-REF-B(覆盖最近一次)", o4 != nil && o4.RefundNo == "TEST-REF-B")
	cAfter4 := fetchCreator(db, *drama.CreatorID)
	exp2 := clawback(490)
	c.ok(fmt.Sprintf("creator 再 -= %d", exp2),
		cAfter4.BalanceCents == cAfter3.BalanceCents-exp2)
	totalClawback := origCreator.BalanceCents - cAfter4.BalanceCents
	expTotal := clawback(500) + clawback(490)
	c.ok(fmt.Sprintf("累计回扣 %d (=clawback(500)+clawback(490))", expTotal),
		totalClawback == expTotal, "got", totalClawback)

	// === 场景 5:已 refunded 再退拒绝 ===
	c.section("场景 5:refunded 状态再退")
	o5, err := svc.RefundOrder(testOrderNo, "TEST-REF-C", 1, "再退")
	c.ok("ErrRefundNotAllowed", errors.Is(err, billing.ErrRefundNotAllowed), "err=", err)
	c.ok("不返回 order", o5 == nil)

	// === 场景 6:参数校验 ===
	c.section("场景 6:参数校验")
	resetOrder(db, origOrder, origCreator) // 回到 paid 才能测金额/订单号路径
	o6a, err := svc.RefundOrder(testOrderNo, "", 100, "")
	c.ok("空 refund_no → ErrRefundNoRequired", errors.Is(err, billing.ErrRefundNoRequired))
	c.ok("不返回 order", o6a == nil)
	o6b, err := svc.RefundOrder(testOrderNo, "REF-X", 0, "")
	c.ok("amount=0 → ErrRefundAmountInvalid", errors.Is(err, billing.ErrRefundAmountInvalid))
	c.ok("不返回 order", o6b == nil)
	o6c, err := svc.RefundOrder("NOT-EXIST", "REF-X", 100, "")
	c.ok("订单不存在 → ErrOrderNotFound", errors.Is(err, billing.ErrOrderNotFound))
	c.ok("不返回 order", o6c == nil)

	// === 场景 7:SyncOrderStatus 在 paid 单上(no-op)===
	c.section("场景 7:SyncOrderStatus 在已 paid 单上")
	o7, err := svc.SyncOrderStatus(testOrderNo)
	c.ok("不报错", err == nil, "err=", err)
	c.ok("仍是 paid", o7 != nil && o7.Status == model.OrderStatusPaid)

	// === 场景 8:SyncOrderStatus pending→paid ===
	c.section("场景 8:SyncOrderStatus 把 pending 推到 paid(模拟 webhook 丢失)")
	// 同时把 expired_at 推到未来,避免触发 MarkOrderPaid 的"订单过期"防御
	// (实际场景:webhook 丢失但订单还在支付窗口内)。
	expiredFuture := time.Now().Add(time.Hour)
	if err := db.Model(&model.Order{}).Where("order_no = ?", testOrderNo).
		Updates(map[string]any{
			"status":            model.OrderStatusPending,
			"paid_at":           nil,
			"platform_trade_no": "",
			"expired_at":        expiredFuture,
		}).Error; err != nil {
		log.Fatalf("setup pending: %v", err)
	}
	balanceBeforeSync := fetchCreator(db, *drama.CreatorID).BalanceCents
	o8, err := svc.SyncOrderStatus(testOrderNo)
	c.ok("不报错", err == nil, "err=", err)
	c.ok("被推到 paid", o8 != nil && o8.Status == model.OrderStatusPaid, "got", safeStatus(o8))
	c.ok("paid_at 非 nil", o8 != nil && o8.PaidAt != nil)
	cAfter8 := fetchCreator(db, *drama.CreatorID)
	expShare := clawback(990)
	c.ok(fmt.Sprintf("creator 余额增加 %d(分账)", expShare),
		cAfter8.BalanceCents == balanceBeforeSync+expShare,
		"got", cAfter8.BalanceCents, "want", balanceBeforeSync+expShare)

	// === 场景 9:并发退款行锁 ===
	c.section("场景 9:5 个 goroutine 并发退 100 分(总 500<990,应全部成功)")
	resetOrder(db, origOrder, origCreator)
	resetStatsDaily(db, *drama.CreatorID, drama.ID, today, statsDailySnap)
	const concurrentN = 5
	const concurrentAmt int64 = 100
	type concResult struct {
		idx int
		o   *model.Order
		err error
	}
	results := make(chan concResult, concurrentN)
	var wg sync.WaitGroup
	for i := 0; i < concurrentN; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			o, err := svc.RefundOrder(testOrderNo, fmt.Sprintf("TEST-CONC-%d", idx), concurrentAmt, "并发")
			results <- concResult{idx: idx, o: o, err: err}
		}(i)
	}
	wg.Wait()
	close(results)
	successCount := 0
	for r := range results {
		if r.err == nil {
			successCount++
		} else {
			fmt.Printf("  并发 #%d err=%v\n", r.idx, r.err)
		}
	}
	c.ok(fmt.Sprintf("5 个并发全部成功(总 %d<%d)", concurrentN*concurrentAmt, origOrder.AmountCents),
		successCount == concurrentN, "got success=", successCount)
	var oFinal9 model.Order
	db.Where("order_no = ?", testOrderNo).First(&oFinal9)
	c.ok(fmt.Sprintf("refund_amount_cents=%d(累计无遗漏)", concurrentN*concurrentAmt),
		oFinal9.RefundAmountCents == concurrentN*concurrentAmt,
		"got", oFinal9.RefundAmountCents)
	c.ok("status=partial_refunded", oFinal9.Status == model.OrderStatusPartialRefunded, "got", oFinal9.Status)
	cAfter9 := fetchCreator(db, *drama.CreatorID)
	expClawback9 := clawback(concurrentAmt) * concurrentN // 50*5 = 250
	c.ok(fmt.Sprintf("creator 余额扣 %d(并发下不重不漏)", expClawback9),
		cAfter9.BalanceCents == origCreator.BalanceCents-expClawback9,
		"got", cAfter9.BalanceCents, "want", origCreator.BalanceCents-expClawback9)

	// === 场景 10:并发超额混合(5 个并发各退 250,总 1250>990,应部分成功)===
	c.section("场景 10:5 个并发各退 250(总 1250>990,行锁串行化后应 3 成 2 拒)")
	resetOrder(db, origOrder, origCreator)
	resetStatsDaily(db, *drama.CreatorID, drama.ID, today, statsDailySnap)
	const overAmt int64 = 250
	results2 := make(chan concResult, concurrentN)
	var wg2 sync.WaitGroup
	for i := 0; i < concurrentN; i++ {
		wg2.Add(1)
		go func(idx int) {
			defer wg2.Done()
			o, err := svc.RefundOrder(testOrderNo, fmt.Sprintf("TEST-OVER-%d", idx), overAmt, "超额混")
			results2 <- concResult{idx: idx, o: o, err: err}
		}(i)
	}
	wg2.Wait()
	close(results2)
	okN, rejectN := 0, 0
	for r := range results2 {
		if r.err == nil {
			okN++
		} else if errors.Is(r.err, billing.ErrRefundAmountInvalid) {
			rejectN++
		}
	}
	// 最大可成功次数 = floor(990/250) = 3 次 = 750 分;第 4 次会超(750+250=1000>990)
	c.ok(fmt.Sprintf("成功 3 次拒绝 2 次(实际 ok=%d reject=%d)", okN, rejectN),
		okN == 3 && rejectN == 2, "ok=", okN, "reject=", rejectN)
	var oFinal10 model.Order
	db.Where("order_no = ?", testOrderNo).First(&oFinal10)
	c.ok("refund_amount_cents=750", oFinal10.RefundAmountCents == 750, "got", oFinal10.RefundAmountCents)
	c.ok("status=partial_refunded", oFinal10.Status == model.OrderStatusPartialRefunded)

	// === 场景 11:CreatorStatsDaily 当日聚合回退 ===
	c.section("场景 11:CreatorStatsDaily 当日 income_cents 按比例回退")
	resetOrder(db, origOrder, origCreator)
	resetStatsDaily(db, *drama.CreatorID, drama.ID, today, statsDailySnap)
	// 先人为把当日聚合设到 1000,模拟"今天已经分账过 1000",再退 200 分(回退 100)
	resetStatsDaily(db, *drama.CreatorID, drama.ID, today, 1000)
	_, err = svc.RefundOrder(testOrderNo, "TEST-STATS", 200, "聚合测试")
	c.ok("退款不报错", err == nil, "err=", err)
	statsAfter := fetchStatsDailyIncome(db, *drama.CreatorID, drama.ID, today)
	expStatsClawback := clawback(200)
	c.ok(fmt.Sprintf("stats_daily.income_cents = 1000 - %d = %d", expStatsClawback, 1000-expStatsClawback),
		statsAfter == 1000-expStatsClawback, "got", statsAfter, "want", 1000-expStatsClawback)

	// === 场景 12:GREATEST 防负兜底 ===
	c.section("场景 12:stats_daily 当日只有 50 分,退 200(回退 100),应被 GREATEST 兜底为 0")
	resetOrder(db, origOrder, origCreator)
	resetStatsDaily(db, *drama.CreatorID, drama.ID, today, 50) // 故意调到不够扣
	_, err = svc.RefundOrder(testOrderNo, "TEST-CLAMP", 200, "兜底测试")
	c.ok("退款不报错(不会因为聚合不够而失败)", err == nil, "err=", err)
	statsAfter12 := fetchStatsDailyIncome(db, *drama.CreatorID, drama.ID, today)
	c.ok("stats_daily.income_cents 被 GREATEST 钳到 0", statsAfter12 == 0, "got", statsAfter12)

	// === 场景 13:无 creator 的订单(drama.CreatorID = NULL)===
	c.section("场景 13:无 creator 的订单退款(不应报错)")
	resetOrder(db, origOrder, origCreator)
	resetStatsDaily(db, *drama.CreatorID, drama.ID, today, statsDailySnap)
	// 临时把 drama 的 creator_id 改成 NULL
	origCreatorID := drama.CreatorID
	if err := db.Model(&model.Drama{}).Where("id = ?", drama.ID).Update("creator_id", nil).Error; err != nil {
		log.Fatalf("setup no-creator: %v", err)
	}
	defer func() {
		// 测完一定要把 creator_id 改回来,否则下次跑测试快照丢失
		db.Model(&model.Drama{}).Where("id = ?", drama.ID).Update("creator_id", origCreatorID)
	}()
	o13, err := svc.RefundOrder(testOrderNo, "TEST-NOCRTR", 300, "无创作者")
	c.ok("退款不报错(无创作者分支)", err == nil, "err=", err)
	c.ok("status=partial_refunded", o13 != nil && o13.Status == model.OrderStatusPartialRefunded)
	c.ok("refund_amount_cents=300", o13 != nil && o13.RefundAmountCents == 300, "got", safeRefundAmt(o13))
	// 把 creator_id 改回来,后面验证 creator 余额(在无 creator 分支下不应被扣)
	db.Model(&model.Drama{}).Where("id = ?", drama.ID).Update("creator_id", origCreatorID)
	cAfter13 := fetchCreator(db, *drama.CreatorID)
	c.ok("creator 余额未被扣(无 creator 分支早退)",
		cAfter13.BalanceCents == origCreator.BalanceCents,
		"got", cAfter13.BalanceCents, "want", origCreator.BalanceCents)

	// === 场景 14:单笔等额全退(边界,990→990)===
	c.section("场景 14:单笔退 990 一次性全退(边界)")
	resetOrder(db, origOrder, origCreator)
	resetStatsDaily(db, *drama.CreatorID, drama.ID, today, statsDailySnap)
	o14, err := svc.RefundOrder(testOrderNo, "TEST-FULL", 990, "一次全退")
	c.ok("不报错", err == nil, "err=", err)
	c.ok("status=refunded(不是 partial)", o14 != nil && o14.Status == model.OrderStatusRefunded, "got", safeStatus(o14))
	c.ok("refund_amount_cents=990", o14 != nil && o14.RefundAmountCents == 990)
	cAfter14 := fetchCreator(db, *drama.CreatorID)
	c.ok("creator 余额扣 495(990*0.5)",
		cAfter14.BalanceCents == origCreator.BalanceCents-clawback(990),
		"got", cAfter14.BalanceCents, "want", origCreator.BalanceCents-clawback(990))
}
