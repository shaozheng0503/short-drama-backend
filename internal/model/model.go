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
	SMSScenAppLogin        = "login"
	SMSSceneCreatorLogin   = "creator_login"
	SMSSceneBankCardChange = "bank_card_change"
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
//
//	draft × pending/default        纯草稿，未提交（未提交时不应展示"审核通过"，用 pending 表达"尚未过审"）
//	reviewing × pending            已提交，待审核
//	draft × rejected               驳回后回到草稿
//	awaiting_publish × approved    审核通过，待上架
//	published × approved           已上架
//
// 注意：草稿与"已提交待审"都是 audit_status=pending，靠 status 区分——
//
//	admin「待审核」队列须按 status=reviewing 取，不能只按 audit_status=pending（否则未提交草稿会混进来）。
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

// 分批审核维度（2026-06-10 会议：短剧审核拆资料内容 / 视频内容 / 合同，本期先做前两项）。
const (
	DramaAuditDimensionContent = "content" // 资料内容审核
	DramaAuditDimensionVideo   = "video"   // 视频内容审核
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
	OrderStatusPending         = "pending"
	OrderStatusPaid            = "paid"
	OrderStatusFailed          = "failed"
	OrderStatusClosed          = "closed"
	OrderStatusRefunded        = "refunded"         // 全额退款
	OrderStatusPartialRefunded = "partial_refunded" // 部分退款,后续可继续退
)

const (
	WithdrawalStatusPending  = "pending"
	WithdrawalStatusApproved = "approved"
	WithdrawalStatusRejected = "rejected"
	WithdrawalStatusPaid     = "paid"
)

// 打款类型：机构=对公，个人=对私。供财务手动打款区分。
const (
	TransferTypePublic  = "public"  // 对公（机构创作者）
	TransferTypePrivate = "private" // 对私（个人创作者）
)

// TransferTypeOf 按创作者类型推断打款类型。
func TransferTypeOf(creatorType string) string {
	if creatorType == CreatorTypeOrganization {
		return TransferTypePublic
	}
	return TransferTypePrivate
}

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
	ID                 uint64 `gorm:"primaryKey;column:id" json:"id"`
	Phone              string `gorm:"column:phone;size:32;uniqueIndex" json:"phone"`
	Name               string `gorm:"column:name;size:64" json:"name"`
	Nickname           string `gorm:"column:nickname;size:64" json:"nickname"`
	AvatarURL          string `gorm:"column:avatar_url;size:512" json:"avatar_url"`
	AccountUID         string `gorm:"column:account_uid;size:64;index" json:"account_uid"`
	CreatorType        string `gorm:"column:creator_type;size:20;default:personal" json:"creator_type"` // personal / organization
	OrgName            string `gorm:"column:org_name;size:128" json:"org_name"`                         // 机构名称（机构类型时填）
	OrgCreditCode      string `gorm:"column:org_credit_code;size:32" json:"org_credit_code"`            // 统一社会信用代码
	BusinessLicenseURL string `gorm:"column:business_license_url;size:512" json:"business_license_url"` // 营业执照图片
	IDCardNoEnc        string `gorm:"column:id_card_no_enc;size:512" json:"-"`
	IDCardNoMasked     string `gorm:"column:id_card_no_masked;size:32" json:"id_card_no_masked"`
	BankName           string `gorm:"column:bank_name;size:64" json:"bank_name"`
	BankBranch         string `gorm:"column:bank_branch;size:128" json:"bank_branch"`           // 开户支行（避免跨行转账受限）
	BankLicenseURL     string `gorm:"column:bank_license_url;size:512" json:"bank_license_url"` // 银行开户许可证（机构认证）
	BankCardNoEnc      string `gorm:"column:bank_card_no_enc;size:512" json:"-"`
	BankCardLast4      string `gorm:"column:bank_card_last4;size:8" json:"-"`
	BankCardNoMasked   string `gorm:"column:bank_card_no_masked;size:32" json:"bank_card_no_masked"`
	IdentityMID        string `gorm:"column:identity_mid;size:64" json:"identity_mid"`   // 创作者身份信息 MID
	IdentityRole       string `gorm:"column:identity_role;size:32" json:"identity_role"` // 版权人 / 制作方等
	VerifyStatus       string `gorm:"column:verify_status;size:20;default:pending" json:"verify_status"`
	VerifyRejectReason string `gorm:"column:verify_reject_reason;size:255" json:"verify_reject_reason"`
	// VerifyRejectFields 字段级驳回标记：逗号分隔的字段 key（如 "bank_card_no,org_legal_id_card"），
	// 供前端高亮"具体哪项被驳"。审核驳回时由 Admin 选填；创作者重新提交时清空。
	VerifyRejectFields string     `gorm:"column:verify_reject_fields;size:255" json:"verify_reject_fields"`
	VerifySubmittedAt  *time.Time `gorm:"column:verify_submitted_at;index" json:"verify_submitted_at"`
	// 第三方核验存档（个人=银行卡三要素 bankcard3 / 企业=营业执照四要素 biz_4e / 降级或未核验 manual）
	OrgLegalPerson       string     `gorm:"column:org_legal_person;size:64" json:"org_legal_person"`                 // 企业法定代表人姓名（用户填，四要素核验项）
	OrgLegalIDCardEnc    string     `gorm:"column:org_legal_id_card_enc;size:512" json:"-"`                          // 法人身份证号密文（AES-GCM）
	OrgLegalIDCardMasked string     `gorm:"column:org_legal_id_card_masked;size:32" json:"org_legal_id_card_masked"` // 法人身份证号脱敏
	VerifyMethod         string     `gorm:"column:verify_method;size:32" json:"verify_method"`                       // 核验方式：bankcard3 / biz_ocr / manual / ""
	VerifyProviderResult string     `gorm:"column:verify_provider_result;type:text" json:"-"`                        // 腾讯云核验原始返回（存档，供 Admin 复核）
	VerifyCheckedAt      *time.Time `gorm:"column:verify_checked_at" json:"verify_checked_at"`                       // 第三方核验时间
	TotalIncomeCents     int64      `gorm:"column:total_income_cents;default:0" json:"total_income_cents"`
	BalanceCents         int64      `gorm:"column:balance_cents;default:0" json:"balance_cents"`
	FrozenCents          int64      `gorm:"column:frozen_cents;default:0" json:"frozen_cents"`
	Status               string     `gorm:"column:status;size:20;default:active" json:"status"`
	CreatedAt            time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt            time.Time  `gorm:"column:updated_at" json:"updated_at"`
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

