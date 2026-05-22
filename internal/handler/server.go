package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"ai-drama-platform/internal/alert"
	"ai-drama-platform/internal/billing"
	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/cos"
	"ai-drama-platform/internal/idempotency"
	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/payment"
	"ai-drama-platform/internal/ratelimit"
	"ai-drama-platform/internal/redisclient"
	"ai-drama-platform/internal/secure"
	"ai-drama-platform/internal/sms"
	"ai-drama-platform/internal/vod"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type Server struct {
	db       *gorm.DB
	cfg      config.Config
	sms      *sms.Service
	payments *payment.Registry
	billing  *billing.Service
	cryptor  *secure.Cryptor
	idem     *idempotency.Service
	alerts   *alert.Client
	redis    *redis.Client
	cos      *cos.Signer
	vod      *vod.Signer
	started  time.Time
}

func New(db *gorm.DB, cfg config.Config) *Server {
	cryptor, err := secure.New(cfg.DataEncryptionKeyB64)
	if err != nil {
		if errors.Is(err, secure.ErrKeyMissing) {
			log.Printf("[secure] DATA_ENCRYPTION_KEY 未配置：创作者实名 / 银行卡资料接口将拒绝写入")
		} else {
			log.Printf("[secure] 加密初始化失败：%v", err)
		}
	}
	payments := payment.NewRegistry(cfg)
	rdb := redisclient.New(cfg)
	return &Server{
		db:       db,
		cfg:      cfg,
		sms:      sms.New(db, cfg),
		payments: payments,
		billing:  billing.New(db, cfg, payments),
		cryptor:  cryptor,
		idem:     idempotency.New(rdb, cfg.IdempotencyTTL),
		alerts:   alert.New(cfg),
		redis:    rdb,
		cos:      cos.New(cfg),
		vod:      vod.New(cfg),
		started:  time.Now(),
	}
}

// StartBackground 启动后台任务；ctx 取消时主动停止所有后台 ticker。
func (s *Server) StartBackground(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		log.Printf("[bg] background tasks started")
		for {
			select {
			case <-ctx.Done():
				log.Printf("[bg] background tasks stopped")
				return
			case now := <-ticker.C:
				s.closeExpiredOrders(now)
			}
		}
	}()
}

func (s *Server) closeExpiredOrders(now time.Time) {
	result, err := s.billing.CloseExpiredOrders(now)
	if err != nil {
		log.Printf("[bg] close expired orders err=%v", err)
		s.alerts.SendAsync(alert.Event{
			Level:   "error",
			Type:    "close_expired_orders_failed",
			Message: "关闭过期订单失败",
			Fields: map[string]interface{}{
				"error": err.Error(),
			},
		})
	} else if result.ClosedCount > 0 {
		log.Printf("[bg] closed %d expired orders oldest_expired_at=%v samples=%v",
			result.ClosedCount, result.OldestExpiredAt, result.SampleOrderNos)
		s.alerts.SendAsync(alert.Event{
			Level:   "warn",
			Type:    "expired_orders_closed",
			Message: "过期订单已关闭",
			Fields: map[string]interface{}{
				"closed_count":      result.ClosedCount,
				"oldest_expired_at": result.OldestExpiredAt,
				"sample_order_nos":  result.SampleOrderNos,
			},
		})
	}
}

