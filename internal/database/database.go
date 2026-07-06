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
	"gorm.io/gorm/logger"
)

func Connect(cfg config.Config) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(cfg.DSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Error), // 只记 error，避免日志爆炸
	})
	if err != nil {
		return nil, err
	}
	// 连接池：显式封顶，防止高并发把 Postgres max_connections 打满导致全站不可用。
	if sqlDB, derr := db.DB(); derr == nil {
		sqlDB.SetMaxOpenConns(cfg.DBMaxOpenConns)
		sqlDB.SetMaxIdleConns(cfg.DBMaxIdleConns)
		sqlDB.SetConnMaxLifetime(cfg.DBConnMaxLifetime)
		sqlDB.SetConnMaxIdleTime(cfg.DBConnMaxIdleTime)
		log.Printf("[db] pool: max_open=%d max_idle=%d max_lifetime=%s max_idle_time=%s",
			cfg.DBMaxOpenConns, cfg.DBMaxIdleConns, cfg.DBConnMaxLifetime, cfg.DBConnMaxIdleTime)
	} else {
		log.Printf("[db] 获取底层 sql.DB 失败，连接池用默认值: %v", derr)
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
		&model.CommentLike{},
		&model.AppMessage{},
		&model.Product{},
		&model.Order{},
		&model.EpisodeUnlock{},
		&model.Contract{},
		&model.Withdrawal{},
		&model.TaxBracket{},
		&model.CreatorStatsDaily{},
		&model.Settlement{},
		&model.SettlementItem{},
		&model.Invoice{},
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
	if err := ensureRoleAdmins(db, cfg); err != nil {
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
	if err := migrateAddSettlementHalfMonthFields(db); err != nil {
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
	// 单集 pending 单去重索引。选集购买的批量单 episode_id=0，同用户多张批量单会在
	// (user_id, episode_id) 上互撞，故条件加 episode_id <> 0，只约束单集单；批量单去重靠
	// advisory lock(user+drama) + Idempotency-Key。下面 DO 块仅在旧条件索引上做一次性重建，
	// reload 不重复 drop（避免零停机窗口里短暂丢失唯一约束）。
	if err := db.Exec(`
		DO $$
		BEGIN
		  IF EXISTS (
		    SELECT 1 FROM pg_indexes
		    WHERE indexname = 'idx_orders_user_episode_pending'
		      AND indexdef NOT LIKE '%episode_id <> 0%'
		  ) THEN
		    DROP INDEX idx_orders_user_episode_pending;
		  END IF;
		END $$;
	`).Error; err != nil {
		return err
	}
	if err := db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_user_episode_pending
		ON orders (user_id, episode_id)
		WHERE status = 'pending' AND episode_id <> 0
	`).Error; err != nil {
		return err
	}
	// 分批审核迁移：新加的资料/视频维度列对存量初始为空，按当前 audit_status 回填一次，
	// 保证已通过的存量剧在"派生总状态"下仍为 approved（不被误判回退）。仅填空值行，幂等。
	if err := db.Exec(`UPDATE dramas SET content_audit_status = audit_status WHERE content_audit_status = '' AND audit_status <> ''`).Error; err != nil {
		return err
	}
	if err := db.Exec(`UPDATE dramas SET video_audit_status = audit_status WHERE video_audit_status = '' AND audit_status <> ''`).Error; err != nil {
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

	// === 性能索引（2026-06-20 压测，20k 剧 / 30w 订单实测）===
	// APP 列表/剧场默认按 published_at、热度按 play_count 排序，且都先过 status=published。
	// 复合索引让查询走索引序、免对全表做 top-N Sort —— 实测列表 6.5ms→0.13ms（约 50×）。
	if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_dramas_status_published_at ON dramas (status, published_at DESC)`).Error; err != nil {
		return err
	}
	if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_dramas_status_play_count ON dramas (status, play_count DESC)`).Error; err != nil {
		return err
	}
	// 中台「每部剧 App 付费收入」/ 看板营收按 paid_at 范围聚合；部分索引只收已支付单，
	// 范围扫描免全表 Seq Scan —— 实测带日期聚合 141ms→59ms。
	if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_orders_paid_at ON orders (paid_at) WHERE paid_at IS NOT NULL`).Error; err != nil {
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

// migrateAddSettlementHalfMonthFields 加 settlements.cycle_key + period_range 字段。
// 2026-07-03 群（吴建棉）：提现改为半月度，每月 15 号 + 月末各结算一次。
//
// 旧月度数据 cycle_key/period_range 为空，新半月度数据会填
//   - cycle_key:  "2026-07-H1" / "2026-07-H2"（唯一键）
//   - period_range: "2026-07-01 ~ 2026-07-15" / "2026-07-16 ~ 2026-07-31"
//
// 幂等：HasColumn 已存在则跳过；同时对存量月度行回填 period_range（让老数据展示更友好）。
func migrateAddSettlementHalfMonthFields(db *gorm.DB) error {
	if !db.Migrator().HasColumn(&model.Settlement{}, "cycle_key") {
		if err := db.Exec(`ALTER TABLE settlements ADD COLUMN cycle_key VARCHAR(16)`).Error; err != nil {
			return err
		}
		if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_settlements_cycle_key ON settlements (cycle_key)`).Error; err != nil {
			return err
		}
	}
	if !db.Migrator().HasColumn(&model.Settlement{}, "period_range") {
		if err := db.Exec(`ALTER TABLE settlements ADD COLUMN period_range VARCHAR(64)`).Error; err != nil {
			return err
		}
	}
	// 存量月度行 period_range 回填："2026-05" → "2026-05-01 ~ 2026-05-31"
	// 用 (period || '-01' ~ period 月末) 形式补；幂等（重复执行因值相同无副作用）。
	if err := db.Exec(`
		UPDATE settlements
		SET period_range = period || '-01' || ' ~ ' || to_char(
			(date_trunc('month', to_date(period, 'YYYY-MM')) + interval '1 month - 1 day')::date,
			'YYYY-MM-DD'
		)
		WHERE period_range IS NULL OR period_range = ''
		  AND period ~ '^\d{4}-\d{2}$'
	`).Error; err != nil {
		return err
	}
	return nil
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

// ensureRoleAdmins 按需补齐分角色管理端账号：finance（财务）与 auditor（审核）。
// 密码沿用 ADMIN_INIT_PASSWORD —— 与超管 admin 同源（"和 admin 一样"）。
// 幂等：按 username 判断，已存在则跳过、绝不覆盖（避免把别人改过的密码冲掉）。
func ensureRoleAdmins(db *gorm.DB, cfg config.Config) error {
	if cfg.AdminInitPassword == "" {
		return nil
	}
	roleAccounts := []struct {
		username string
		role     string
	}{
		{"finance", model.AdminRoleFinance},
		{"auditor", model.AdminRoleAuditor},
	}
	for _, ra := range roleAccounts {
		var existing model.Admin
		err := db.Where("username = ?", ra.username).First(&existing).Error
		if err == nil {
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminInitPassword), cfg.BcryptCost)
		if err != nil {
			return err
		}
		admin := model.Admin{
			Username:     ra.username,
			PasswordHash: string(hash),
			Role:         ra.role,
			Status:       model.StatusActive,
		}
		if err := db.Create(&admin).Error; err != nil {
			return err
		}
		log.Printf("seeded role admin: username=%s role=%s", ra.username, ra.role)
	}
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
