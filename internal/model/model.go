package model

import (
	"time"

	"gorm.io/gorm"
)

const (
	StatusActive   = "active"
	StatusBanned   = "banned"
	StatusDisabled = "disabled"
)

const (
	SMSScenAppLogin         = "login"
	SMSSceneCreatorLogin    = "creator_login"
	SMSSceneBankCardChange  = "bank_card_change"
)

const (
	AdminRoleAdmin   = "admin" // 超管：全部权限
	AdminRoleFinance = "finance"
	AdminRoleAuditor = "auditor" // 审核：内容审核相关
)

// 创作者主体类型：个人 / 机构。
const (
	CreatorTypePersonal     = "personal"
	CreatorTypeOrganization = "organization"
)

const (
	CreatorVerifyUnverified = "unverified" // 已注册但未提交认证资料
	CreatorVerifyPending    = "pending"    // 已提交，待 Admin 审核
	CreatorVerifyVerified   = "verified"
	CreatorVerifyRejected   = "rejected"
)

// Drama.Status —— 发布阶段（与 audit_status 配合）：
//   - draft            ：草稿，未提交审核
//   - reviewing        ：已提交，待审核
//   - awaiting_publish ：审核通过，等待上架
//   - published        ：已上架
//   - offline          ：已下架
//
// audit_status 仍保留：pending / approved / rejected，表达审核结论。
const (
	DramaStatusDraft           = "draft"
	DramaStatusReviewing       = "reviewing"
	DramaStatusAwaitingPublish = "awaiting_publish"
	DramaStatusPublished       = "published"
	DramaStatusOffline         = "offline"
)

