package database

import (
	"errors"
	"log"

	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/seed"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func Connect(cfg config.Config) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(cfg.DSN), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(
		&model.User{},
		&model.SMSCode{},
		&model.Admin{},
		&model.Creator{},
		&model.CreatorChannelAccount{},
		&model.Category{},
		&model.Language{},
		&model.DramaTag{},
		&model.Drama{},
		&model.DramaCover{},
		&model.DramaCharacter{},
		&model.Episode{},
		&model.PlayHistory{},
		&model.UserAction{},
		&model.Comment{},
		&model.Product{},
		&model.Order{},
		&model.EpisodeUnlock{},
		&model.Contract{},
		&model.Withdrawal{},
		&model.TaxBracket{},
		&model.CreatorStatsDaily{},
		&model.ChannelIncomeDaily{},
		&model.ChannelIncomeImportBatch{},
		&model.OperationLog{},
		&model.Notification{},
		&model.GlobalConfig{},
	); err != nil {
		return nil, err
	}
	if err := ensureInitialAdmin(db, cfg); err != nil {
		return nil, err
	}
	if err := seed.EnsureThemeCategories(db); err != nil {
		return nil, err
	}
	if err := ensureDefaultProduct(db); err != nil {
		return nil, err
	}
	if err := ensureIndexes(db); err != nil {
		return nil, err
	}
	if err := migrateMergeDialectIntoLanguage(db); err != nil {
		return nil, err
	}
	if cfg.SeedMockData {
		result, err := seed.Run(db, cfg)
		if err != nil {
			return nil, err
		}
		log.Printf("[seed] auto-seed on startup: %+v", result)
	}
	return db, nil
}

func ensureIndexes(db *gorm.DB) error {
	if err := db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_user_episode_pending
		ON orders (user_id, episode_id)
		WHERE status = 'pending'
	`).Error; err != nil {
		return err
	}
	// 观看历史「一剧一条」迁移：删除旧的 (user,episode) 唯一索引，对存量去重（每个 user+drama 仅留最近一条），
	// 再建 (user,drama) 唯一索引。幂等，重复执行安全。
	if err := db.Exec(`DROP INDEX IF EXISTS uniq_user_episode`).Error; err != nil {
		return err
	}
	if err := db.Exec(`
		DELETE FROM play_histories ph
		USING play_histories keep
		WHERE ph.user_id = keep.user_id
		  AND ph.drama_id = keep.drama_id
		  AND (
		    ph.updated_at < keep.updated_at
		    OR (ph.updated_at = keep.updated_at AND ph.id < keep.id)
		  )
	`).Error; err != nil {
		return err
	}
	if err := db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS uniq_play_user_drama
		ON play_histories (user_id, drama_id)
	`).Error; err != nil {
		return err
	}
	// 点赞下沉到集级：删除旧 (user,drama,action) 唯一索引——它会挡住同剧多集点赞。
	// 新唯一键 (user,drama,episode,action) 由 AutoMigrate 依 model tag 建（uniq_user_drama_episode_action）。
	if err := db.Exec(`DROP INDEX IF EXISTS uniq_user_drama_action`).Error; err != nil {
		return err
	}
	return nil
}

// migrateMergeDialectIntoLanguage 将 dramas.dialect_id 合并进 language_id 后删除 dialect_id 列。
func migrateMergeDialectIntoLanguage(db *gorm.DB) error {
	if !db.Migrator().HasColumn(&model.Drama{}, "dialect_id") {
		return nil
	}
	if err := db.Exec(`
		UPDATE dramas
		SET language_id = COALESCE(dialect_id, language_id)
		WHERE dialect_id IS NOT NULL
	`).Error; err != nil {
		return err
	}
	return db.Migrator().DropColumn(&model.Drama{}, "dialect_id")
}

func ensureInitialAdmin(db *gorm.DB, cfg config.Config) error {
	if cfg.AdminInitUsername == "" || cfg.AdminInitPassword == "" {
		return nil
	}
	var existing model.Admin
	err := db.Where("username = ?", cfg.AdminInitUsername).First(&existing).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminInitPassword), cfg.BcryptCost)
	if err != nil {
		return err
	}
	admin := model.Admin{
		Username:     cfg.AdminInitUsername,
		PasswordHash: string(hash),
		Role:         model.AdminRoleAdmin,
		Status:       model.StatusActive,
	}
	if err := db.Create(&admin).Error; err != nil {
		return err
	}
	log.Printf("seeded initial admin: username=%s", cfg.AdminInitUsername)
	return nil
}

func ensureDefaultProduct(db *gorm.DB) error {
	var cnt int64
	if err := db.Model(&model.Product{}).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt > 0 {
		return nil
	}
	defaultProduct := model.Product{
		Name:       "单集解锁",
		Type:       model.ProductTypeEpisodeUnlock,
		PriceCents: 0, // 实际价格以 dramas.price_cents 为准；本商品仅为占位
		Status:     model.StatusActive,
	}
	if err := db.Create(&defaultProduct).Error; err != nil {
		return err
	}
	log.Printf("seeded default product: id=%d", defaultProduct.ID)
	return nil
}
