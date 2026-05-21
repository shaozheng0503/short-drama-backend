package redisclient

import (
	"context"
	"log"
	"strings"
	"time"

	"ai-drama-platform/internal/config"

	"github.com/redis/go-redis/v9"
)

func New(cfg config.Config) *redis.Client {
	if strings.TrimSpace(cfg.RedisAddr) == "" {
		log.Printf("[redis] REDIS_ADDR 未配置，Redis 相关能力将停用")
		return nil
	}
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		log.Printf("[redis] ping failed addr=%s err=%v，Redis 相关能力将停用", cfg.RedisAddr, err)
		_ = client.Close()
		return nil
	}
	log.Printf("[redis] connected addr=%s db=%d", cfg.RedisAddr, cfg.RedisDB)
	return client
}
