package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/configcheck"
	"ai-drama-platform/internal/database"
	"ai-drama-platform/internal/handler"

	"github.com/cloudflare/tableflip"
)

func main() {
	cfg := config.Load()

	// 启动配置体检：把所有问题打到日志便于排查；其中 JWT_SECRET 为空/弱默认值时
	// 直接拒绝启动——否则任何人都能用已知 dev 密钥伪造管理员/任意用户 token。
	for _, issue := range configcheck.Run(cfg, configcheck.Options{Prod: true}).Issues {
		log.Printf("[configcheck] %s %s: %s", issue.Severity, issue.Code, issue.Message)
	}
	if cfg.JWTSecret == "" || cfg.JWTSecret == "dev-secret-change-me" || cfg.JWTSecret == "dev-secret" {
		log.Fatalf("[configcheck] JWT_SECRET 未配置或为弱默认值，拒绝启动：请在 .env 设置足够随机的 JWT_SECRET")
	}

	db, err := database.Connect(cfg)
	if err != nil {
		log.Fatalf("connect database: %v", err)
	}

	server := handler.New(db, cfg)

	// PIDFile 默认 /run/drama-api/pid，可由 PID_FILE 环境变量覆盖。
	// 跑隔离的第二实例（如沙箱联调）时指向不同路径，避免与生产抢同一 PIDFile；
	// tableflip 父子协调走 socketpair(fd)+env 哨兵、不依赖 PIDFile，故不同 PIDFile 即完全隔离。
	pidFilePath := os.Getenv("PID_FILE")
	if pidFilePath == "" {
		pidFilePath = "/run/drama-api/pid"
	}

	// tableflip 接管 listener：SIGHUP 触发零停机升级。Ready 时把当前 PID 写入 PIDFile，
	// systemd 通过 PIDFile= 重新跟踪 MainPID，避免老进程退出被误判为服务挂掉。
	upg, err := tableflip.New(tableflip.Options{
		UpgradeTimeout: 30 * time.Second,
		PIDFile:        pidFilePath,
	})
	if err != nil {
		log.Fatalf("tableflip new: %v", err)
	}
	defer upg.Stop()

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGHUP)
		for range sig {
			log.Printf("SIGHUP received, upgrading")
			if err := upg.Upgrade(); err != nil {
				log.Printf("upgrade failed: %v", err)
			}
		}
	}()

	ln, err := upg.Listen("tcp", cfg.Addr)
	if err != nil {
		log.Fatalf("listen on %s: %v", cfg.Addr, err)
	}

	// HTTP 超时：默认无超时会被慢速客户端(slowloris)拖住连接耗尽资源。
	// 大文件(视频/图片)走腾讯云直传不经本服务，经本服务最大是 10MB xlsx，故 30s 读足够。
	httpServer := &http.Server{
		Handler:           server.Router(),
		ReadHeaderTimeout: 10 * time.Second,  // 防 slowloris 慢发头
		ReadTimeout:       30 * time.Second,  // 含 body
		WriteTimeout:      120 * time.Second, // 留足导入/导出等较慢的管理操作
		IdleTimeout:       120 * time.Second, // keep-alive 空闲回收
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server.StartBackground(ctx)

	errCh := make(chan error, 1)
	go func() {
		log.Printf("api server listening on %s (pid=%d)", cfg.Addr, os.Getpid())
		errCh <- httpServer.Serve(ln)
	}()

	if err := upg.Ready(); err != nil {
		log.Fatalf("tableflip ready: %v", err)
	}

	select {
	case <-ctx.Done():
		log.Printf("shutdown signal received")
	case <-upg.Exit():
		log.Printf("upgrade complete, old process exiting")
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
