package main

import (
	"log"

	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/database"
	"ai-drama-platform/internal/handler"
)

func main() {
	cfg := config.Load()
	db, err := database.Connect(cfg)
	if err != nil {
		log.Fatalf("connect database: %v", err)
	}

	server := handler.New(db, cfg)
	if err := server.Router().Run(cfg.Addr); err != nil {
		log.Fatalf("run api server: %v", err)
	}
}