// Drama.AuditStatus —— 审核结论：
//   - pending  ：等待 admin 审核
//   - approved ：审核通过
//   - rejected ：审核驳回
//
// 典型组合：
//   draft × approved/default     纯草稿，未提交
//   reviewing × pending            已提交，待审核
//   draft × rejected               驳回后回到草稿
//   awaiting_publish × approved    审核通过，待上架
//   published × approved           已上架
//
// 转换规则：
//   - submit        ：status → reviewing；audit_status → pending
//   - admin approve ：audit_status → approved；status: reviewing/draft/offline → awaiting_publish
//   - admin reject  ：audit_status → rejected；status: reviewing/awaiting_publish → draft；published → offline
//   - creator publish：audit_status 必须 approved；status: awaiting_publish/offline → published
const (
	DramaAuditPending  = "pending"
	DramaAuditApproved = "approved"
	DramaAuditRejected = "rejected"
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

// 红果短剧分类的 4 个维度：一部剧通常 1 个 theme + 多个 setting + 1~2 个 background + 1 个 audience。
// 流转：Category.Type 标记维度；DramaTag 做多对多；Drama.CategoryID 仍指向首要 theme，保持单 FK 老接口可用。
const (
	CategoryTypeTheme      = "theme"      // 主题：现言、古言、悬疑 …
	CategoryTypeSetting    = "setting"    // 设定：豪门、重生、甜宠 …
	CategoryTypeBackground = "background" // 背景：现代、古代、校园 …
	CategoryTypeAudience   = "audience"   // 受众：男频 / 女频
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
	ContractStatusPending   = "pending"
	ContractStatusSigning   = "signing"
	ContractStatusSigned    = "signed"
	ContractStatusCancelled = "cancelled"
)

// 男女频：申报字段，与 audience 分类维度并存，这里用独立字段方便上传表单直接选。
const (
	AudienceMale    = "男频"
	AudienceFemale  = "女频"
	AudienceGeneral = "通用"
)

// 发布类型：自主发布（发在创作者自己平台号下）/ 平台发布（发在官方号下）。
const (
	PublishTypeSelf     = "self"
	PublishTypePlatform = "platform"
)

func ValidAudience(v string) bool {
	return v == AudienceMale || v == AudienceFemale || v == AudienceGeneral
}

func ValidPublishType(v string) bool {
	return v == PublishTypeSelf || v == PublishTypePlatform
}

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
	ID                  uint64     `gorm:"primaryKey;column:id" json:"id"`
	Username            string     `gorm:"column:username;size:64;uniqueIndex" json:"username"`
	PasswordHash        string     `gorm:"column:password_hash;size:255" json:"-"`
	Role                string     `gorm:"column:role;size:32;default:admin" json:"role"`
	Status              string     `gorm:"column:status;size:20;default:active" json:"status"`
	FailedLoginAttempts int        `gorm:"column:failed_login_attempts;default:0" json:"-"`
	LockedUntil         *time.Time `gorm:"column:locked_until" json:"-"`
	CreatedAt           time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt           time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (Admin) TableName() string { return "admins" }

// Creator —— 创作者表（MVP 数据库设计 3.4）
// IDCardNoEnc / BankCardNoEnc 存 AES-GCM 密文（base64），不入接口返回。
type Creator struct {
	ID                 uint64    `gorm:"primaryKey;column:id" json:"id"`
	Phone              string    `gorm:"column:phone;size:32;uniqueIndex" json:"phone"`
	Name               string    `gorm:"column:name;size:64" json:"name"`
	Nickname           string    `gorm:"column:nickname;size:64" json:"nickname"`
	AvatarURL          string    `gorm:"column:avatar_url;size:512" json:"avatar_url"`
	AccountUID         string    `gorm:"column:account_uid;size:64;index" json:"account_uid"`
	CreatorType        string    `gorm:"column:creator_type;size:20;default:personal" json:"creator_type"` // personal / organization
	OrgName            string    `gorm:"column:org_name;size:128" json:"org_name"`                         // 机构名称（机构类型时填）
	OrgCreditCode      string    `gorm:"column:org_credit_code;size:32" json:"org_credit_code"`            // 统一社会信用代码
	BusinessLicenseURL string    `gorm:"column:business_license_url;size:512" json:"business_license_url"` // 营业执照图片
	IDCardNoEnc        string    `gorm:"column:id_card_no_enc;size:512" json:"-"`
	IDCardNoMasked     string    `gorm:"column:id_card_no_masked;size:32" json:"id_card_no_masked"`
	BankName           string    `gorm:"column:bank_name;size:64" json:"bank_name"`
	BankCardNoEnc      string    `gorm:"column:bank_card_no_enc;size:512" json:"-"`
	BankCardLast4      string    `gorm:"column:bank_card_last4;size:8" json:"-"`
	BankCardNoMasked   string    `gorm:"column:bank_card_no_masked;size:32" json:"bank_card_no_masked"`
	IdentityMID        string    `gorm:"column:identity_mid;size:64" json:"identity_mid"`   // 创作者身份信息 MID
	IdentityRole       string    `gorm:"column:identity_role;size:32" json:"identity_role"` // 版权人 / 制作方等
	VerifyStatus       string     `gorm:"column:verify_status;size:20;default:pending" json:"verify_status"`
	VerifyRejectReason string     `gorm:"column:verify_reject_reason;size:255" json:"verify_reject_reason"`
	VerifySubmittedAt  *time.Time `gorm:"column:verify_submitted_at;index" json:"verify_submitted_at"`
	TotalIncomeCents   int64      `gorm:"column:total_income_cents;default:0" json:"total_income_cents"`
	BalanceCents       int64     `gorm:"column:balance_cents;default:0" json:"balance_cents"`
	FrozenCents        int64     `gorm:"column:frozen_cents;default:0" json:"frozen_cents"`
	Status             string    `gorm:"column:status;size:20;default:active" json:"status"`
	CreatedAt          time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt          time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (Creator) TableName() string { return "creators" }

// CreatorChannelAccount —— 创作者绑定的外部渠道账号（抖音 / 快手 / 视频号等）。
type CreatorChannelAccount struct {
	ID          uint64    `gorm:"primaryKey;column:id" json:"id"`
	CreatorID   uint64    `gorm:"column:creator_id;uniqueIndex:uniq_creator_channel_uid,priority:1;index" json:"creator_id"`
	Platform    string    `gorm:"column:platform;size:32;uniqueIndex:uniq_creator_channel_uid,priority:2" json:"platform"`
	AccountUID  string    `gorm:"column:account_uid;size:128;uniqueIndex:uniq_creator_channel_uid,priority:3" json:"account_uid"`
	Nickname    string    `gorm:"column:nickname;size:128" json:"nickname"`
	AvatarURL   string    `gorm:"column:avatar_url;size:512" json:"avatar_url"`
	HomepageURL string    `gorm:"column:homepage_url;size:512" json:"homepage_url"`
	Status      string    `gorm:"column:status;size:20;default:active;index" json:"status"`
	CreatedAt   time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (CreatorChannelAccount) TableName() string { return "creator_channel_accounts" }

// Category —— 短剧分类（MVP 数据库设计 3.5）
// Type 见 CategoryType* 常量：theme / setting / background / audience。
// 老库迁移：缺省 'theme'，存量行 AutoMigrate 后默认值会自动回填。
// 唯一键 (type, name)：允许不同维度下重名（理论上不会，但 schema 上不堵）。
type Category struct {
	ID        uint64    `gorm:"primaryKey;column:id" json:"id"`
	Type      string    `gorm:"column:type;size:20;default:theme;uniqueIndex:uniq_cat_type_name,priority:1" json:"type"`
	Name      string    `gorm:"column:name;size:64;uniqueIndex:uniq_cat_type_name,priority:2;index" json:"name"`
	SortOrder int       `gorm:"column:sort_order;default:0" json:"sort_order"`
	Status    string    `gorm:"column:status;size:20;default:active" json:"status"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (Category) TableName() string { return "categories" }

// DramaTag —— 剧与分类的多对多。 Drama.CategoryID 是首要主题（向后兼容老 API），
// 其余维度 (setting / background / audience) 都通过 drama_tags 关联。
type DramaTag struct {
	ID         uint64    `gorm:"primaryKey;column:id" json:"id"`
	DramaID    uint64    `gorm:"column:drama_id;uniqueIndex:uniq_drama_tag,priority:1" json:"drama_id"`
	CategoryID uint64    `gorm:"column:category_id;uniqueIndex:uniq_drama_tag,priority:2;index" json:"category_id"`
	CreatedAt  time.Time `gorm:"column:created_at" json:"created_at"`
}

func (DramaTag) TableName() string { return "drama_tags" }

// Drama —— 短剧（MVP 数据库设计 3.6）
type Drama struct {
	ID            uint64  `gorm:"primaryKey;column:id" json:"id"`
	Title         string  `gorm:"column:title;size:128;index" json:"title"`
	Description   string  `gorm:"column:description;type:text" json:"description"`
	CoverURL      string  `gorm:"column:cover_url;size:512" json:"cover_url"`
	CategoryID    *uint64 `gorm:"column:category_id;index" json:"category_id"`
	CreatorID     *uint64 `gorm:"column:creator_id;index" json:"creator_id"`
	TotalEpisodes int     `gorm:"column:total_episodes;default:0" json:"total_episodes"`
	FreeEpisodes  int     `gorm:"column:free_episodes;default:0" json:"free_episodes"`
	PriceCents    int64   `gorm:"column:price_cents;default:0" json:"price_cents"`
	Status        string  `gorm:"column:status;size:20;default:draft;index" json:"status"`
	AuditStatus   string  `gorm:"column:audit_status;size:20;default:approved;index" json:"audit_status"`
	AuditReason   string  `gorm:"column:audit_reason;size:255" json:"audit_reason"`
	AuditSubmittedAt *time.Time `gorm:"column:audit_submitted_at;index" json:"audit_submitted_at"`
	// MVP 整剧一次审核，仅用 audit_status；以下 video_* 列保留兼容老库，接口不再暴露。
	VideoAuditStatus string     `gorm:"column:video_audit_status;size:20;default:'';index" json:"-"`
	VideoAuditReason string     `gorm:"column:video_audit_reason;size:255" json:"-"`
	VideoSubmittedAt *time.Time `gorm:"column:video_submitted_at;index" json:"-"`
	VideoReviewerID  *uint64    `gorm:"column:video_reviewer_id" json:"-"`
	VideoReviewedAt  *time.Time `gorm:"column:video_reviewed_at" json:"-"`

	// === 申报级扩展字段（2026-05-27 漫剧上传规格）===
	IsAI                bool       `gorm:"column:is_ai;default:false" json:"is_ai"`                             // 是否 AI 作品
	Audience            string     `gorm:"column:audience;size:20" json:"audience"`                             // 男频/女频/通用
	AliasPaid           string     `gorm:"column:alias_paid;size:64" json:"alias_paid"`                         // 站外付费别名（选填）
	AliasFree           string     `gorm:"column:alias_free;size:64" json:"alias_free"`                         // 站外免费别名（选填）
	ProductionOrg       string     `gorm:"column:production_org;size:128" json:"production_org"`                // 制作机构
	Producer            string     `gorm:"column:producer;size:64" json:"producer"`                             // 制片人
	Director            string     `gorm:"column:director;size:64" json:"director"`                             // 导演
	Screenwriter        string     `gorm:"column:screenwriter;size:64" json:"screenwriter"`                     // 编剧（选填）
	ProductionCostCents int64      `gorm:"column:production_cost_cents;default:0" json:"production_cost_cents"` // 备案制作金额
	CostConfigURL       string     `gorm:"column:cost_config_url;size:512" json:"cost_config_url"`              // 成本配置（图片）
	IsIPAdaptation      bool       `gorm:"column:is_ip_adaptation;default:false" json:"is_ip_adaptation"`       // 版权专区 IP 改编
	CopyrightFileURL    string     `gorm:"column:copyright_file_url;size:512" json:"copyright_file_url"`        // 权署文件（图片）
	NonInfringementURL  string     `gorm:"column:non_infringement_url;size:512" json:"non_infringement_url"`    // 不侵权承诺函
	PublishType         string     `gorm:"column:publish_type;size:20" json:"publish_type"`                     // self/platform
	ScheduledPublishAt  *time.Time `gorm:"column:scheduled_publish_at" json:"scheduled_publish_at"`             // 计划发布时间

	ReviewerID    *uint64    `gorm:"column:reviewer_id" json:"reviewer_id"`
	ReviewedAt    *time.Time `gorm:"column:reviewed_at" json:"reviewed_at"`
	PlayCount     int64      `gorm:"column:play_count;default:0" json:"play_count"`
	LikeCount     int64      `gorm:"column:like_count;default:0" json:"like_count"`
	FavoriteCount int64      `gorm:"column:favorite_count;default:0" json:"favorite_count"`
	SortOrder     int        `gorm:"column:sort_order;default:0" json:"sort_order"`
	PublishedAt   *time.Time `gorm:"column:published_at" json:"published_at"`
	CreatedAt     time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (Drama) TableName() string { return "dramas" }

// DramaCover —— 漫剧封面多图（≤5）。Drama.CoverURL 始终存第一张作为主封面，向后兼容老接口。
type DramaCover struct {
	ID        uint64    `gorm:"primaryKey;column:id" json:"id"`
	DramaID   uint64    `gorm:"column:drama_id;index" json:"drama_id"`
	URL       string    `gorm:"column:url;size:512" json:"url"`
	SortOrder int       `gorm:"column:sort_order;default:0" json:"sort_order"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

func (DramaCover) TableName() string { return "drama_covers" }

// DramaCharacter —— 角色信息（至少 1 位）。姓名必填，照片/简介选填。
type DramaCharacter struct {
	ID        uint64    `gorm:"primaryKey;column:id" json:"id"`
	DramaID   uint64    `gorm:"column:drama_id;index" json:"drama_id"`
	Name      string    `gorm:"column:name;size:64" json:"name"`
	PhotoURL  string    `gorm:"column:photo_url;size:512" json:"photo_url"`
	Intro     string    `gorm:"column:intro;type:text" json:"intro"`
	SortOrder int       `gorm:"column:sort_order;default:0" json:"sort_order"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

func (DramaCharacter) TableName() string { return "drama_characters" }

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

// Comment —— APP 端评论。
// 软删用 DeletedAt（GORM 自动支持）：删除后 List 不返，但保留落库供审计 / 复活。
// 长度限制 1000，超长在 handler 截断+拒绝；user_id 索引按用户拉自己评论。
type Comment struct {
	ID        uint64         `gorm:"primaryKey;column:id" json:"id"`
	DramaID   uint64         `gorm:"column:drama_id;index;not null" json:"drama_id"`
	UserID    uint64         `gorm:"column:user_id;index;not null" json:"user_id"`
	Content   string         `gorm:"column:content;type:text;not null" json:"content"`
	CreatedAt time.Time      `gorm:"column:created_at;index" json:"created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at" json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;index" json:"-"`
}

func (Comment) TableName() string { return "comments" }

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
	ID              uint64     `gorm:"primaryKey;column:id" json:"id"`
	OrderNo         string     `gorm:"column:order_no;size:64;uniqueIndex" json:"order_no"`
	UserID          uint64     `gorm:"column:user_id;index" json:"user_id"`
	ProductID       *uint64    `gorm:"column:product_id" json:"product_id"`
	DramaID         uint64     `gorm:"column:drama_id;index" json:"drama_id"`
	EpisodeID       uint64     `gorm:"column:episode_id;index" json:"episode_id"`
	AmountCents     int64      `gorm:"column:amount_cents" json:"amount_cents"`
	PaymentMethod   string     `gorm:"column:payment_method;size:20" json:"payment_method"`
	PlatformTradeNo string     `gorm:"column:platform_trade_no;size:128;index" json:"platform_trade_no"`
	Status          string     `gorm:"column:status;size:20;default:pending;index" json:"status"`
	PaidAt          *time.Time `gorm:"column:paid_at" json:"paid_at"`
	ExpiredAt       *time.Time `gorm:"column:expired_at" json:"expired_at"`
	CreatedAt       time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at" json:"updated_at"`
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
	DramaID            *uint64    `gorm:"column:drama_id;index" json:"drama_id"`
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

// ChannelIncomeDaily —— 第三方渠道每日收益明细（财务 Excel 导入）。
// 本平台自有付费收入走支付分账写 creator_stats_daily，不进此表。
// 唯一键 (drama_id, channel, stat_date)：同剧同渠道同日重复导入按覆盖处理。
type ChannelIncomeDaily struct {
	ID          uint64    `gorm:"primaryKey;column:id" json:"id"`
	DramaID     uint64    `gorm:"column:drama_id;uniqueIndex:uniq_channel_income,priority:1" json:"drama_id"`
	Channel     string    `gorm:"column:channel;size:32;uniqueIndex:uniq_channel_income,priority:2" json:"channel"`
	StatDate    string    `gorm:"column:stat_date;size:10;uniqueIndex:uniq_channel_income,priority:3" json:"stat_date"`
	CreatorID   uint64    `gorm:"column:creator_id;index" json:"creator_id"`
	IncomeCents int64     `gorm:"column:income_cents;default:0" json:"income_cents"`
	BatchNo     string    `gorm:"column:batch_no;size:32;index" json:"batch_no"`
	ImportRowNo int       `gorm:"column:import_row_no;default:0" json:"import_row_no"`
	CreatedAt   time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (ChannelIncomeDaily) TableName() string { return "channel_income_daily" }

// ChannelIncomeImportBatch —— 财务 Excel 导入批次记录，便于后台查历史与逐行报告。
type ChannelIncomeImportBatch struct {
	ID               uint64    `gorm:"primaryKey;column:id" json:"id"`
	BatchNo          string    `gorm:"column:batch_no;size:32;uniqueIndex" json:"batch_no"`
	AdminID          uint64    `gorm:"column:admin_id;index" json:"admin_id"`
	FileName         string    `gorm:"column:file_name;size:255" json:"file_name"`
	ProcessedRows    int       `gorm:"column:processed_rows;default:0" json:"processed_rows"`
	CreatedRows      int       `gorm:"column:created_rows;default:0" json:"created_rows"`
	UpdatedRows      int       `gorm:"column:updated_rows;default:0" json:"updated_rows"`
	UnchangedRows    int       `gorm:"column:unchanged_rows;default:0" json:"unchanged_rows"`
	DuplicateRows    int       `gorm:"column:duplicate_rows;default:0" json:"duplicate_rows"`
	FailedRows       int       `gorm:"column:failed_rows;default:0" json:"failed_rows"`
	IncomeDeltaCents int64     `gorm:"column:income_delta_cents;default:0" json:"income_delta_cents"`
	RowReportsJSON   string    `gorm:"column:row_reports_json;type:text" json:"-"`
	CreatedAt        time.Time `gorm:"column:created_at;index" json:"created_at"`
}

func (ChannelIncomeImportBatch) TableName() string { return "channel_income_import_batches" }

// OperationLog —— 后台操作审计日志；不记录请求体，避免敏感信息落库。
type OperationLog struct {
	ID              uint64    `gorm:"primaryKey;column:id" json:"id"`
	ActorSubject    string    `gorm:"column:actor_subject;size:32;index" json:"actor_subject"`
	ActorID         uint64    `gorm:"column:actor_id;index" json:"actor_id"`
	Method          string    `gorm:"column:method;size:10" json:"method"`
	Path            string    `gorm:"column:path;size:255;index" json:"path"`
	FullPath        string    `gorm:"column:full_path;size:255;index" json:"full_path"`
	Action          string    `gorm:"column:action;size:64;index" json:"action"`
	ResourceType    string    `gorm:"column:resource_type;size:64;index" json:"resource_type"`
	ResourceID      string    `gorm:"column:resource_id;size:64;index" json:"resource_id"`
	StatusCode      int       `gorm:"column:status_code" json:"status_code"`
	ResponseCode    int       `gorm:"column:response_code" json:"response_code"`
	ResponseMessage string    `gorm:"column:response_message;size:255" json:"response_message"`
	ClientIP        string    `gorm:"column:client_ip;size:64" json:"client_ip"`
	UserAgent       string    `gorm:"column:user_agent;size:255" json:"user_agent"`
	CreatedAt       time.Time `gorm:"column:created_at;index" json:"created_at"`
}

func (OperationLog) TableName() string { return "operation_logs" }

// Notification —— 创作者站内消息。MVP 只做纯文本 + 可选跳转链接。
type Notification struct {
	ID        uint64    `gorm:"primaryKey;column:id" json:"id"`
	CreatorID uint64    `gorm:"column:creator_id;index" json:"creator_id"`
	Title     string    `gorm:"column:title;size:128" json:"title"`
	Content   string    `gorm:"column:content;type:text" json:"content"`
	LinkURL   string    `gorm:"column:link_url;size:512" json:"link_url"`
	IsRead    bool      `gorm:"column:is_read;default:false;index" json:"is_read"`
	CreatedAt time.Time `gorm:"column:created_at;index" json:"created_at"`
}

func (Notification) TableName() string { return "notifications" }

// GlobalConfig —— 全局键值配置。MVP 用于统一管理免费集数 / 每集单价。
type GlobalConfig struct {
	Key       string    `gorm:"primaryKey;column:key;size:64" json:"key"`
	Value     string    `gorm:"column:value;size:255" json:"value"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (GlobalConfig) TableName() string { return "global_configs" }

// 全局配置键。
const (
	ConfigKeyFreeEpisodes = "pricing.free_episodes" // 全局默认免费集数
	ConfigKeyPriceCents   = "pricing.price_cents"   // 全局默认每集单价（分）
)
