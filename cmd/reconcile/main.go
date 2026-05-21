package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/reconcile"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func main() {
	cfg := config.Load()
	db, err := gorm.Open(postgres.Open(cfg.DSN), &gorm.Config{})
	if err != nil {
		log.Fatalf("connect database failed: %v", err)
	}

	report, err := reconcile.Run(db, cfg, time.Now())
	if err != nil {
		log.Fatalf("reconcile failed: %v", err)
	}

	printReport(report)
	if report.HasErrors() {
		os.Exit(1)
	}
}

func printReport(report reconcile.Report) {
	fmt.Printf("DramaBackend 账务对账结果\n")
	fmt.Printf("checked_at: %s\n", report.CheckedAt.Format(time.RFC3339))
	fmt.Printf("paid_orders: %d\n", report.PaidOrderCount)
	fmt.Printf("creators: %d\n", report.CreatorCount)
	fmt.Printf("withdrawals: %d\n", report.WithdrawalCount)
	fmt.Printf("creator_stats_daily_rows: %d\n", report.StatsRowCount)
	fmt.Printf("missing_unlocks: %d\n", report.MissingUnlockCount)
	fmt.Printf("issues: %d\n", len(report.Issues))

	if len(report.Issues) == 0 {
		fmt.Println("status: OK")
		return
	}

	fmt.Println("status: FAILED")
	for _, issue := range report.Issues {
		fmt.Printf("[%s] %s: %s\n", issue.Severity, issue.Code, issue.Message)
	}
}
