package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"

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

	httpServer := &http.Server{
		Addr:    cfg.Addr,
		Handler: server.Router(),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server.StartBackground(ctx)

	errCh := make(chan error, 1)
	go func() {
		log.Printf("api server listening on %s", cfg.Addr)
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		log.Printf("shutdown signal received")
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("run api server: %v", err)
		}
		return
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("shutdown api server: %v", err)
	}
	log.Printf("api server stopped gracefully")
}
