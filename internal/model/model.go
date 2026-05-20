package model

import (
	"time"
)

const (
	StatusActive   = "active"
	StatusBanned   = "banned"
	StatusDisabled = "disabled"
)

const (
	SMSScenAppLogin      = "login"
	SMSSceneCreatorLogin = "creator_login"
)

const (
	AdminRoleAdmin   = "admin"
	AdminRoleFinance = "finance"
)

const (
	CreatorVerifyPending  = "pending"
	CreatorVerifyVerified = "verified"
	CreatorVerifyRejected = "rejected"
)

const (
	DramaStatusDraft     = "draft"
	DramaStatusReviewing = "reviewing"
	DramaStatusPublished = "published"
	DramaStatusOffline   = "offline"
)

const (
	EpisodeStatusUploading = "uploading"
	EpisodeStatusReady     = "ready"
	EpisodeStatusFailed    = "failed"
)

const (
	ActionLike     = "like"
	ActionFavorite = "favorite"
)

const (
	ProductTypeEpisodeUnlock = "episode_unlock"
)

const (
	PaymentMethodWechat = "wechat"
	PaymentMethodAlipay = "alipay"
)

const (
	OrderStatusPending  = "pending"
	OrderStatusPaid     = "paid"
	OrderStatusFailed   = "failed"
	OrderStatusClosed   = "closed"
	OrderStatusRefunded = "refunded"
)

const (
	WithdrawalStatusPending  = "pending"
	WithdrawalStatusApproved = "approved"
	WithdrawalStatusRejected = "rejected"
	WithdrawalStatusPaid     = "paid"
)

const (
	ContractStatusPending = "pending"
	ContractStatusSigning = "signing"
	ContractStatusSigned  = "signed"
)

