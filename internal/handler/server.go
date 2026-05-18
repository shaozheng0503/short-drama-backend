package handler

import (
	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Server struct {
	db  *gorm.DB
	cfg config.Config
}

func New(db *gorm.DB, cfg config.Config) *Server {
	return &Server{db: db, cfg: cfg}
}

func (s *Server) Router() *gin.Engine {
	r := gin.Default()

	r.GET("/health", s.health)

	api := r.Group("/api/v1")
	api.POST("/auth/register", s.register)
	api.POST("/auth/login", s.login)

	public := api.Group("")
	public.GET("/dramas", s.listDramas)
	public.GET("/dramas/:id", s.getDrama)
	public.GET("/dramas/:id/episodes", s.listEpisodes)
	public.GET("/search", s.search)

	auth := api.Group("")
	auth.Use(middleware.Auth(s.cfg))
	auth.GET("/me", s.me)
	auth.POST("/dramas/:id/like", s.likeDrama)
	auth.POST("/dramas/:id/favorite", s.favoriteDrama)
	auth.POST("/dramas/:id/share", s.shareDrama)
	auth.POST("/dramas/:id/comments", s.createComment)
	auth.GET("/dramas/:id/comments", s.listComments)
	auth.PUT("/watch-history", s.upsertWatchHistory)
	auth.GET("/watch-history", s.listWatchHistory)
	auth.POST("/checkins", s.checkIn)
	auth.POST("/orders", s.createOrder)
	auth.POST("/orders/:id/pay", s.markOrderPaid)
	auth.GET("/notifications", s.listNotifications)
	auth.PUT("/notifications/:id/read", s.readNotification)

	creator := api.Group("/creator")
	creator.Use(middleware.Auth(s.cfg), middleware.RequireRoles(model.RoleCreator, model.RoleAdmin))
	creator.POST("/profile", s.upsertCreatorProfile)
	creator.GET("/dashboard", s.creatorDashboard)
	creator.POST("/dramas", s.createDrama)
	creator.PUT("/dramas/:id", s.updateDrama)
	creator.POST("/dramas/:id/episodes", s.createEpisode)
	creator.GET("/contracts", s.creatorContracts)
	creator.POST("/contracts", s.createContract)
	creator.GET("/revenues", s.creatorRevenues)
	creator.POST("/withdrawals", s.createWithdrawal)
	creator.GET("/withdrawals", s.creatorWithdrawals)

	admin := api.Group("/admin")
	admin.Use(middleware.Auth(s.cfg), middleware.RequireRoles(model.RoleAdmin))
	admin.GET("/dashboard", s.adminDashboard)
	admin.GET("/users", s.adminUsers)
	admin.PUT("/creators/:id/verify", s.verifyCreator)
	admin.POST("/dramas", s.adminCreateDrama)
	admin.PUT("/dramas/:id", s.adminUpdateDrama)
	admin.PUT("/dramas/:id/status", s.updateDramaStatus)
	admin.GET("/finance/orders", s.adminOrders)
	admin.GET("/finance/withdrawals", s.adminWithdrawals)
	admin.PUT("/finance/withdrawals/:id/status", s.updateWithdrawalStatus)
	admin.GET("/contracts", s.adminContracts)
	admin.PUT("/contracts/:id/status", s.updateContractStatus)

	return r
}
