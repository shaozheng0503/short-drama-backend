package database

import (
	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/model"

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
		&model.CreatorProfile{},
		&model.Drama{},
		&model.Episode{},
		&model.UserDramaAction{},
		&model.Comment{},
		&model.WatchHistory{},
		&model.CheckIn{},
		&model.Order{},
		&model.Notification{},
		&model.Contract{},
		&model.Withdrawal{},
		&model.RevenueDaily{},
	); err != nil {
		return nil, err
	}
	return db, nil
}