// User —— APP 用户表（MVP 数据库设计 3.1）
type User struct {
	ID        uint64    `gorm:"primaryKey;column:id" json:"id"`
	Phone     string    `gorm:"column:phone;size:32;uniqueIndex" json:"phone"`
	Nickname  string    `gorm:"column:nickname;size:64" json:"nickname"`
	Avatar    string    `gorm:"column:avatar;size:512" json:"avatar"`
	Status    string    `gorm:"column:status;size:20;default:active" json:"status"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (User) TableName() string { return "users" }

// SMSCode —— 短信验证码表（MVP 数据库设计 3.2）
type SMSCode struct {
	ID        uint64     `gorm:"primaryKey;column:id" json:"id"`
	Phone     string     `gorm:"column:phone;size:32;index" json:"phone"`
	Code      string     `gorm:"column:code;size:8" json:"code"`
	Scene     string     `gorm:"column:scene;size:32;index" json:"scene"`
	ExpiredAt time.Time  `gorm:"column:expired_at" json:"expired_at"`
	UsedAt    *time.Time `gorm:"column:used_at" json:"used_at"`
	CreatedAt time.Time  `gorm:"column:created_at" json:"created_at"`
}

func (SMSCode) TableName() string { return "sms_codes" }

// Admin —— 管理员表（MVP 数据库设计 3.3）
type Admin struct {
	ID           uint64    `gorm:"primaryKey;column:id" json:"id"`
	Username     string    `gorm:"column:username;size:64;uniqueIndex" json:"username"`
	PasswordHash string    `gorm:"column:password_hash;size:255" json:"-"`
	Role         string    `gorm:"column:role;size:32;default:admin" json:"role"`
	Status       string    `gorm:"column:status;size:20;default:active" json:"status"`
	CreatedAt    time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (Admin) TableName() string { return "admins" }

// Creator —— 创作者表（MVP 数据库设计 3.4）
// IDCardNoEnc / BankCardNoEnc 存 AES-GCM 密文（base64），不入接口返回。
type Creator struct {
	ID               uint64    `gorm:"primaryKey;column:id" json:"id"`
	Phone            string    `gorm:"column:phone;size:32;uniqueIndex" json:"phone"`
	Name             string    `gorm:"column:name;size:64" json:"name"`
	IDCardNoEnc      string    `gorm:"column:id_card_no_enc;size:512" json:"-"`
	BankName         string    `gorm:"column:bank_name;size:64" json:"bank_name"`
	BankCardNoEnc    string    `gorm:"column:bank_card_no_enc;size:512" json:"-"`
	BankCardLast4    string    `gorm:"column:bank_card_last4;size:8" json:"-"`
	VerifyStatus     string    `gorm:"column:verify_status;size:20;default:pending" json:"verify_status"`
	TotalIncomeCents int64     `gorm:"column:total_income_cents;default:0" json:"total_income_cents"`
	BalanceCents     int64     `gorm:"column:balance_cents;default:0" json:"balance_cents"`
	FrozenCents      int64     `gorm:"column:frozen_cents;default:0" json:"frozen_cents"`
	Status           string    `gorm:"column:status;size:20;default:active" json:"status"`
	CreatedAt        time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt        time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (Creator) TableName() string { return "creators" }

// Category —— 短剧分类（MVP 数据库设计 3.5）
type Category struct {
	ID        uint64    `gorm:"primaryKey;column:id" json:"id"`
	Name      string    `gorm:"column:name;size:64;index" json:"name"`
	SortOrder int       `gorm:"column:sort_order;default:0" json:"sort_order"`
	Status    string    `gorm:"column:status;size:20;default:active" json:"status"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (Category) TableName() string { return "categories" }

// Drama —— 短剧（MVP 数据库设计 3.6）
type Drama struct {
	ID            uint64     `gorm:"primaryKey;column:id" json:"id"`
	Title         string     `gorm:"column:title;size:128;index" json:"title"`
	Description   string     `gorm:"column:description;type:text" json:"description"`
	CoverURL      string     `gorm:"column:cover_url;size:512" json:"cover_url"`
	CategoryID    *uint64    `gorm:"column:category_id;index" json:"category_id"`
	CreatorID     *uint64    `gorm:"column:creator_id;index" json:"creator_id"`
	TotalEpisodes int        `gorm:"column:total_episodes;default:0" json:"total_episodes"`
	FreeEpisodes  int        `gorm:"column:free_episodes;default:0" json:"free_episodes"`
	PriceCents    int64      `gorm:"column:price_cents;default:0" json:"price_cents"`
	Status        string     `gorm:"column:status;size:20;default:draft;index" json:"status"`
	PlayCount     int64      `gorm:"column:play_count;default:0" json:"play_count"`
	LikeCount     int64      `gorm:"column:like_count;default:0" json:"like_count"`
	FavoriteCount int64      `gorm:"column:favorite_count;default:0" json:"favorite_count"`
	SortOrder     int        `gorm:"column:sort_order;default:0" json:"sort_order"`
	PublishedAt   *time.Time `gorm:"column:published_at" json:"published_at"`
	CreatedAt     time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (Drama) TableName() string { return "dramas" }

// Episode —— 剧集（MVP 数据库设计 3.7）
type Episode struct {
	ID              uint64    `gorm:"primaryKey;column:id" json:"id"`
	DramaID         uint64    `gorm:"column:drama_id;uniqueIndex:uniq_drama_episode_no,priority:1" json:"drama_id"`
	EpisodeNo       int       `gorm:"column:episode_no;uniqueIndex:uniq_drama_episode_no,priority:2" json:"episode_no"`
	Title           string    `gorm:"column:title;size:128" json:"title"`
	VODFileID       string    `gorm:"column:vod_file_id;size:128" json:"vod_file_id"`
	VideoURL        string    `gorm:"column:video_url;size:512" json:"video_url"`
	DurationSeconds int       `gorm:"column:duration_seconds;default:0" json:"duration_seconds"`
	Status          string    `gorm:"column:status;size:20;default:uploading" json:"status"`
	CreatedAt       time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt       time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (Episode) TableName() string { return "episodes" }

// PlayHistory —— 观看历史（MVP 数据库设计 3.8）
type PlayHistory struct {
	ID              uint64    `gorm:"primaryKey;column:id" json:"id"`
	UserID          uint64    `gorm:"column:user_id;uniqueIndex:uniq_user_episode,priority:1" json:"user_id"`
	DramaID         uint64    `gorm:"column:drama_id;index" json:"drama_id"`
	EpisodeID       uint64    `gorm:"column:episode_id;uniqueIndex:uniq_user_episode,priority:2" json:"episode_id"`
	ProgressSeconds int       `gorm:"column:progress_seconds;default:0" json:"progress_seconds"`
	UpdatedAt       time.Time `gorm:"column:updated_at" json:"updated_at"`
	CreatedAt       time.Time `gorm:"column:created_at" json:"created_at"`
}

func (PlayHistory) TableName() string { return "play_histories" }

// UserAction —— 点赞 / 收藏（MVP 数据库设计 3.9）
type UserAction struct {
	ID        uint64    `gorm:"primaryKey;column:id" json:"id"`
	UserID    uint64    `gorm:"column:user_id;uniqueIndex:uniq_user_drama_action,priority:1" json:"user_id"`
	DramaID   uint64    `gorm:"column:drama_id;uniqueIndex:uniq_user_drama_action,priority:2" json:"drama_id"`
	Action    string    `gorm:"column:action;size:20;uniqueIndex:uniq_user_drama_action,priority:3" json:"action"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

func (UserAction) TableName() string { return "user_actions" }

// Product —— 商品（MVP 数据库设计 3.11）
type Product struct {
	ID         uint64    `gorm:"primaryKey;column:id" json:"id"`
	Name       string    `gorm:"column:name;size:64" json:"name"`
	Type       string    `gorm:"column:type;size:20" json:"type"`
	PriceCents int64     `gorm:"column:price_cents;default:0" json:"price_cents"`
	Status     string    `gorm:"column:status;size:20;default:active" json:"status"`
	CreatedAt  time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt  time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (Product) TableName() string { return "products" }

// Order —— 订单（MVP 数据库设计 3.12）
type Order struct {
	ID               uint64     `gorm:"primaryKey;column:id" json:"id"`
	OrderNo          string     `gorm:"column:order_no;size:64;uniqueIndex" json:"order_no"`
	UserID           uint64     `gorm:"column:user_id;index" json:"user_id"`
	ProductID        *uint64    `gorm:"column:product_id" json:"product_id"`
	DramaID          uint64     `gorm:"column:drama_id;index" json:"drama_id"`
	EpisodeID        uint64     `gorm:"column:episode_id;index" json:"episode_id"`
	AmountCents      int64      `gorm:"column:amount_cents" json:"amount_cents"`
	PaymentMethod    string     `gorm:"column:payment_method;size:20" json:"payment_method"`
	PlatformTradeNo  string     `gorm:"column:platform_trade_no;size:128;index" json:"platform_trade_no"`
	Status           string     `gorm:"column:status;size:20;default:pending;index" json:"status"`
	PaidAt           *time.Time `gorm:"column:paid_at" json:"paid_at"`
	ExpiredAt        *time.Time `gorm:"column:expired_at" json:"expired_at"`
	CreatedAt        time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt        time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (Order) TableName() string { return "orders" }

// EpisodeUnlock —— 剧集解锁（MVP 数据库设计 3.13）
type EpisodeUnlock struct {
	ID        uint64    `gorm:"primaryKey;column:id" json:"id"`
	UserID    uint64    `gorm:"column:user_id;uniqueIndex:uniq_user_episode_unlock,priority:1" json:"user_id"`
	DramaID   uint64    `gorm:"column:drama_id;index" json:"drama_id"`
	EpisodeID uint64    `gorm:"column:episode_id;uniqueIndex:uniq_user_episode_unlock,priority:2" json:"episode_id"`
	OrderID   *uint64   `gorm:"column:order_id" json:"order_id"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

func (EpisodeUnlock) TableName() string { return "episode_unlocks" }

// Contract —— 合同（MVP 数据库设计 3.14）
type Contract struct {
	ID          uint64    `gorm:"primaryKey;column:id" json:"id"`
	CreatorID   uint64    `gorm:"column:creator_id;index" json:"creator_id"`
	DramaID     *uint64   `gorm:"column:drama_id" json:"drama_id"`
	ContractNo  string    `gorm:"column:contract_no;size:64;index" json:"contract_no"`
	EsignFlowID string    `gorm:"column:esign_flow_id;size:128" json:"esign_flow_id"`
	FileURL     string    `gorm:"column:file_url;size:512" json:"file_url"`
	Status      string    `gorm:"column:status;size:20;default:pending" json:"status"`
	CreatedAt   time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (Contract) TableName() string { return "contracts" }

// Withdrawal —— 提现申请（MVP 数据库设计 3.15）
type Withdrawal struct {
	ID                 uint64     `gorm:"primaryKey;column:id" json:"id"`
	WithdrawalNo       string     `gorm:"column:withdrawal_no;size:64;uniqueIndex" json:"withdrawal_no"`
	CreatorID          uint64     `gorm:"column:creator_id;index" json:"creator_id"`
	AmountCents        int64      `gorm:"column:amount_cents" json:"amount_cents"`
	BankNameSnapshot   string     `gorm:"column:bank_name_snapshot;size:64" json:"bank_name_snapshot"`
	BankCardNoSnapshot string     `gorm:"column:bank_card_no_snapshot;size:64" json:"bank_card_no_snapshot"`
	Status             string     `gorm:"column:status;size:20;default:pending;index" json:"status"`
	Remark             string     `gorm:"column:remark;size:255" json:"remark"`
	TransactionNo      string     `gorm:"column:transaction_no;size:128" json:"transaction_no"`
	ReviewedBy         *uint64    `gorm:"column:reviewed_by" json:"reviewed_by"`
	ReviewedAt         *time.Time `gorm:"column:reviewed_at" json:"reviewed_at"`
	PaidAt             *time.Time `gorm:"column:paid_at" json:"paid_at"`
	CreatedAt          time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt          time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (Withdrawal) TableName() string { return "withdrawals" }

// CreatorStatsDaily —— 创作者每日数据（MVP 数据库设计 3.16）
type CreatorStatsDaily struct {
	ID          uint64    `gorm:"primaryKey;column:id" json:"id"`
	CreatorID   uint64    `gorm:"column:creator_id;uniqueIndex:uniq_creator_drama_date,priority:1" json:"creator_id"`
	DramaID     uint64    `gorm:"column:drama_id;uniqueIndex:uniq_creator_drama_date,priority:2" json:"drama_id"`
	StatDate    string    `gorm:"column:stat_date;size:10;uniqueIndex:uniq_creator_drama_date,priority:3" json:"stat_date"`
	PlayCount   int64     `gorm:"column:play_count;default:0" json:"play_count"`
	IncomeCents int64     `gorm:"column:income_cents;default:0" json:"income_cents"`
	CreatedAt   time.Time `gorm:"column:created_at" json:"created_at"`
}

func (CreatorStatsDaily) TableName() string { return "creator_stats_daily" }