// Language —— 可配置语言 / 方言。ParentID 为空=语言（中文/英文…），非空=该语言下的方言子项（粤语/闽南语…）。
// 海外版用它做语言筛选，吴建棉要的「单独方言选择」即 ParentID 非空的子项。
type Language struct {
	ID        uint64    `gorm:"primaryKey;column:id" json:"id"`
	ParentID  *uint64   `gorm:"column:parent_id;index" json:"parent_id"` // 空=语言，非空=方言
	Name      string    `gorm:"column:name;size:64;index" json:"name"`
	Code      string    `gorm:"column:code;size:32;index" json:"code"` // 选填，如 zh / en / yue
	SortOrder int       `gorm:"column:sort_order;default:0" json:"sort_order"`
	Status    string    `gorm:"column:status;size:20;default:active;index" json:"status"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (Language) TableName() string { return "languages" }

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
	ID               uint64     `gorm:"primaryKey;column:id" json:"id"`
	Title            string     `gorm:"column:title;size:128;index" json:"title"`
	Description      string     `gorm:"column:description;type:text" json:"description"`
	CoverURL         string     `gorm:"column:cover_url;size:512" json:"cover_url"`
	CategoryID       *uint64    `gorm:"column:category_id;index" json:"category_id"`
	CreatorID        *uint64    `gorm:"column:creator_id;index" json:"creator_id"`
	TotalEpisodes    int        `gorm:"column:total_episodes;default:0" json:"total_episodes"`
	FreeEpisodes     int        `gorm:"column:free_episodes;default:0" json:"free_episodes"`
	PriceCents       int64      `gorm:"column:price_cents;default:0" json:"price_cents"`
	Status           string     `gorm:"column:status;size:20;default:draft;index" json:"status"`
	AuditStatus      string     `gorm:"column:audit_status;size:20;default:pending;index" json:"audit_status"`
	AuditReason      string     `gorm:"column:audit_reason;size:255" json:"audit_reason"`
	AuditSubmittedAt *time.Time `gorm:"column:audit_submitted_at;index" json:"audit_submitted_at"`
	// 分批审核（2026-06-10 会议）：资料内容 + 视频内容各自独立审核，audit_status 为派生总状态
	//（资料✓且视频✓才 approved；任一 rejected 则 rejected）。合同维度暂沿用 Contract.status，未纳入派生。
	// 维度状态取值同 audit：pending/approved/rejected；默认 pending（未审），与 audit_status 保持一致，避免新建剧分维度显示成空。
	ContentAuditStatus string     `gorm:"column:content_audit_status;size:20;default:pending;index" json:"content_audit_status"`
	ContentAuditReason string     `gorm:"column:content_audit_reason;size:255" json:"content_audit_reason"`
	VideoAuditStatus   string     `gorm:"column:video_audit_status;size:20;default:pending;index" json:"video_audit_status"`
	VideoAuditReason   string     `gorm:"column:video_audit_reason;size:255" json:"video_audit_reason"`
	VideoSubmittedAt   *time.Time `gorm:"column:video_submitted_at;index" json:"-"`
	VideoReviewerID    *uint64    `gorm:"column:video_reviewer_id" json:"-"`
	VideoReviewedAt    *time.Time `gorm:"column:video_reviewed_at" json:"-"`

	// === 申报级扩展字段（2026-05-27 漫剧上传规格）===
	IsAI                bool       `gorm:"column:is_ai;default:false" json:"is_ai"`                             // 是否 AI 作品
	AIGCTools           []string   `gorm:"column:aigc_tools;serializer:json" json:"aigc_tools"`                 // 关联 AIGC 创作工具（多选固定 tag，即梦/小云雀等，is_ai 时填）
	LanguageID          *uint64    `gorm:"column:language_id;index" json:"language_id"`                         // 语言/方言（languages.id，含粤语等子项）
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
	// CopyrightFileURLs —— 2026-07-03 改：版权/授权文件多张图（吴建棉要求）
	// 旧字段名 copyright_file_url (varchar 512, 单图) → 新字段名 copyright_file_urls (text, JSON 数组)
	// 存 JSON 数组；老数据是单图 URL 字符串，serializer:json 反序列化失败时回退到空数组（前端用空兜底）
	CopyrightFileURLs   []string   `gorm:"column:copyright_file_urls;type:text;serializer:json" json:"copyright_file_urls"`
	NonInfringementURL  string     `gorm:"column:non_infringement_url;size:512" json:"non_infringement_url"`    // 不侵权承诺函
	PublishType         string     `gorm:"column:publish_type;size:20" json:"publish_type"`                     // self/platform
	ScheduledPublishAt  *time.Time `gorm:"column:scheduled_publish_at" json:"scheduled_publish_at"`             // 计划发布时间

	ReviewerID    *uint64    `gorm:"column:reviewer_id" json:"reviewer_id"`
	ReviewedAt    *time.Time `gorm:"column:reviewed_at" json:"reviewed_at"`
	PlayCount     int64      `gorm:"column:play_count;default:0" json:"play_count"`
	LikeCount     int64      `gorm:"column:like_count;default:0" json:"like_count"`
	FavoriteCount int64      `gorm:"column:favorite_count;default:0" json:"favorite_count"`
	ShareCount    int64      `gorm:"column:share_count;default:0" json:"share_count"`
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
	Title           string     `gorm:"column:title;size:128" json:"title"`
	VODFileID       string     `gorm:"column:vod_file_id;size:128" json:"vod_file_id"`
	VideoURL        string     `gorm:"column:video_url;size:512" json:"video_url"`
	DurationSeconds int        `gorm:"column:duration_seconds;default:0" json:"duration_seconds"`
	Status          string     `gorm:"column:status;size:20;default:uploading" json:"status"`
	LikeCount       int64      `gorm:"column:like_count;default:0" json:"like_count"` // 集级点赞数（对齐红果：点赞是单集的）
	// VODSyncedAt —— 后端最近一次主动调 VOD DescribeMediaInfos 的时间
	// （v0.13.1 懒加载机制用，30 秒内不重复调）
	VODSyncedAt  *time.Time `gorm:"column:vod_synced_at" json:"vod_synced_at,omitempty"`
	CreatedAt    time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt    time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (Episode) TableName() string { return "episodes" }

// Comment —— APP 端评论。
// 软删用 DeletedAt（GORM 自动支持）：删除后 List 不返，但保留落库供审计 / 复活。
// 长度限制 1000，超长在 handler 截断+拒绝；user_id 索引按用户拉自己评论。
type Comment struct {
	ID        uint64  `gorm:"primaryKey;column:id" json:"id"`
	DramaID   uint64  `gorm:"column:drama_id;index;not null" json:"drama_id"`
	EpisodeID *uint64 `gorm:"column:episode_id;index" json:"episode_id"` // 空=剧评，有值=该集的集评
	UserID    uint64  `gorm:"column:user_id;index;not null" json:"user_id"`
	// 楼中楼：两级模型。ParentID 空=顶层评论；非空=回复，指向「顶层评论」（回复的回复也拍平挂到同一顶层）。
	// ReplyToUserID=被回复者，用于「回复 @某人」展示（直接回复顶层评论时可为空）。
	ParentID      *uint64        `gorm:"column:parent_id;index" json:"parent_id"`
	ReplyToUserID *uint64        `gorm:"column:reply_to_user_id" json:"reply_to_user_id"`
	Content       string         `gorm:"column:content;type:text;not null" json:"content"`
	LikeCount     int64          `gorm:"column:like_count;default:0" json:"like_count"`   // 评论点赞数（冗余列，点赞/取消时事务±1）
	ReplyCount    int64          `gorm:"column:reply_count;default:0" json:"reply_count"` // 回复数（冗余列，仅顶层评论维护）
	CreatedAt     time.Time      `gorm:"column:created_at;index" json:"created_at"`
	UpdatedAt     time.Time      `gorm:"column:updated_at" json:"updated_at"`
	DeletedAt     gorm.DeletedAt `gorm:"column:deleted_at;index" json:"-"`
}

func (Comment) TableName() string { return "comments" }

// CommentLike —— 评论点赞。唯一键 (comment_id,user_id) 保证一人一赞，幂等点赞用 OnConflict DoNothing。
type CommentLike struct {
	ID        uint64    `gorm:"primaryKey;column:id" json:"id"`
	CommentID uint64    `gorm:"column:comment_id;uniqueIndex:uniq_comment_user,priority:1" json:"comment_id"`
	UserID    uint64    `gorm:"column:user_id;uniqueIndex:uniq_comment_user,priority:2" json:"user_id"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

func (CommentLike) TableName() string { return "comment_likes" }

// AppMessage 类型：
const (
	AppMessageTypeCommentReply = "comment_reply" // 有人回复了我的评论（楼中楼），一回复一条
	AppMessageTypeCommentLike  = "comment_like"  // 有人点赞了我的评论，按(收信人,评论)聚合成一条
)

// AppMessage —— APP 用户站内消息（消息页）。
// comment_reply：一回复一条；comment_like：按 (recipient_id, comment_id) 聚合，新点赞则更新触发者+置未读+顶上来。
// 展示字段（评论者/剧集封面/集数等）一律读时 join 出来，消息表只存路由与去重所需的最小信息。
type AppMessage struct {
	ID          uint64    `gorm:"primaryKey;column:id" json:"id"`
	RecipientID uint64    `gorm:"column:recipient_id;index:idx_app_msg_recipient,priority:1;not null" json:"recipient_id"` // 收信人=被回复/被点赞的评论作者
	Type        string    `gorm:"column:type;size:20;not null" json:"type"`
	CommentID   uint64    `gorm:"column:comment_id;index;not null" json:"comment_id"` // 被回复/被点赞的「我的评论」
	ReplyID     *uint64   `gorm:"column:reply_id" json:"reply_id"`                    // comment_reply 专用：新回复的评论 id
	ActorID     uint64    `gorm:"column:actor_id;not null" json:"actor_id"`           // 触发者（回复者 / 最近点赞者）
	IsRead      bool      `gorm:"column:is_read;default:false;index:idx_app_msg_recipient,priority:2" json:"is_read"`
	CreatedAt   time.Time `gorm:"column:created_at;index" json:"created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at;index" json:"updated_at"`
}

func (AppMessage) TableName() string { return "app_messages" }

// PlayHistory —— 观看历史（MVP 数据库设计 3.8）
// PlayHistory —— 观看历史。**一剧一条**：每个 (user, drama) 只保留一行，episode_id 记最近看到的那一集，
// 上报时 upsert 覆盖。唯一键 (user_id, drama_id) 由 ensureIndexes 手动建（含存量去重），不走 gorm 标签，
// 以便对存量「一集一条」数据安全迁移。
type PlayHistory struct {
	ID              uint64    `gorm:"primaryKey;column:id" json:"id"`
	UserID          uint64    `gorm:"column:user_id;index" json:"user_id"`
	DramaID         uint64    `gorm:"column:drama_id;index" json:"drama_id"`
	EpisodeID       uint64    `gorm:"column:episode_id" json:"episode_id"` // 最近观看的剧集
	ProgressSeconds int       `gorm:"column:progress_seconds;default:0" json:"progress_seconds"`
	UpdatedAt       time.Time `gorm:"column:updated_at" json:"updated_at"`
	CreatedAt       time.Time `gorm:"column:created_at" json:"created_at"`
}

func (PlayHistory) TableName() string { return "play_histories" }

// UserAction —— 点赞 / 收藏（MVP 数据库设计 3.9）。
// 对齐红果：点赞是**单集级**（episode_id 为该集 id），收藏是**整剧级**（episode_id=0）。
// 用 0 而非 NULL 占位，避免 Postgres 唯一索引对 NULL 不去重导致收藏可重复插入。
type UserAction struct {
	ID        uint64    `gorm:"primaryKey;column:id" json:"id"`
	UserID    uint64    `gorm:"column:user_id;uniqueIndex:uniq_user_drama_episode_action,priority:1" json:"user_id"`
	DramaID   uint64    `gorm:"column:drama_id;uniqueIndex:uniq_user_drama_episode_action,priority:2" json:"drama_id"`
	EpisodeID uint64    `gorm:"column:episode_id;default:0;uniqueIndex:uniq_user_drama_episode_action,priority:3" json:"episode_id"` // 0=剧级(收藏)，>0=集级(点赞)
	Action    string    `gorm:"column:action;size:20;uniqueIndex:uniq_user_drama_episode_action,priority:4" json:"action"`
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
	ID        uint64  `gorm:"primaryKey;column:id" json:"id"`
	OrderNo   string  `gorm:"column:order_no;size:64;uniqueIndex" json:"order_no"`
	UserID    uint64  `gorm:"column:user_id;index" json:"user_id"`
	ProductID *uint64 `gorm:"column:product_id" json:"product_id"`
	DramaID   uint64  `gorm:"column:drama_id;index" json:"drama_id"`
	EpisodeID uint64  `gorm:"column:episode_id;index" json:"episode_id"`
	// 选集购买：批量单在此存集 id 清单（一单多集）；单集单留空、仍用 episode_id。
	// serializer:json 落库为 json 文本；episode_unlocks 仍是解锁唯一真相源。
	EpisodeIDs      []uint64   `gorm:"column:episode_ids;serializer:json" json:"episode_ids,omitempty"`
	AmountCents     int64      `gorm:"column:amount_cents" json:"amount_cents"`
	PaymentMethod   string     `gorm:"column:payment_method;size:20" json:"payment_method"`
	PlatformTradeNo string     `gorm:"column:platform_trade_no;size:128;index" json:"platform_trade_no"`
	Status          string     `gorm:"column:status;size:20;default:pending;index" json:"status"`
	PaidAt          *time.Time `gorm:"column:paid_at" json:"paid_at"`
	ExpiredAt       *time.Time `gorm:"column:expired_at" json:"expired_at"`
	// 退款相关字段(2026-06 加):RefundAmountCents 累计已退金额(支持多次部分退);
	// RefundNo 是最近一次退款的商户单号,PlatformRefundNo 是渠道侧退款流水。
	RefundAmountCents int64      `gorm:"column:refund_amount_cents;default:0" json:"refund_amount_cents"`
	RefundedAt        *time.Time `gorm:"column:refunded_at" json:"refunded_at"`
	RefundReason      string     `gorm:"column:refund_reason;size:255" json:"refund_reason"`
	RefundNo          string     `gorm:"column:refund_no;size:64;index" json:"refund_no"`
	PlatformRefundNo  string     `gorm:"column:platform_refund_no;size:128" json:"platform_refund_no"`
	CreatedAt         time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt         time.Time  `gorm:"column:updated_at" json:"updated_at"`
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
	FileURL     string    `gorm:"column:file_url;size:512" json:"file_url"` // 系统生成的合同模板文件
	ScanFileKey string    `gorm:"column:scan_file_key;size:512" json:"-"`   // 管理员上传的签署后合同扫描件（PDF）私有 COS key；下载走鉴权后 presigned GET
	Status      string    `gorm:"column:status;size:20;default:pending" json:"status"`
	CreatedAt   time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (Contract) TableName() string { return "contracts" }

// Withdrawal —— 提现申请（MVP 数据库设计 3.15）
type Withdrawal struct {
	ID                 uint64  `gorm:"primaryKey;column:id" json:"id"`
	WithdrawalNo       string  `gorm:"column:withdrawal_no;size:64;uniqueIndex" json:"withdrawal_no"`
	CreatorID          uint64  `gorm:"column:creator_id;index" json:"creator_id"`
	DramaID            *uint64 `gorm:"column:drama_id;index" json:"drama_id"`
	AmountCents        int64   `gorm:"column:amount_cents" json:"amount_cents"`
	BankNameSnapshot   string  `gorm:"column:bank_name_snapshot;size:64" json:"bank_name_snapshot"`
	BankCardNoSnapshot string  `gorm:"column:bank_card_no_snapshot;size:64" json:"bank_card_no_snapshot"`
	Status             string  `gorm:"column:status;size:20;default:pending;index" json:"status"`
	// 个税：个人创作者按阶梯代扣，机构开票不扣。下列三项在申请时按当时配置快照。
	CreatorTypeSnapshot string     `gorm:"column:creator_type_snapshot;size:20" json:"creator_type_snapshot"`
	TransferType        string     `gorm:"column:transfer_type;size:20" json:"transfer_type"` // public=对公(机构) / private=对私(个人)，财务打款区分
	TaxCents            int64      `gorm:"column:tax_cents;default:0" json:"tax_cents"`       // 代扣个税
	NetCents            int64      `gorm:"column:net_cents;default:0" json:"net_cents"`       // 实际到账 = amount_cents - tax_cents
	Remark              string     `gorm:"column:remark;size:255" json:"remark"`
	TransactionNo       string     `gorm:"column:transaction_no;size:128" json:"transaction_no"`
	ReviewedBy          *uint64    `gorm:"column:reviewed_by" json:"reviewed_by"`
	ReviewedAt          *time.Time `gorm:"column:reviewed_at" json:"reviewed_at"`
	PaidAt              *time.Time `gorm:"column:paid_at" json:"paid_at"`
	// 2026-07 加：提现必须先上传对应结算单的发票（已审核通过）。可选：一笔提现可能跨多张结算单时为空。
	InvoiceID           *uint64    `gorm:"column:invoice_id;index" json:"invoice_id"`
	CreatedAt           time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt           time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (Withdrawal) TableName() string { return "withdrawals" }

// TaxBracket —— 个人创作者提现个税阶梯配置（速算扣除法）。
// 落地数字由财务提供，先建表与计算框架；空配置=不扣税。机构开票创作者不走此表。
// 计算：找到 min<=金额<max（max=0 表示无上限）的档，tax = round(金额×rate_bp/10000) - quick_deduct_cents，最低 0。
type TaxBracket struct {
	ID               uint64    `gorm:"primaryKey;column:id" json:"id"`
	MinCents         int64     `gorm:"column:min_cents;default:0" json:"min_cents"`                   // 区间下限（含）
	MaxCents         int64     `gorm:"column:max_cents;default:0" json:"max_cents"`                   // 区间上限（不含），0=无上限
	RateBP           int       `gorm:"column:rate_bp;default:0" json:"rate_bp"`                       // 税率基点（10000=100%）
	QuickDeductCents int64     `gorm:"column:quick_deduct_cents;default:0" json:"quick_deduct_cents"` // 速算扣除数
	SortOrder        int       `gorm:"column:sort_order;default:0" json:"sort_order"`
	Status           string    `gorm:"column:status;size:20;default:active;index" json:"status"`
	CreatedAt        time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt        time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (TaxBracket) TableName() string { return "tax_brackets" }

// === 结算单 & 发票（2026-07-01 创作者结算+发票功能）===

// Settlement 状态：
//   - draft      ：草稿，cron 自动生成中
//   - open       ：待上传发票（创作者可下载对账单、上传发票、发起提现）
//   - invoiced   ：发票已上传/审核中（提现可走，但等发票审核完才打款）
//   - paid       ：已打款（结算单生命周期结束）
//   - void       ：作废（财务手动关账）
// 数据源：CreatorStatsDaily（按月汇总 income_cents）+ ChannelIncomeDaily（第三方渠道）
const (
	SettlementStatusDraft    = "draft"
	SettlementStatusOpen     = "open"
	SettlementStatusInvoiced = "invoiced"
	SettlementStatusPaid     = "paid"
	SettlementStatusVoid     = "void"
)

// Settlement —— 创作者结算单（按月 × 合同 × 创作者 一条）。
// 一部结算单对应多个订单贡献（detail 见 settlement_items 表），
// 创作者自行开票并上传发票，平台审核通过后随提现一并打款。
type Settlement struct {
	ID            uint64     `gorm:"primaryKey;column:id" json:"id"`
	SettlementNo  string     `gorm:"column:settlement_no;size:64;uniqueIndex" json:"settlement_no"` // 业务编号 ST202607-0001
	CreatorID     uint64     `gorm:"column:creator_id;index" json:"creator_id"`
	ContractNo    string     `gorm:"column:contract_no;size:64;index" json:"contract_no"`        // 关联合同编号
	Period        string     `gorm:"column:period;size:16;index" json:"period"`                  // "2026-05" / "2026-05-R3"
	// 2026-07-06 加：半月结算唯一键
	// 2026-07-03 群（吴建棉）：提现改为半月度，每月 15 号 + 月末各结算一次
	// 旧月度数据（cycle_key=""）保留——不强制迁移，新半月度数据用 2026-07-H1 / 2026-07-H2 格式
	CycleKey string `gorm:"column:cycle_key;size:16;index" json:"cycle_key"`
	// 2026-07-06 加：实际结算区间展示文案 "2026-07-16 ~ 2026-07-31"
	// 区别于 Period（自然月 YYYY-MM），PeriodRange 是真实起止日期
	PeriodRange string `gorm:"column:period_range;size:64" json:"period_range"`
	GrossCents    int64      `gorm:"column:gross_cents;default:0" json:"gross_cents"`            // 订单总流水
	PlatformCents int64      `gorm:"column:platform_cents;default:0" json:"platform_cents"`      // 平台抽成
	NetCents      int64      `gorm:"column:net_cents;default:0" json:"net_cents"`                // 创作者净收入
	Status        string     `gorm:"column:status;size:20;default:open;index" json:"status"`
	OpenedAt      *time.Time `gorm:"column:opened_at" json:"opened_at"`      // 进入 open 状态的时间
	ClosedAt      *time.Time `gorm:"column:closed_at" json:"closed_at"`      // paid / void 时间
	Remark        string     `gorm:"column:remark;size:255" json:"remark"`
	CreatedAt     time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (Settlement) TableName() string { return "settlements" }

// SettlementItem —— 结算单-订单关联（一张结算单可包含多个订单）。
// 冗余 AmountCents 字段：避免改 Order 后追溯历史结算单金额变动。
type SettlementItem struct {
	ID            uint64    `gorm:"primaryKey;column:id" json:"id"`
	SettlementID  uint64    `gorm:"column:settlement_id;uniqueIndex:uniq_settle_order,priority:1;index" json:"settlement_id"`
	OrderID       uint64    `gorm:"column:order_id;uniqueIndex:uniq_settle_order,priority:2" json:"order_id"`
	DramaID       uint64    `gorm:"column:drama_id;index" json:"drama_id"`
	Source        string    `gorm:"column:source;size:32" json:"source"`        // self / channel_xxx
	AmountCents   int64     `gorm:"column:amount_cents;default:0" json:"amount_cents"` // 创作者分成实得（冗余）
	OrderNo       string    `gorm:"column:order_no;size:64" json:"order_no"`
	PaidAt        *time.Time `gorm:"column:paid_at" json:"paid_at"`
	CreatedAt     time.Time  `gorm:"column:created_at" json:"created_at"`
}

func (SettlementItem) TableName() string { return "settlement_items" }

// Invoice 状态：
//   - pending    ：已上传，待 Admin 审核
//   - approved   ：审核通过（创作者可继续走提现）
//   - rejected   ：审核驳回（创作者可重传）
// 驳回后可重传：旧 invoice 状态置 rejected，不删；上传新发票创建新 invoice 记录。
const (
	InvoiceStatusPending  = "pending"
	InvoiceStatusApproved = "approved"
	InvoiceStatusRejected = "rejected"
)

// InvoiceType 发票类型（增值税专用 / 增值税普通 / 电子专用 / 电子普通）
const (
	InvoiceTypeVATSpecial   = "vat_special"   // 增值税专用发票
	InvoiceTypeVATGeneral   = "vat_general"   // 增值税普通发票
	InvoiceTypeEVATSpecial  = "evat_special"  // 增值税电子专用发票
	InvoiceTypeEVATGeneral  = "evat_general"  // 增值税电子普通发票
)

// Invoice —— 创作者上传的发票。
// 一个结算单可对应多张发票（创作者可分次上传），approved 后的发票金额加和
// 应 >= 结算单 NetCents 才能走提现。
type Invoice struct {
	ID            uint64     `gorm:"primaryKey;column:id" json:"id"`
	InvoiceNo     string     `gorm:"column:invoice_no;size:64" json:"invoice_no"`              // 业务编号 INV202607-0001
	SettlementID  uint64     `gorm:"column:settlement_id;index" json:"settlement_id"`
	CreatorID     uint64     `gorm:"column:creator_id;index" json:"creator_id"`
	InvoiceType   string     `gorm:"column:invoice_type;size:32" json:"invoice_type"`           // vat_special / vat_general / evat_special / evat_general
	ExternalNo    string     `gorm:"column:external_no;size:128" json:"external_no"`           // 发票号（创作者手填，可事后补）
	AmountCents   int64      `gorm:"column:amount_cents;default:0" json:"amount_cents"`
	FileURL       string     `gorm:"column:file_url;size:512" json:"file_url"`                 // COS 路径
	FileHash      string     `gorm:"column:file_hash;size:128" json:"file_hash"`               // sha256 防重
	FileSize      int64      `gorm:"column:file_size;default:0" json:"file_size"`
	Status        string     `gorm:"column:status;size:20;default:pending;index" json:"status"`
	RejectReason  string     `gorm:"column:reject_reason;size:255" json:"reject_reason"`
	ReviewedBy    *uint64    `gorm:"column:reviewed_by" json:"reviewed_by"`
	ReviewedAt    *time.Time `gorm:"column:reviewed_at" json:"reviewed_at"`
	CreatedAt     time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (Invoice) TableName() string { return "invoices"}

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

// 2026-07-06 加：状态变迁事件表（事件溯源）
// 记录结算单/发票/提现单等关键实体的状态变化，支撑"时间线按天回看"功能。
// 只从今天起开始记——老数据回看只能回看到表里最早的 transition 时间。
//
// 实体类型 entity_type：
//   - "settlement"  : 结算单（对应 settlements.id）
//   - "invoice"     : 发票（对应 invoices.id）
//   - "withdrawal"  : 提现申请（对应 withdrawals.id）
//
// actor_type 触发方：
//   - "system"  : cron 任务 / 定时任务自动触发
//   - "creator" : 创作者触发（如提交提现）
//   - "admin"   : 管理员/财务触发（如审核/打款/驳回）
type StateTransition struct {
	ID         uint64    `gorm:"primaryKey;column:id" json:"id"`
	EntityType string    `gorm:"column:entity_type;size:32;index" json:"entity_type"`
	EntityID   uint64    `gorm:"column:entity_id;index" json:"entity_id"`
	FromStatus string    `gorm:"column:from_status;size:20" json:"from_status"`
	ToStatus   string    `gorm:"column:to_status;size:20" json:"to_status"`
	ActorType  string    `gorm:"column:actor_type;size:16" json:"actor_type"`
	ActorID    *uint64   `gorm:"column:actor_id" json:"actor_id"`
	Reason     string    `gorm:"column:reason;size:255" json:"reason"`
	// 2026-07-06 改：用 *string 指针——GORM nil 写 NULL（jsonb 列不接受空字符串）
	Metadata   *string   `gorm:"column:metadata;type:jsonb" json:"metadata"` // 额外上下文（金额变化 / 关联 invoice_ids 等）
	CreatedAt  time.Time `gorm:"column:created_at;index" json:"created_at"`
}

func (StateTransition) TableName() string { return "state_transitions" }

// ChannelIncomeDaily —— 第三方渠道每日收益明细（财务 Excel 导入）。
// 本平台自有付费收入走支付分账写 creator_stats_daily，不进此表。
// 唯一键 (drama_id, channel, stat_date)：同剧同渠道同日重复导入按覆盖处理。
type ChannelIncomeDaily struct {
	ID           uint64    `gorm:"primaryKey;column:id" json:"id"`
	DramaID      uint64    `gorm:"column:drama_id;uniqueIndex:uniq_channel_income,priority:1" json:"drama_id"`
	Channel      string    `gorm:"column:channel;size:32;uniqueIndex:uniq_channel_income,priority:2" json:"channel"`
	StatDate     string    `gorm:"column:stat_date;size:10;uniqueIndex:uniq_channel_income,priority:3" json:"stat_date"`
	CreatorID    uint64    `gorm:"column:creator_id;index" json:"creator_id"`
	GrossCents   int64     `gorm:"column:gross_cents;default:0" json:"gross_cents"`       // 渠道总收益（财务填）
	ShareRatioBP int       `gorm:"column:share_ratio_bp;default:0" json:"share_ratio_bp"` // 创作者分成比例，基点(10000=100%)
	IncomeCents  int64     `gorm:"column:income_cents;default:0" json:"income_cents"`     // 创作者实得 = 总收益×比例，入账金额
	BatchNo      string    `gorm:"column:batch_no;size:32;index" json:"batch_no"`
	ImportRowNo  int       `gorm:"column:import_row_no;default:0" json:"import_row_no"`
	CreatedAt    time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at" json:"updated_at"`
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
	ConfigKeyAIGCTools    = "aigc.tools"            // 可选 AIGC 创作工具列表（JSON 数组字符串）
	ConfigKeyHotSearch    = "search.hot_keywords"   // 搜索框推荐/热搜词（JSON 数组字符串）
	// 渠道收益分成比例（基点，10000=100%）。按渠道分开配置，键 = 前缀 + 渠道名；
	// ConfigKeyIncomeShareDefault 为兜底默认值。Excel 行内填了比例则以行内为准。
	ConfigKeyIncomeSharePrefix  = "income.share_ratio."        // + 渠道名
	ConfigKeyIncomeShareDefault = "income.share_ratio.default" // 默认分成比例（基点）
)

// 分成比例基点常量。
const (
	ShareRatioBPFull = 10000 // 100%
)

// ============================================================
// 0.15.0 发行商（Distributor）模块
// ============================================================

// 发行商认证状态（复用 Creator 同口径值）
const (
	DistributorVerifyUnverified = "unverified"
	DistributorVerifyPending    = "pending"
	DistributorVerifyVerified   = "verified"
	DistributorVerifyRejected   = "rejected"
)

// 发行平台
const (
	PlatformDouyin      = "douyin"
	PlatformKuaishou    = "kuaishou"
	PlatformWechatVideo = "wechat_video"
	PlatformBilibili    = "bilibili"
)

// 领剧申请状态
const (
	DistAppPending    = "pending"    // 待 admin 审核
	DistAppApproved   = "approved"   // admin 审核通过，待授权
	DistAppAuthorized = "authorized" // 已授权，可发行
	DistAppRejected   = "rejected"   // 驳回，保证金释放
	DistAppWithdrawn  = "withdrawn"  // 发行商主动撤回
)

// 授权状态
const (
	DistDramaAuthorized = "authorized" // 已授权
	DistDramaActive     = "active"     // 活跃发行中
	DistDramaRevoked    = "revoked"    // 已撤销
	DistDramaExpired    = "expired"    // 已过期
)

// 保证金状态
const (
	DepositFrozen    = "frozen"    // 冻结中
	DepositReleased  = "released"  // 已释放（复活）
	DepositForfeited = "forfeited" // 已没收（违约）
)

// 合同付款状态
const (
	ContractPayUnpaid  = "unpaid"
	ContractPayPartial = "partial"
	ContractPayPaid    = "paid"
	ContractPayOverdue = "overdue"
)

// 保证金配置键
const (
	ConfigKeyDepositBaseCents       = "deposit.base_cents"        // 基础保证金（分），默认 40000
	ConfigKeyDepositEpisodeThreshold = "deposit.episode_threshold" // 集数阈值，默认 50
	ConfigKeyDepositEpisodeAmount    = "deposit.episode_amount"    // 阈值以上保证金（分），默认 50000
	ConfigKeyDepositPlatformRateBP   = "deposit.platform_rate_bp"  // 每增一个平台加价（基点），默认 1500=15%
)

// 短信场景
const (
	SMSSceneDistributorLogin = "distributor_login"
)

// Distributor —— 发行商账号表
type Distributor struct {
	ID                   uint64    `gorm:"primaryKey;column:id" json:"id"`
	Phone                string    `gorm:"column:phone;size:20;uniqueIndex" json:"phone"`
	Name                 string    `gorm:"column:name;size:64" json:"name"`
	Nickname             string    `gorm:"column:nickname;size:64" json:"nickname"`
	AvatarURL            string    `gorm:"column:avatar_url;size:512" json:"avatar_url"`
	// 企业认证字段（与 Creator 企业认证对齐）
	OrgName              string    `gorm:"column:org_name;size:128" json:"org_name"`
	OrgCreditCode        string    `gorm:"column:org_credit_code;size:32;index" json:"org_credit_code"`
	OrgLegalPerson       string    `gorm:"column:org_legal_person;size:64" json:"org_legal_person"`
	OrgLegalIDCardEnc    string    `gorm:"column:org_legal_id_card_enc;type:text" json:"-"`
	OrgLegalIDCardMasked string    `gorm:"column:org_legal_id_card_masked;size:32" json:"org_legal_id_card_masked"`
	BusinessLicenseURL   string    `gorm:"column:business_license_url;size:512" json:"business_license_url"`
	BankLicenseURL       string    `gorm:"column:bank_license_url;size:512" json:"bank_license_url"`
	BankName             string    `gorm:"column:bank_name;size:64" json:"bank_name"`
	BankBranch           string    `gorm:"column:bank_branch;size:128" json:"bank_branch"`
	BankCardNoEnc        string    `gorm:"column:bank_card_no_enc;type:text" json:"-"`
	BankCardLast4        string    `gorm:"column:bank_card_last4;size:4" json:"-"`
	BankCardNoMasked     string    `gorm:"column:bank_card_no_masked;size:32" json:"bank_card_no_masked"`
	// 认证状态
	VerifyStatus         string    `gorm:"column:verify_status;size:20;default:unverified;index" json:"verify_status"`
	VerifyRejectReason   string    `gorm:"column:verify_reject_reason;size:255" json:"verify_reject_reason"`
	VerifyRejectFields   string    `gorm:"column:verify_reject_fields;size:255" json:"verify_reject_fields"`
	VerifySubmittedAt    *time.Time `gorm:"column:verify_submitted_at" json:"verify_submitted_at"`
	VerifyMethod         string    `gorm:"column:verify_method;size:20" json:"verify_method"`
	VerifyProviderResult string    `gorm:"column:verify_provider_result;type:text" json:"-"`
	VerifyCheckedAt      *time.Time `gorm:"column:verify_checked_at" json:"verify_checked_at"`
	// 保证金钱包
	DepositAvailableCents int64     `gorm:"column:deposit_available_cents;default:0" json:"deposit_available_cents"`
	DepositFrozenCents    int64     `gorm:"column:deposit_frozen_cents;default:0" json:"deposit_frozen_cents"`
	// 收益钱包
	TotalIncomeCents      int64     `gorm:"column:total_income_cents;default:0" json:"total_income_cents"`
	BalanceCents          int64     `gorm:"column:balance_cents;default:0" json:"balance_cents"`
	FrozenCents           int64     `gorm:"column:frozen_cents;default:0" json:"frozen_cents"`
	// 账号状态
	Status                string    `gorm:"column:status;size:20;default:active;index" json:"status"`
	CreatedAt             time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt             time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (Distributor) TableName() string { return "distributors" }

// DistributorRecharge —— 保证金充值订单表
type DistributorRecharge struct {
	ID              uint64     `gorm:"primaryKey;column:id" json:"id"`
	RechargeNo      string     `gorm:"column:recharge_no;size:32;uniqueIndex" json:"recharge_no"`
	DistributorID   uint64     `gorm:"column:distributor_id;index" json:"distributor_id"`
	AmountCents     int64      `gorm:"column:amount_cents" json:"amount_cents"`
	PaymentMethod   string     `gorm:"column:payment_method;size:20" json:"payment_method"`
	PlatformTradeNo string     `gorm:"column:platform_trade_no;size:128;index" json:"platform_trade_no"`
	Status          string     `gorm:"column:status;size:20;default:pending;index" json:"status"`
	PaidAt          *time.Time `gorm:"column:paid_at" json:"paid_at"`
	ExpiredAt       *time.Time `gorm:"column:expired_at" json:"expired_at"`
	Remark          string     `gorm:"column:remark;size:255" json:"remark"`
	CreatedAt       time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (DistributorRecharge) TableName() string { return "distributor_recharges" }

// DistributorApplication —— 领剧申请表
type DistributorApplication struct {
	ID                 uint64     `gorm:"primaryKey;column:id" json:"id"`
	ApplicationNo      string     `gorm:"column:application_no;size:32;uniqueIndex" json:"application_no"`
	DistributorID      uint64     `gorm:"column:distributor_id;index" json:"distributor_id"`
	DramaID            uint64     `gorm:"column:drama_id;index" json:"drama_id"`
	Platforms          string     `gorm:"column:platforms;type:text" json:"platforms"` // JSON 数组 ["douyin","kuaishou"]
	DepositAmountCents int64      `gorm:"column:deposit_amount_cents" json:"deposit_amount_cents"`
	Status             string     `gorm:"column:status;size:20;default:pending;index" json:"status"`
	RejectReason       string     `gorm:"column:reject_reason;size:255" json:"reject_reason"`
	ReviewedBy         *uint64    `gorm:"column:reviewed_by" json:"reviewed_by"`
	ReviewedAt         *time.Time `gorm:"column:reviewed_at" json:"reviewed_at"`
	AuthorizedAt       *time.Time `gorm:"column:authorized_at" json:"authorized_at"`
	CreatedAt          time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt          time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (DistributorApplication) TableName() string { return "distributor_applications" }

// DistributorDrama —— 领剧授权关联表（已授权发行的剧）
type DistributorDrama struct {
	ID                 uint64     `gorm:"primaryKey;column:id" json:"id"`
	DistributorID      uint64     `gorm:"column:distributor_id;index:idx_dist_drama,priority:1" json:"distributor_id"`
	DramaID            uint64     `gorm:"column:drama_id;index:idx_dist_drama,priority:2" json:"drama_id"`
	ApplicationID      uint64     `gorm:"column:application_id" json:"application_id"`
	Platforms          string     `gorm:"column:platforms;type:text" json:"platforms"` // JSON 数组
	Status             string     `gorm:"column:status;size:20;default:authorized;index" json:"status"`
	AuthorizedAt       *time.Time `gorm:"column:authorized_at" json:"authorized_at"`
	ExpiresAt          *time.Time `gorm:"column:expires_at" json:"expires_at"`
	DepositAmountCents int64      `gorm:"column:deposit_amount_cents" json:"deposit_amount_cents"`
	DepositStatus      string     `gorm:"column:deposit_status;size:20;default:frozen" json:"deposit_status"`
	ReleasedAt         *time.Time `gorm:"column:released_at" json:"released_at"`
	ContractID         *uint64    `gorm:"column:contract_id" json:"contract_id"`
	CreatedAt          time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt          time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (DistributorDrama) TableName() string { return "distributor_dramas" }

// DistributorContract —— 发行合同表
type DistributorContract struct {
	ID                 uint64     `gorm:"primaryKey;column:id" json:"id"`
	DistributorID      uint64     `gorm:"column:distributor_id;index" json:"distributor_id"`
	DramaID            uint64     `gorm:"column:drama_id;index" json:"drama_id"`
	DistributorDramaID uint64     `gorm:"column:distributor_drama_id" json:"distributor_drama_id"`
	ContractNo         string     `gorm:"column:contract_no;size:32;index" json:"contract_no"`
	EsignFlowID        string     `gorm:"column:esign_flow_id;size:128" json:"esign_flow_id"`
	FileURL            string     `gorm:"column:file_url;size:512" json:"file_url"`
	AmountCents        int64      `gorm:"column:amount_cents" json:"amount_cents"`
	PaidCents          int64      `gorm:"column:paid_cents;default:0" json:"paid_cents"`
	PaidAt             *time.Time `gorm:"column:paid_at" json:"paid_at"`
	PenaltyCents       int64      `gorm:"column:penalty_cents;default:0" json:"penalty_cents"`
	PaymentStatus      string     `gorm:"column:payment_status;size:20;default:unpaid;index" json:"payment_status"`
	FinanceConfirmedBy *uint64    `gorm:"column:finance_confirmed_by" json:"finance_confirmed_by"`
	FinanceConfirmedAt *time.Time `gorm:"column:finance_confirmed_at" json:"finance_confirmed_at"`
	Status             string     `gorm:"column:status;size:20;default:pending;index" json:"status"`
	CreatedAt          time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt          time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (DistributorContract) TableName() string { return "distributor_contracts" }

// DistributorIncomeDaily —— 发行商每日收益明细（财务导入）
type DistributorIncomeDaily struct {
	ID            uint64    `gorm:"primaryKey;column:id" json:"id"`
	DistributorID uint64    `gorm:"column:distributor_id;index:idx_dist_income,priority:1" json:"distributor_id"`
	DramaID       uint64    `gorm:"column:drama_id;index:idx_dist_income,priority:2" json:"drama_id"`
	Platform      string    `gorm:"column:platform;size:32;index:idx_dist_income,priority:3" json:"platform"`
	StatDate      string    `gorm:"column:stat_date;size:10;index:idx_dist_income,priority:4" json:"stat_date"`
	GrossCents    int64     `gorm:"column:gross_cents" json:"gross_cents"`
	ShareRatioBP  int       `gorm:"column:share_ratio_bp" json:"share_ratio_bp"` // 5500 = 55%
	IncomeCents   int64     `gorm:"column:income_cents" json:"income_cents"`     // 机构实得
	BatchNo       string    `gorm:"column:batch_no;size:32;index" json:"batch_no"`
	ImportRowNo   int       `gorm:"column:import_row_no" json:"import_row_no"`
	CreatedAt     time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (DistributorIncomeDaily) TableName() string { return "distributor_income_daily" }

// DistributorSettlement —— 发行商结算单
type DistributorSettlement struct {
	ID            uint64     `gorm:"primaryKey;column:id" json:"id"`
	SettlementNo  string     `gorm:"column:settlement_no;size:32;uniqueIndex" json:"settlement_no"`
	DistributorID uint64     `gorm:"column:distributor_id;index" json:"distributor_id"`
	ContractNo    string     `gorm:"column:contract_no;size:32;index" json:"contract_no"`
	Period        string     `gorm:"column:period;size:10" json:"period"`
	CycleKey      string     `gorm:"column:cycle_key;size:16;index" json:"cycle_key"`
	PeriodRange   string     `gorm:"column:period_range;size:64" json:"period_range"`
	GrossCents    int64      `gorm:"column:gross_cents" json:"gross_cents"`
	PlatformCents int64      `gorm:"column:platform_cents" json:"platform_cents"` // 平台 45%
	NetCents      int64      `gorm:"column:net_cents" json:"net_cents"`           // 机构 55%
	Status        string     `gorm:"column:status;size:20;default:draft;index" json:"status"`
	OpenedAt      *time.Time `gorm:"column:opened_at" json:"opened_at"`
	ClosedAt      *time.Time `gorm:"column:closed_at" json:"closed_at"`
	Remark        string     `gorm:"column:remark;size:255" json:"remark"`
	CreatedAt     time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (DistributorSettlement) TableName() string { return "distributor_settlements" }

// DistributorWithdrawal —— 发行商提现申请
type DistributorWithdrawal struct {
	ID                uint64     `gorm:"primaryKey;column:id" json:"id"`
	WithdrawalNo      string     `gorm:"column:withdrawal_no;size:32;uniqueIndex" json:"withdrawal_no"`
	DistributorID     uint64     `gorm:"column:distributor_id;index" json:"distributor_id"`
	SettlementID      uint64     `gorm:"column:settlement_id;index" json:"settlement_id"`
	AmountCents       int64      `gorm:"column:amount_cents" json:"amount_cents"`
	BankNameSnapshot  string     `gorm:"column:bank_name_snapshot;size:64" json:"bank_name_snapshot"`
	BankCardNoSnapshot string    `gorm:"column:bank_card_no_snapshot;size:64" json:"bank_card_no_snapshot"`
	Status            string     `gorm:"column:status;size:20;default:pending;index" json:"status"`
	InvoiceID         *uint64    `gorm:"column:invoice_id;index" json:"invoice_id"`
	TransactionNo     string     `gorm:"column:transaction_no;size:128" json:"transaction_no"`
	ReviewedBy        *uint64    `gorm:"column:reviewed_by" json:"reviewed_by"`
	ReviewedAt        *time.Time `gorm:"column:reviewed_at" json:"reviewed_at"`
	PaidAt            *time.Time `gorm:"column:paid_at" json:"paid_at"`
	Remark            string     `gorm:"column:remark;size:255" json:"remark"`
	CreatedAt         time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt         time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (DistributorWithdrawal) TableName() string { return "distributor_withdrawals" }

// DistributorInvoice —— 发行商发票
type DistributorInvoice struct {
	ID           uint64     `gorm:"primaryKey;column:id" json:"id"`
	InvoiceNo    string     `gorm:"column:invoice_no;size:32;uniqueIndex" json:"invoice_no"`
	SettlementID uint64     `gorm:"column:settlement_id;index" json:"settlement_id"`
	DistributorID uint64    `gorm:"column:distributor_id;index" json:"distributor_id"`
	InvoiceType  string     `gorm:"column:invoice_type;size:20" json:"invoice_type"`
	ExternalNo   string     `gorm:"column:external_no;size:64" json:"external_no"`
	AmountCents  int64      `gorm:"column:amount_cents" json:"amount_cents"`
	FileURL      string     `gorm:"column:file_url;size:512" json:"file_url"`
	FileHash     string     `gorm:"column:file_hash;size:64" json:"file_hash"`
	FileSize     int64      `gorm:"column:file_size" json:"file_size"`
	Status       string     `gorm:"column:status;size:20;default:pending;index" json:"status"`
	RejectReason string     `gorm:"column:reject_reason;size:255" json:"reject_reason"`
	ReviewedBy   *uint64    `gorm:"column:reviewed_by" json:"reviewed_by"`
	ReviewedAt   *time.Time `gorm:"column:reviewed_at" json:"reviewed_at"`
	CreatedAt    time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt    time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (DistributorInvoice) TableName() string { return "distributor_invoices" }
