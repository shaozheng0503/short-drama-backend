package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Addr       string
	DSN        string
	JWTSecret  string
	JWTExpires time.Duration
}

func Load() Config {
	return Config{
		Addr:       getEnv("APP_ADDR", ":8080"),
		DSN:        getEnv("DATABASE_DSN", "host=localhost user=postgres password=postgres dbname=ai_drama port=5432 sslmode=disable TimeZone=Asia/Shanghai"),
		JWTSecret:  getEnv("JWT_SECRET", "dev-secret-change-me"),
		JWTExpires: time.Duration(getEnvInt("JWT_EXPIRES_HOURS", 168)) * time.Hour,
	}
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}
