package model

import (
	"time"

	"gorm.io/gorm"
)

type Role string

const (
	RoleUser    Role = "user"
	RoleCreator Role = "creator"
	RoleAdmin   Role = "admin"
)

type User struct {
	ID           uint           `gorm:"primaryKey" json:"id"`
	Phone        string         `gorm:"uniqueIndex;size:32" json:"phone"`
	PasswordHash string         `json:"-"`
	Nickname     string         `gorm:"size:64" json:"nickname"`
	AvatarURL    string         `gorm:"size:512" json:"avatar_url"`
	Role         Role           `gorm:"size:16;index" json:"role"`
	Points       int64          `json:"points"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}

type CreatorProfile struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	UserID         uint      `gorm:"uniqueIndex" json:"user_id"`
	User           User      `json:"user"`
	RealName       string    `gorm:"size:64" json:"real_name"`
	IDCardNo       string    `gorm:"size:32" json:"id_card_no"`
	IDCardImageURL string    `gorm:"size:512" json:"id_card_image_url"`
	BankAccount    string    `gorm:"size:64" json:"bank_account"`
	BankName       string    `gorm:"size:128" json:"bank_name"`
	VerifiedStatus string    `gorm:"size:32;default:pending" json:"verified_status"`
	PublisherName  string    `gorm:"size:128" json:"publisher_name"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Drama struct {
	ID          uint           `gorm:"primaryKey" json:"id"`
	CreatorID   *uint          `gorm:"index" json:"creator_id"`
	Creator     *User          `json:"creator,omitempty"`
	Title       string         `gorm:"size:128;index" json:"title"`
	Description string         `gorm:"type:text" json:"description"`
	CoverURL    string         `gorm:"size:512" json:"cover_url"`
	Category    string         `gorm:"size:64;index" json:"category"`
	Region      string         `gorm:"size:32;index" json:"region"`
	Language    string         `gorm:"size:32;index" json:"language"`
	Tags        string         `gorm:"size:512" json:"tags"`
	Status      string         `gorm:"size:32;index;default:draft" json:"status"`
	IsPaid      bool           `json:"is_paid"`
	PriceCents  int64          `json:"price_cents"`
	ViewCount   int64          `json:"view_count"`
	LikeCount   int64          `json:"like_count"`
	FavCount    int64          `json:"fav_count"`
	ShareCount  int64          `json:"share_count"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
}

type Episode struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	DramaID       uint      `gorm:"index" json:"drama_id"`
	Title         string    `gorm:"size:128" json:"title"`
	EpisodeNo     int       `gorm:"index" json:"episode_no"`
	VideoURL      string    `gorm:"size:512" json:"video_url"`
	DurationSec   int       `json:"duration_sec"`
	IsFree        bool      `json:"is_free"`
	TranscodeStat string    `gorm:"size:32;default:ready" json:"transcode_status"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type UserDramaAction struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"uniqueIndex:idx_user_drama_action" json:"user_id"`
	DramaID   uint      `gorm:"uniqueIndex:idx_user_drama_action" json:"drama_id"`
	Action    string    `gorm:"uniqueIndex:idx_user_drama_action;size:32" json:"action"`
	CreatedAt time.Time `json:"created_at"`
}

type Comment struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index" json:"user_id"`
	User      User      `json:"user"`
	DramaID   uint      `gorm:"index" json:"drama_id"`
	Content   string    `gorm:"size:1000" json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type WatchHistory struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	UserID      uint      `gorm:"uniqueIndex:idx_user_episode" json:"user_id"`
	DramaID     uint      `gorm:"index" json:"drama_id"`
	EpisodeID   uint      `gorm:"uniqueIndex:idx_user_episode" json:"episode_id"`
	ProgressSec int       `json:"progress_sec"`
	UpdatedAt   time.Time `json:"updated_at"`
	CreatedAt   time.Time `json:"created_at"`
}

type CheckIn struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"uniqueIndex:idx_user_day" json:"user_id"`
	Day       string    `gorm:"uniqueIndex:idx_user_day;size:10" json:"day"`
	Points    int64     `json:"points"`
	CreatedAt time.Time `json:"created_at"`
}

type Order struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	UserID      uint      `gorm:"index" json:"user_id"`
	DramaID     *uint     `gorm:"index" json:"drama_id"`
	Channel     string    `gorm:"size:32" json:"channel"`
	AmountCents int64     `json:"amount_cents"`
	Status      string    `gorm:"size:32;index;default:pending" json:"status"`
	TradeNo     string    `gorm:"size:128;index" json:"trade_no"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Notification struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	UserID    uint       `gorm:"index" json:"user_id"`
	Title     string     `gorm:"size:128" json:"title"`
	Content   string     `gorm:"size:1000" json:"content"`
	ReadAt    *time.Time `json:"read_at"`
	CreatedAt time.Time  `json:"created_at"`
}

type Contract struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	CreatorID   uint      `gorm:"index" json:"creator_id"`
	DramaID     *uint     `gorm:"index" json:"drama_id"`
	Provider    string    `gorm:"size:64;default:tencent-ess" json:"provider"`
	ExternalID  string    `gorm:"size:128;index" json:"external_id"`
	Status      string    `gorm:"size:32;index;default:draft" json:"status"`
	DownloadURL string    `gorm:"size:512" json:"download_url"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Withdrawal struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	CreatorID   uint      `gorm:"index" json:"creator_id"`
	AmountCents int64     `json:"amount_cents"`
	BankAccount string    `gorm:"size:64" json:"bank_account"`
	Status      string    `gorm:"size:32;index;default:pending" json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type RevenueDaily struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	CreatorID   uint      `gorm:"index" json:"creator_id"`
	DramaID     uint      `gorm:"index" json:"drama_id"`
	Day         string    `gorm:"size:10;index" json:"day"`
	ViewCount   int64     `json:"view_count"`
	AmountCents int64     `json:"amount_cents"`
	CreatedAt   time.Time `json:"created_at"`
}
