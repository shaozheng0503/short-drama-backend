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
		// 0.15.0 发行商模块
		&model.Distributor{},
		&model.DistributorRecharge{},
		&model.DistributorApplication{},
		&model.DistributorDrama{},
		&model.DistributorContract{},
		&model.DistributorIncomeDaily{},
		&model.DistributorSettlement{},
		&model.DistributorWithdrawal{},
		&model.DistributorInvoice{},
		&model.DistributorDepositTransaction{},
		&model.DistributorAbandonRequest{},
		&model.AdminPermission{},
		// 穿山甲激励视频 —— 看广告解锁
		&model.AdUnlockTicket{},
	); err != nil {
		return nil, err
	}
	if err := ensureInitialAdmin(db, cfg); err != nil {
		return nil, err
	}
	if err := ensureRoleAdmins(db, cfg); err != nil {
		return nil, err
	}
	if err := ensureAdminPermissions(db); err != nil {
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
	if err := migrateCreateStateTransitions(db); err != nil {
		return nil, err
	}
	if err := migrateClaimStatusEnums(db); err != nil {
		return nil, err
	}
	if err := migrateDistributorSettlementUniqueCycle(db); err != nil {
		return nil, err
	}
	if err := migrateBackfillDepositTxDramaID(db); err != nil {
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
	// 分批审核迁移：新加的资料/视频维度列对存量初始为空（'' 或 NULL，旧行经 gorm 零值写入可能是 NULL），
	// 按当前 audit_status 回填一次，保证已通过的存量剧在"派生总状态"下仍为 approved（不被误判回退）。
	// 仅填空值行，幂等。NULL 会被后端 norm() 当作 pending 处理，故必须一并回填。
	if err := db.Exec(`UPDATE dramas SET content_audit_status = audit_status WHERE (content_audit_status IS NULL OR content_audit_status = '') AND audit_status <> ''`).Error; err != nil {
		return err
	}
	if err := db.Exec(`UPDATE dramas SET video_audit_status = audit_status WHERE (video_audit_status IS NULL OR video_audit_status = '') AND audit_status <> ''`).Error; err != nil {
		return err
	}
	// 兜底：audit_status 也为空的极端行，两维度统一置 pending（与模型 default 一致）。
	if err := db.Exec(`UPDATE dramas SET content_audit_status = 'pending' WHERE content_audit_status IS NULL OR content_audit_status = ''`).Error; err != nil {
		return err
	}
	if err := db.Exec(`UPDATE dramas SET video_audit_status = 'pending' WHERE video_audit_status IS NULL OR video_audit_status = ''`).Error; err != nil {
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

	// === 押金导入行级幂等（2026-08-29）===
	// 收益导入「押金」类目行的业务号格式为 IMPDEP:<fileHash>:<rowNo>；唯一部分索引只约束
	// related_type='import' 的 recharge 流水（freeze/deduct/manual 等其他 related_business_no
	// 语义不同，不受约束）。并发双击导入时，即使事务内 SELECT 幂等检查同桶竞争，Create 也会被
	// 数据库唯一约束兜底拒绝，杜绝同一文件同一行重复入账。
	// 存量数据兼容：若曾用旧版本（随机 batchNo 业务号）重复导入过押金，先探活再降级告警，
	// 避免唯一索引创建失败阻断服务启动。
	var importDupCount int64
	db.Raw(`SELECT COUNT(*) FROM (
		SELECT related_business_no FROM distributor_deposit_transactions
		WHERE related_type = 'import'
		GROUP BY related_business_no HAVING COUNT(*) > 1
	) dup`).Scan(&importDupCount)
	if importDupCount > 0 {
		log.Printf("[db] 警告：发现 %d 组历史重复押金导入流水（related_business_no 重复），行级幂等唯一索引已跳过创建，请人工核对后重建", importDupCount)
	} else if err := db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS uniq_deposit_tx_import_biz
		ON distributor_deposit_transactions (related_type, related_business_no)
		WHERE related_type = 'import'
	`).Error; err != nil {
		return err
	}

	// === 点赞聚合消息唯一性（2026-08-29）===
	// comment_like 按 (recipient_id, comment_id) 聚合成一条，靠本部分唯一索引约束；
	// comment_reply 是一回复一条，绝不能进唯一约束，故索引只收 type='comment_like'。
	// 存量兼容：历史并发已插出重复聚合消息时先探活，降级告警跳过，避免启动被阻断。
	var likeMsgDupCount int64
	db.Raw(`SELECT COUNT(*) FROM (
		SELECT recipient_id, comment_id FROM app_messages
		WHERE type = 'comment_like'
		GROUP BY recipient_id, comment_id HAVING COUNT(*) > 1
	) dup`).Scan(&likeMsgDupCount)
	if likeMsgDupCount > 0 {
		log.Printf("[db] 警告：发现 %d 组历史重复点赞聚合消息（app_messages），唯一索引已跳过创建，请人工去重后重建", likeMsgDupCount)
	} else if err := db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS uniq_app_msg_like_recipient_comment
		ON app_messages (recipient_id, comment_id)
		WHERE type = 'comment_like'
	`).Error; err != nil {
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

	// === 模糊搜索 GIN 索引（pg_trgm）===
	// admin 列表搜索 / APP 搜索 / 发行商广场搜索均用 ILIKE '%keyword%'，
	// 无 trgm 索引时走全表 Seq Scan，20k+ 行时延迟显著。pg_trgm 扩展 + gin_trgm_ops
	// 让 ILIKE 走位图索引扫描，实测 20k 行 title ILIKE '%X%' 从 45ms 降到 0.3ms。
	// 幂等：CREATE EXTENSION IF NOT EXISTS + CREATE INDEX IF NOT EXISTS。
	if err := db.Exec(`CREATE EXTENSION IF NOT EXISTS pg_trgm`).Error; err != nil {
		// pg_trgm 需要超级用户权限；云数据库可能需要用户手动在控制台开启。
		// 扩展创建失败不阻断启动，索引也会跳过，只是搜索走 Seq Scan（功能不受影响）。
		log.Printf("[db] pg_trgm 扩展创建失败（搜索将走全表扫描）: %v", err)
	} else {
		// dramas.title / dramas.description — admin 剧集搜索 + APP 搜索
		if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_dramas_title_trgm ON dramas USING gin (title gin_trgm_ops)`).Error; err != nil {
			log.Printf("[db] idx_dramas_title_trgm 创建失败: %v", err)
		}
		if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_dramas_description_trgm ON dramas USING gin (description gin_trgm_ops)`).Error; err != nil {
			log.Printf("[db] idx_dramas_description_trgm 创建失败: %v", err)
		}
		// distributors.name / phone / org_name — admin 发行商搜索
		if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_distributors_name_trgm ON distributors USING gin (name gin_trgm_ops)`).Error; err != nil {
			log.Printf("[db] idx_distributors_name_trgm 创建失败: %v", err)
		}
		if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_distributors_phone_trgm ON distributors USING gin (phone gin_trgm_ops)`).Error; err != nil {
			log.Printf("[db] idx_distributors_phone_trgm 创建失败: %v", err)
		}
		if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_distributors_org_name_trgm ON distributors USING gin (org_name gin_trgm_ops)`).Error; err != nil {
			log.Printf("[db] idx_distributors_org_name_trgm 创建失败: %v", err)
		}
		// creators.name / phone — admin 创作者搜索（admin_finance.go 用 name ILIKE OR phone ILIKE）
		if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_creators_name_trgm ON creators USING gin (name gin_trgm_ops)`).Error; err != nil {
			log.Printf("[db] idx_creators_name_trgm 创建失败: %v", err)
		}
		if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_creators_phone_trgm ON creators USING gin (phone gin_trgm_ops)`).Error; err != nil {
			log.Printf("[db] idx_creators_phone_trgm 创建失败: %v", err)
		}
		// orders.order_no / orders.platform_trade_no — admin 订单搜索（LIKE）
		if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_orders_order_no_trgm ON orders USING gin (order_no gin_trgm_ops)`).Error; err != nil {
			log.Printf("[db] idx_orders_order_no_trgm 创建失败: %v", err)
		}
		if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_orders_platform_trade_no_trgm ON orders USING gin (platform_trade_no gin_trgm_ops)`).Error; err != nil {
			log.Printf("[db] idx_orders_platform_trade_no_trgm 创建失败: %v", err)
		}
		log.Printf("[db] pg_trgm GIN 索引已创建（dramas/distributors/creators/orders 模糊搜索加速）")
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

// migrateCreateStateTransitions 创建状态变迁事件表。
// 2026-07-06 P1-5「时间线按天回看」功能支持表。
//
// 表设计要点：
//   - (entity_type, entity_id) 联合索引——回看时按实体查最近一条变迁
//   - created_at 索引——按时间范围过滤
//   - 不存现状——每次状态变化追加一行
func migrateCreateStateTransitions(db *gorm.DB) error {
	if db.Migrator().HasTable(&model.StateTransition{}) {
		return nil
	}
	if err := db.Exec(`
		CREATE TABLE IF NOT EXISTS state_transitions (
			id          BIGSERIAL PRIMARY KEY,
			entity_type VARCHAR(32)  NOT NULL,
			entity_id   BIGINT       NOT NULL,
			from_status VARCHAR(20)  NOT NULL,
			to_status   VARCHAR(20)  NOT NULL,
			actor_type  VARCHAR(16)  NOT NULL,
			actor_id    BIGINT,
			reason      VARCHAR(255),
			metadata    JSONB,
			created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
		)
	`).Error; err != nil {
		return err
	}
	if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_state_transitions_entity ON state_transitions (entity_type, entity_id, created_at)`).Error; err != nil {
		return err
	}
	if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_state_transitions_created_at ON state_transitions (created_at)`).Error; err != nil {
		return err
	}
	return nil
}

// migrateClaimStatusEnums 统一认领流程状态枚举（2026-07-24）。
// 1. status 列扩容 VARCHAR(20) → VARCHAR(32)（authorization_pending 21 字符超限）
// 2. auth_pending → authorization_pending（Issue 5：命名统一）
// 3. 驳回认领 deposit_status: paid → released（Issue 2：驳回后押金状态）
// 4. 未进入合同阶段 contract_status: pending → none（Issue 4：避免误导）
// 幂等：每条 UPDATE 都带 WHERE 条件，重复执行安全。
func migrateClaimStatusEnums(db *gorm.DB) error {
	// 0. 扩容 status 列（authorization_pending = 21 字符 > VARCHAR(20)）
	if err := db.Exec(`ALTER TABLE distributor_applications ALTER COLUMN status TYPE VARCHAR(32)`).Error; err != nil {
		return err
	}
	// 1. auth_pending → authorization_pending
	if err := db.Exec(`UPDATE distributor_applications SET status = 'authorization_pending' WHERE status = 'auth_pending'`).Error; err != nil {
		return err
	}
	// 2. 驳回认领：deposit_status paid → released
	if err := db.Exec(`UPDATE distributor_applications SET deposit_status = 'released' WHERE status = 'rejected' AND deposit_status = 'paid'`).Error; err != nil {
		return err
	}
	// 3. 未进入合同阶段：contract_status pending → none
	//    仅对 deposit_pending / authorization_pending / review_pending / rejected 状态的认领
	if err := db.Exec(`UPDATE distributor_applications SET contract_status = 'none' WHERE status IN ('deposit_pending', 'authorization_pending', 'review_pending', 'rejected') AND contract_status = 'pending'`).Error; err != nil {
		return err
	}
	return nil
}

// migrateDistributorSettlementUniqueCycle 为发行商结算单创建部分唯一索引。
// 仅 cycle_key 非空时 (distributor_id, cycle_key) 唯一，防止并发重复生成结算单。
// 旧数据 cycle_key='' 不受约束（兼容历史月度数据）。
func migrateDistributorSettlementUniqueCycle(db *gorm.DB) error {
	return db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uniq_dist_settlement_cycle
ON distributor_settlements (distributor_id, cycle_key)
WHERE cycle_key <> ''`).Error
}

