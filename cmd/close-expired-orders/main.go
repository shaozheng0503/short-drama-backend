package main

import (
	"fmt"
	"log"
	"time"

	"ai-drama-platform/internal/alert"
	"ai-drama-platform/internal/billing"
	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/payment"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func main() {
	cfg := config.Load()
	db, err := gorm.Open(postgres.Open(cfg.DSN), &gorm.Config{})
	if err != nil {
		log.Fatalf("connect database failed: %v", err)
	}

	service := billing.New(db, cfg, payment.NewRegistry(cfg))
	result, err := service.CloseExpiredOrders(time.Now())
	if err != nil {
		alert.New(cfg).SendAsync(alert.Event{
			Level:   "error",
			Type:    "close_expired_orders_failed",
			Message: "关闭过期订单失败",
			Fields:  map[string]interface{}{"error": err.Error()},
		})
		log.Fatalf("close expired orders failed: %v", err)
	}
	if result.ClosedCount > 0 {
		alert.New(cfg).SendAsync(alert.Event{
			Level:   "warn",
			Type:    "expired_orders_closed",
			Message: "过期订单已关闭",
			Fields: map[string]interface{}{
				"closed_count":      result.ClosedCount,
				"oldest_expired_at": result.OldestExpiredAt,
				"sample_order_nos":  result.SampleOrderNos,
			},
		})
		time.Sleep(200 * time.Millisecond)
	}

	fmt.Printf("closed_expired_orders: %d\n", result.ClosedCount)
	if result.OldestExpiredAt != nil {
		fmt.Printf("oldest_expired_at: %s\n", result.OldestExpiredAt.Format(time.RFC3339))
	}
	if len(result.SampleOrderNos) > 0 {
		fmt.Printf("sample_order_nos: %v\n", result.SampleOrderNos)
	}
}