func (s *Server) Router() *gin.Engine {
	r := gin.Default()
	r.Use(s.corsMiddleware())
	r.Use(ratelimit.New(s.cfg).Handler())
	r.GET("/health", s.health)
	r.GET("/ready", s.ready)

	v1 := r.Group("/v1")

	common := v1.Group("/common")
	common.POST("/sms/send", s.sendSMS)
	common.POST("/uploads/image-sign", s.commonImageUploadSign)

	// === APP ===
	app := v1.Group("/app")
	app.POST("/auth/login", s.appLogin)
	app.GET("/home", s.appHome)
	app.GET("/dramas", s.appListDramas)
	app.GET("/dramas/:id", s.appDramaDetail)
	app.GET("/dramas/:id/episodes", s.appListEpisodes)
	app.GET("/dramas/:id/comments", s.appListComments)
	app.GET("/search", s.appSearch)
	app.GET("/products", s.appListProducts)

	appAuth := app.Group("")
	appAuth.Use(middleware.RequireApp(s.cfg))
	appAuth.Use(s.requireActiveApp())
	appAuth.GET("/me", s.appMe)
	appAuth.PUT("/me", s.appUpdateMe)
	appAuth.GET("/me/favorites", s.appListFavorites)
	appAuth.GET("/episodes/:id/play", s.appPlayEpisode)
	appAuth.POST("/play-history", s.appUpsertPlayHistory)
	appAuth.GET("/play-history", s.appListPlayHistory)
	appAuth.POST("/dramas/:id/like", s.appLikeDrama)
	appAuth.DELETE("/dramas/:id/like", s.appUnlikeDrama)
	appAuth.POST("/dramas/:id/favorite", s.appFavoriteDrama)
	appAuth.DELETE("/dramas/:id/favorite", s.appUnfavoriteDrama)
	appAuth.POST("/dramas/:id/share", s.appShareDrama)
	appAuth.POST("/dramas/:id/comments", s.appCreateComment)
	appAuth.POST("/orders", s.idempotencyMiddleware("app"), s.appCreateOrder)
	appAuth.GET("/orders/:order_no", s.appGetOrder)
	appAuth.POST("/episodes/:id/unlock", s.appUnlockEpisode)

	// === Creator ===
	creator := v1.Group("/creator")
	creator.POST("/auth/login", s.creatorLogin)
	creatorAuth := creator.Group("")
	creatorAuth.Use(middleware.RequireCreator(s.cfg))
	creatorAuth.Use(s.requireActiveCreator())
	creatorAuth.GET("/me", s.creatorMe)
	creatorAuth.PUT("/me/profile", s.creatorUpdateProfile)
	creatorAuth.GET("/dashboard", s.creatorDashboard)
	creatorAuth.GET("/dramas", s.creatorListDramas)
	creatorAuth.GET("/dramas/:id/stats", s.creatorDramaStats)
	creatorAuth.GET("/income", s.creatorIncome)
	creatorAuth.POST("/withdrawals", s.idempotencyMiddleware("creator"), s.creatorCreateWithdrawal)
	creatorAuth.GET("/withdrawals", s.creatorListWithdrawals)
	creatorAuth.GET("/contracts", s.creatorListContracts)
	creatorAuth.GET("/contracts/:id", s.creatorGetContract)

	// === Admin ===
	admin := v1.Group("/admin")
	admin.POST("/auth/login", s.adminLogin)
	adminAuth := admin.Group("")
	adminAuth.Use(middleware.RequireAdmin(s.cfg))
	adminAuth.Use(s.requireActiveAdmin())
	adminAuth.Use(s.auditMiddleware())
	adminAuth.GET("/me", s.adminMe)
	adminAuth.GET("/dashboard", s.adminDashboard)

	adminAuth.GET("/categories", s.adminListCategories)
	adminAuth.POST("/categories", s.adminCreateCategory)
	adminAuth.PUT("/categories/:id", s.adminUpdateCategory)

	adminAuth.GET("/dramas", s.adminListDramas)
	adminAuth.POST("/dramas", s.adminCreateDrama)
	adminAuth.GET("/dramas/:id", s.adminGetDrama)
	adminAuth.PUT("/dramas/:id", s.adminUpdateDrama)
	adminAuth.POST("/dramas/:id/publish", s.adminPublishDrama)
	adminAuth.POST("/dramas/:id/offline", s.adminOfflineDrama)

	adminAuth.GET("/dramas/:id/episodes", s.adminListEpisodes)
	adminAuth.POST("/dramas/:id/episodes", s.adminCreateEpisode)
	adminAuth.PUT("/episodes/:id", s.adminUpdateEpisode)

	adminAuth.POST("/uploads/vod-sign", s.adminVODUploadSign)

	adminAuth.GET("/creators", s.adminListCreators)
	adminAuth.POST("/creators", s.adminCreateCreator)
	adminAuth.GET("/creators/:id", s.adminGetCreator)
	adminAuth.PUT("/creators/:id", s.adminUpdateCreator)
	adminAuth.POST("/creators/:id/ban", s.adminBanCreator)

	adminAuth.GET("/orders", s.adminListOrders)
	adminAuth.GET("/orders/:order_no", s.adminGetOrder)

	adminAuth.GET("/withdrawals", s.adminListWithdrawals)
	adminAuth.POST("/withdrawals/:id/approve", s.adminApproveWithdrawal)
	adminAuth.POST("/withdrawals/:id/reject", s.adminRejectWithdrawal)
	adminAuth.POST("/withdrawals/:id/mark-paid", s.adminMarkWithdrawalPaid)

	adminAuth.GET("/contracts", s.adminListContracts)
	adminAuth.POST("/contracts", s.adminCreateContract)
	adminAuth.GET("/contracts/:id", s.adminGetContract)
	adminAuth.POST("/contracts/:id/esign", s.adminEsignContract)

	// === Webhooks（公开，由 provider 自己验签）===
	webhooks := v1.Group("/webhooks")
	webhooks.POST("/wechat/pay", s.webhookWechatPay)
	webhooks.POST("/alipay/pay", s.webhookAlipayPay)
	webhooks.POST("/vod", s.webhookVOD)

	// === Dev-only：一键模拟支付成功 + 一键灌 mock 数据，PAYMENT_DEV_MODE=true 才挂载 ===
	if s.cfg.PaymentDevMode {
		dev := v1.Group("/dev")
		dev.POST("/orders/:order_no/pay", s.devMockPayOrder)
		dev.POST("/seed", s.devSeed)
		log.Printf("[dev] PAYMENT_DEV_MODE=true，已挂载 POST /v1/dev/orders/:order_no/pay 和 POST /v1/dev/seed")
	}

	return r
}

func (s *Server) idempotencyMiddleware(subject string) gin.HandlerFunc {
	return s.idem.Middleware(subject, func(c *gin.Context) uint64 {
		return middleware.CurrentID(c)
	})
}

func (s *Server) corsMiddleware() gin.HandlerFunc {
	allowed := map[string]bool{}
	allowAny := false
	for _, origin := range s.cfg.CORSAllowedOrigins {
		if origin == "*" {
			allowAny = true
			continue
		}
		allowed[origin] = true
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		// 不论 origin 是否命中白名单，只要存在白名单就声明 Vary: Origin，
		// 防止反向代理 / CDN 把非白名单 origin 的响应缓存给白名单 origin。
		if !allowAny && len(allowed) > 0 {
			c.Header("Vary", "Origin")
		}
		if origin != "" && (allowAny || allowed[origin]) {
			if allowAny {
				c.Header("Access-Control-Allow-Origin", "*")
			} else {
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Access-Control-Allow-Credentials", "true")
			}
			c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			c.Header("Access-Control-Max-Age", "600")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