// migrateBackfillDepositTxDramaID 回填 distributor_deposit_transactions.drama_id。
// 新增 drama_id 列后，已有的 freeze/unfreeze 记录 drama_id=0，需通过
// related_business_no 关联 distributor_applications.application_no 回填对应剧集 ID。
// 充值（type=recharge）记录 drama_id 保持 0（充值不关联具体剧集）。
func migrateBackfillDepositTxDramaID(db *gorm.DB) error {
	result := db.Exec(`UPDATE distributor_deposit_transactions t
SET drama_id = sub.drama_id
FROM (
	SELECT da.drama_id, da.application_no
	FROM distributor_applications da
	WHERE da.application_no IS NOT NULL AND da.application_no <> ''
) sub
WHERE t.related_business_no = sub.application_no
  AND t.drama_id = 0
  AND t.type IN ('freeze', 'unfreeze')`)
	if result.Error != nil {
		log.Printf("[migrate] backfill deposit_tx drama_id failed: %v", result.Error)
		return result.Error
	}
	if result.RowsAffected > 0 {
		log.Printf("[migrate] backfilled drama_id for %d deposit transactions", result.RowsAffected)
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

// ensureAdminPermissions 为现有 3 个种子账号补权限项（幂等）。
// admin → super_admin；finance → finance；auditor → creator_audit + content_audit + distributor_audit + claim_audit。
// 已拥有对应权限则跳过，不重复写入。
func ensureAdminPermissions(db *gorm.DB) error {
	permMap := map[string][]string{
		"admin":   {model.PermSuperAdmin},
		"finance": {model.PermFinance},
		"auditor": {model.PermCreatorAudit, model.PermContentAudit, model.PermDistributorAudit, model.PermClaimAudit},
	}
	for username, perms := range permMap {
		var admin model.Admin
		err := db.Where("username = ?", username).First(&admin).Error
		if err != nil {
			continue // 账号不存在则跳过
		}
		for _, perm := range perms {
			var count int64
			db.Model(&model.AdminPermission{}).
				Where("admin_id = ? AND permission = ?", admin.ID, perm).
				Count(&count)
			if count > 0 {
				continue
			}
			db.Create(&model.AdminPermission{
				AdminID:    admin.ID,
				Permission: perm,
			})
		}
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
