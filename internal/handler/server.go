package handler

import (
	"errors"
	"log"
	"time"

	"ai-drama-platform/internal/billing"
	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/payment"
	"ai-drama-platform/internal/secure"
	"ai-drama-platform/internal/sms"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Server struct {
	db       *gorm.DB
	cfg      config.Config
	sms      *sms.Service
	payments *payment.Registry
	billing  *billing.Service
	cryptor  *secure.Cryptor
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
	return &Server{
		db:       db,
		cfg:      cfg,
		sms:      sms.New(db, cfg),
		payments: payments,
		billing:  billing.New(db, cfg, payments),
		cryptor:  cryptor,
	}
}

// StartBackground 启动后台任务：当前只有「过期订单关闭」一个 ticker。
func (s *Server) StartBackground() {
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for now := range ticker.C {
			if n, err := s.billing.CloseExpiredOrders(now); err != nil {
				log.Printf("[bg] close expired orders err=%v", err)
			} else if n > 0 {
				log.Printf("[bg] closed %d expired orders", n)
			}
		}
	}()
}

func (s *Server) Router() *gin.Engine {
	r := gin.Default()
	r.GET("/health", s.health)

	v1 := r.Group("/v1")

	common := v1.Group("/common")
	common.POST("/sms/send", s.sendSMS)

	// === APP ===
	app := v1.Group("/app")
	app.POST("/auth/login", s.appLogin)
	app.GET("/home", s.appHome)
	app.GET("/dramas", s.appListDramas)
	app.GET("/dramas/:id", s.appDramaDetail)
	app.GET("/dramas/:id/episodes", s.appListEpisodes)
	app.GET("/search", s.appSearch)
	app.GET("/products", s.appListProducts)

	appAuth := app.Group("")
	appAuth.Use(middleware.RequireApp(s.cfg))
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
	appAuth.POST("/orders", s.appCreateOrder)
	appAuth.GET("/orders/:order_no", s.appGetOrder)
	appAuth.POST("/episodes/:id/unlock", s.appUnlockEpisode)

	// === Creator ===
	creator := v1.Group("/creator")
	creator.POST("/auth/login", s.creatorLogin)
	creatorAuth := creator.Group("")
	creatorAuth.Use(middleware.RequireCreator(s.cfg))
	creatorAuth.GET("/me", s.creatorMe)
	creatorAuth.PUT("/me/profile", s.creatorUpdateProfile)
	creatorAuth.GET("/dashboard", s.creatorDashboard)
	creatorAuth.GET("/dramas", s.creatorListDramas)
	creatorAuth.GET("/dramas/:id/stats", s.creatorDramaStats)
	creatorAuth.GET("/income", s.creatorIncome)
	creatorAuth.POST("/withdrawals", s.creatorCreateWithdrawal)
	creatorAuth.GET("/withdrawals", s.creatorListWithdrawals)
	creatorAuth.GET("/contracts", s.creatorListContracts)
	creatorAuth.GET("/contracts/:id", s.creatorGetContract)

	// === Admin ===
	admin := v1.Group("/admin")
	admin.POST("/auth/login", s.adminLogin)
	adminAuth := admin.Group("")
	adminAuth.Use(middleware.RequireAdmin(s.cfg))
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

	return r
}
