// 一次性脚本：在远端跑，给 admins 表 INSERT 一个"郎志"账号
// 用法：
//
//	cd /opt/drama-backend-sandbox && sudo -u ai_drama /opt/drama-backend-sandbox/drama-api create-admin  # 不可行，要单独的 cmd
//
// 实际：把这段代码包成一个独立 Go 程序，编译后用 sandbox 环境的 PG 直连 INSERT。
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const langzhiHash = "$2b$10$4LbVOSRbvmMrWg8S10HzHOWc7gsLrkvQtAb10MOGKorf4cE0cdB42" // langzhi@2026

type Admin struct {
	ID           uint64 `gorm:"primaryKey;column:id"`
	Username     string `gorm:"column:username;size:64;uniqueIndex"`
	PasswordHash string `gorm:"column:password_hash;size:255"`
	Role         string `gorm:"column:role;size:32;default:admin"`
	Status       string `gorm:"column:status;size:20;default:active"`
}

func (Admin) TableName() string { return "admins" }

func main() {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		log.Fatal("DATABASE_DSN env required")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatalf("db open: %v", err)
	}
	username := "郎志"
	// 双重保险：用 Go bcrypt 重新算一遍本地 hash（不依赖外部 hash 字面量）
	hash, err := bcrypt.GenerateFromPassword([]byte("langzhi@2026"), 10)
	if err != nil {
		log.Fatalf("bcrypt: %v", err)
	}
	// 幂等：先查
	var exist Admin
	tx := db.WithContext(context.Background()).Where("username = ?", username).First(&exist)
	if tx.Error == nil {
		fmt.Printf("admin %q already exists id=%d role=%s status=%s — skip\n", username, exist.ID, exist.Role, exist.Status)
		os.Exit(0)
	}
	row := Admin{
		Username:     username,
		PasswordHash: string(hash),
		Role:         "admin",
		Status:       "active",
	}
	if err := db.Create(&row).Error; err != nil {
		log.Fatalf("insert: %v", err)
	}
	fmt.Printf("OK seeded admin id=%d username=%q role=admin password=langzhi@2026\n", row.ID, row.Username)
	fmt.Printf("  (precomputed hash for record: %s)\n", langzhiHash)
}
