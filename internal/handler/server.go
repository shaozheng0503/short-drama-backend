package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"ai-drama-platform/internal/alert"
	"ai-drama-platform/internal/billing"
	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/cos"
	"ai-drama-platform/internal/idempotency"
	"ai-drama-platform/internal/kyc"
	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/payment"
	"ai-drama-platform/internal/ratelimit"
	"ai-drama-platform/internal/redisclient"
	"ai-drama-platform/internal/response"
	"ai-drama-platform/internal/secure"
	"ai-drama-platform/internal/sms"
	"ai-drama-platform/internal/vod"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type Server struct {
	db        *gorm.DB
	cfg       config.Config
	sms       *sms.Service
	payments  *payment.Registry
	billing   *billing.Service
	cryptor   *secure.Cryptor
	idem      *idempotency.Service
	alerts    *alert.Client
	redis     *redis.Client
	cos       *cos.Signer
	vod       *vod.Signer
	kyc       kyc.Provider
	shareMu   sync.Mutex
	shareSeen map[string]time.Time // Redis 不可用时的 IP+drama 分享计数限频兜底
	started   time.Time
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
		db:        db,
		cfg:       cfg,
		sms:       sms.New(db, cfg),
		payments:  payments,
		billing:   billing.New(db, cfg, payments),
		cryptor:   cryptor,
		idem:      idempotency.New(rdb, cfg.IdempotencyTTL),
		alerts:    alert.New(cfg),
		redis:     rdb,
		cos:       cos.New(cfg),
		vod:       vod.New(cfg),
		kyc:       kyc.SelectProvider(cfg),
		shareSeen: map[string]time.Time{},
		started:   time.Now(),
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
				s.closeExpiredRecharges(now)
			}
		}
	}()
	// 月度结算 cron（每月 1 号 02:00 自动生成上月结算单；启动期补一遍）
	s.startSettlementCron(ctx)
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

func (s *Server) closeExpiredRecharges(now time.Time) {
	result, err := s.billing.CloseExpiredRecharges(now)
	if err != nil {
		log.Printf("[bg] close expired recharges err=%v", err)
		s.alerts.SendAsync(alert.Event{
			Level:   "error",
			Type:    "close_expired_recharges_failed",
			Message: "关闭过期充值单失败",
			Fields: map[string]interface{}{
				"error": err.Error(),
			},
		})
	} else if result.ClosedCount > 0 {
		log.Printf("[bg] closed %d expired recharges oldest_expired_at=%v samples=%v",
			result.ClosedCount, result.OldestExpiredAt, result.SampleRechargeNos)
		s.alerts.SendAsync(alert.Event{
			Level:   "warn",
			Type:    "expired_recharges_closed",
			Message: "过期充值单已关闭",
			Fields: map[string]interface{}{
				"closed_count":        result.ClosedCount,
				"oldest_expired_at":   result.OldestExpiredAt,
				"sample_recharge_nos": result.SampleRechargeNos,
			},
		})
	}
}

func (s *Server) Router() *gin.Engine {
	// 默认 release 模式：debug 模式会打印每条路由 + 额外每请求开销，且 gin 官方明确不建议生产用。
	// 本地调试可设 GIN_MODE=debug 覆盖。
	if gin.Mode() == gin.DebugMode && os.Getenv("GIN_MODE") == "" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.Default()
	// 默认 gin 不区分 404 / 405，路径错或方法错都走 NoRoute。打开后才能让 NoMethod 生效，
	// 给前端更清晰的错误码（虽然 body 走同一套 JSON 结构）。
	r.HandleMethodNotAllowed = true
	// NoRoute / NoMethod 走统一 JSON 包，避免 gin 默认 "404 page not found" plain text
	// 让前端 / Apifox 拿到结构化错误响应（与其他 40x 响应一致）。
	r.NoRoute(func(c *gin.Context) {
		response.NotFound(c, "接口路径不存在")
	})
	r.NoMethod(func(c *gin.Context) {
		c.JSON(http.StatusMethodNotAllowed, response.Body{
			Code:    response.CodeNotFound,
			Message: "HTTP 方法不允许",
			Data:    nil,
		})
	})
	// 单请求体上限：视频/图片/合同都走腾讯云直传（不经本服务），经本服务的最大 body 是
	// 财务 xlsx 导入的 multipart，10MB 足够；防止超大 body 撑爆内存。
	r.MaxMultipartMemory = maxRequestBodyBytes
	r.Use(limitRequestBody(maxRequestBodyBytes))
	r.Use(s.corsMiddleware())
	r.Use(ratelimit.New(s.cfg).Handler())
	r.GET("/health", s.health)
	r.GET("/ready", s.ready)

	v1 := r.Group("/v1")

	common := v1.Group("/common")
	common.POST("/sms/send", s.sendSMS)
	common.POST("/uploads/image-sign", s.commonImageUploadSign)
	common.GET("/aigc-tools", s.getAIGCTools)
	common.GET("/languages", s.listLanguages)
	common.GET("/app/latest", s.getAppLatest) // App 最新版本信息（公开，无需登录）

	// === APP ===
	app := v1.Group("/app")
	app.POST("/auth/login", s.appLogin)
	app.GET("/categories", s.appListCategories)
	app.GET("/home", s.appHome)
	app.GET("/theater", s.appTheater)
	app.GET("/dramas", s.appListDramas)
	app.GET("/share/dramas/:id", s.appShareDramaPage)
	app.GET("/dramas/:id", s.appDramaDetail)
	app.GET("/dramas/:id/episodes", s.appListEpisodes)
	app.GET("/dramas/:id/comments", s.appListComments)
	app.GET("/comments/:id/replies", s.appListReplies) // 楼中楼：某条顶层评论下的回复列表（匿名可看）
	app.GET("/search", s.appSearch)
	app.GET("/search/hot", s.getHotSearch)
	app.GET("/products", s.appListProducts)
	// 不登录刷短剧：播放地址匿名可取（免费集直接给链；付费集返回 need_login）。
	app.GET("/episodes/:id/play", s.appPlayEpisode)

	appAuth := app.Group("")
	appAuth.Use(middleware.RequireApp(s.cfg))
	appAuth.Use(s.requireActiveApp())
	appAuth.GET("/me", s.appMe)
	appAuth.PUT("/me", s.appUpdateMe)
	appAuth.GET("/me/favorites", s.appListFavorites)
	appAuth.GET("/me/likes", s.appListLikes) // 我的点赞：点赞过的剧集（集级）
	// 消息页：评论回复 / 评论点赞 两类站内信 + 已读上报
	appAuth.GET("/messages", s.appListMessages)
	appAuth.POST("/messages/read-all", s.appMarkAllMessagesRead)
	appAuth.POST("/messages/:id/read", s.appMarkMessageRead)
	appAuth.POST("/play-history", s.appUpsertPlayHistory)
	appAuth.GET("/play-history", s.appListPlayHistory)
	// 点赞=单集级（对齐红果），收藏=整剧级
	appAuth.POST("/episodes/:id/like", s.appLikeEpisode)
	appAuth.DELETE("/episodes/:id/like", s.appUnlikeEpisode)
	appAuth.POST("/dramas/:id/favorite", s.appFavoriteDrama)
	appAuth.DELETE("/dramas/:id/favorite", s.appUnfavoriteDrama)
	appAuth.POST("/dramas/:id/share", s.appShareDrama)
	appAuth.POST("/dramas/:id/comments", s.appCreateComment) // 顶层评论 / 楼中楼回复（body 带 parent_id 即回复）
	appAuth.POST("/comments/:id/like", s.appLikeComment)     // 评论点赞
	appAuth.DELETE("/comments/:id/like", s.appUnlikeComment) // 取消评论点赞
	appAuth.POST("/orders/preview", s.appOrderPreview)       // 单集下单前试算：展示实付金额
	appAuth.POST("/orders", s.idempotencyMiddleware("app"), s.appCreateOrder)
	appAuth.POST("/orders/batch/preview", s.appBatchOrderPreview)
	appAuth.POST("/orders/batch", s.idempotencyMiddleware("app"), s.appCreateBatchOrder)
	appAuth.GET("/orders", s.appListOrders)
	appAuth.GET("/orders/grouped", s.appListOrdersGrouped) // 按短剧折叠视图
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
	creatorAuth.GET("/account", s.creatorGetAccount)
	creatorAuth.PUT("/account", s.creatorUpdateAccount)
	creatorAuth.GET("/verification", s.creatorGetVerification)
	creatorAuth.PUT("/verification/personal", s.creatorUpdatePersonalVerification)
	creatorAuth.PUT("/verification/enterprise", s.creatorUpdateEnterpriseVerification)
	creatorAuth.POST("/bank-card/send-sms", s.creatorSendBankCardSMS)
	creatorAuth.POST("/bank-card/change", s.creatorChangeBankCard)
	creatorAuth.POST("/verification/biz-license/ocr", s.creatorBizLicenseOCR) // 营业执照 OCR 自动填充
	creatorAuth.GET("/channel-accounts", s.creatorListChannelAccounts)
	creatorAuth.POST("/channel-accounts", s.creatorCreateChannelAccount)
	creatorAuth.PUT("/channel-accounts/:id", s.creatorUpdateChannelAccount)
	creatorAuth.DELETE("/channel-accounts/:id", s.creatorDeleteChannelAccount)
	creatorAuth.GET("/dashboard", s.creatorDashboard)
	creatorAuth.GET("/categories", s.creatorListCategories)
	creatorAuth.GET("/config/pricing", s.creatorGetPricingConfig)
	creatorAuth.GET("/config/cover-specs", s.creatorGetCoverSpecs)                       // 漫剧封面上传规格（比例/分辨率/大小/格式）
	creatorAuth.GET("/cost-config/template.xlsx", s.creatorDownloadCostConfigTemplate) // 成本配置清单模板下载
	creatorAuth.GET("/dramas", s.creatorListDramas)
	// verifiedCreator: 未实名认证通过的创作者不能上传 / 建剧 / 提交审核（「没认证通过不能上传还有相关操作」）。
	verified := s.requireVerifiedCreator()
	creatorAuth.POST("/dramas", verified, s.creatorCreateDrama)
	creatorAuth.GET("/dramas/:id", s.creatorGetDrama)
	creatorAuth.PUT("/dramas/:id", verified, s.creatorUpdateDrama)
	creatorAuth.DELETE("/dramas/:id", s.creatorDeleteDrama)
	creatorAuth.POST("/dramas/:id/submit", verified, s.creatorSubmitDrama)
	creatorAuth.PUT("/dramas/:id/publish-config", verified, s.creatorUpdateDramaPublishConfig)
	creatorAuth.POST("/dramas/:id/publish", verified, s.creatorPublishDrama)
	creatorAuth.POST("/dramas/:id/offline", s.creatorOfflineDrama)
	creatorAuth.GET("/dramas/:id/stats", s.creatorDramaStats)
	// 2026-07-06 加：一键下载素材清单（前端 JSZip 打包，不走后端 zip 流）
	creatorAuth.GET("/dramas/:id/files-manifest", s.creatorGetDramaFilesManifest)

	creatorAuth.GET("/dramas/:id/episodes", s.creatorListEpisodes)
	creatorAuth.POST("/dramas/:id/episodes", verified, s.creatorCreateEpisode)
	creatorAuth.POST("/dramas/:id/episodes/batch", verified, s.creatorBatchCreateEpisodes)
	creatorAuth.PUT("/dramas/:id/episodes/reorder", verified, s.creatorReorderEpisodes)
	creatorAuth.PUT("/episodes/:id", verified, s.creatorUpdateEpisode)
	creatorAuth.DELETE("/episodes/:id", s.creatorDeleteEpisode)
	creatorAuth.POST("/episodes/:id/refresh-vod", verified, s.creatorRefreshEpisodeVOD)
	creatorAuth.POST("/episodes/:id/retry", verified, s.creatorRetryEpisode)
	creatorAuth.GET("/episodes/:id/preview", s.creatorPreviewEpisode)

	creatorAuth.POST("/uploads/vod-sign", verified, s.creatorVODUploadSign)
	creatorAuth.POST("/uploads/image-sign", verified, s.creatorImageUploadSign)

	creatorAuth.GET("/income", s.creatorIncome)
	// === 创作者结算 & 发票（2026-07-01）===
	// 旧 /settlement/summary 改名为 /settlements/summary（保持单数 path 不动，避免影响老前端）
	creatorAuth.GET("/settlement/summary", s.creatorSettlementSummary) // 老接口兼容
	creatorAuth.GET("/settlements", s.creatorListSettlements)
	creatorAuth.GET("/settlements/:id", s.creatorGetSettlement)
	creatorAuth.GET("/settlements/:id/download", s.creatorDownloadSettlementExcel) // Excel 对账单（电子表格，方便筛选）
	creatorAuth.GET("/settlements/:id/download.pdf", s.creatorDownloadSettlementPDF) // PDF 对账单（存档/发邮件用，固定版式）
	// 2026-07-02 改：流程图步骤 1 预览结算单（实时聚合，不入库）
	creatorAuth.GET("/settlement/preview", s.creatorPreviewSettlement)
	// 2026-07-06 加 P1-5：时间线按天回看
	creatorAuth.GET("/settlements/:id/timeline", s.creatorSettlementTimeline)
	// 2026-07-07 改（邱嘉诚反馈）：发票不再独立管理，合并到提现接口
	// 删除 POST/GET/DELETE /invoices 系列接口，发票在提现事务内自动创建
	creatorAuth.POST("/withdrawals", s.idempotencyMiddleware("creator"), s.creatorCreateWithdrawal)
	creatorAuth.GET("/withdrawals", s.creatorListWithdrawals)
	creatorAuth.GET("/withdrawals/tax-preview", s.creatorWithdrawTaxPreview)
	creatorAuth.GET("/withdrawals/:id", s.creatorGetWithdrawal) // 提现记录详情（含关联发票+结算单）
	// 2026-07-06 加 P1-5：提现时间线
	creatorAuth.GET("/withdrawals/:id/timeline", s.creatorWithdrawalTimeline)
	creatorAuth.GET("/withdrawals/:id/download.pdf", s.creatorDownloadWithdrawalPDF) // 提现单 PDF（报账用）
	creatorAuth.GET("/data-overview", s.creatorDataOverview)
	creatorAuth.GET("/contracts", s.creatorListContracts)
	creatorAuth.GET("/contracts/:id/scan", s.creatorDownloadContractScan)
	creatorAuth.GET("/contracts/:id", s.creatorGetContract)

	creatorAuth.GET("/notifications", s.creatorListNotifications)
	creatorAuth.POST("/notifications/read-all", s.creatorMarkAllNotificationsRead)
	creatorAuth.POST("/notifications/:id/read", s.creatorMarkNotificationRead)

	// === Distributor（发行商，0.15.0）===
	distributor := v1.Group("/distributor")
	distributor.POST("/auth/login", s.distributorLogin)
	distAuth := distributor.Group("")
	distAuth.Use(middleware.RequireDistributor(s.cfg))
	distAuth.Use(s.requireActiveDistributor())
	distAuth.GET("/me", s.distributorMe)
	distAuth.PUT("/me", s.distributorUpdateMe)
	distAuth.GET("/verification/status", s.distributorVerificationStatus)
	distAuth.PUT("/verification/enterprise", s.distributorUpdateEnterpriseVerification)
	distAuth.POST("/verification/biz-license/ocr", s.distributorBizLicenseOCR)
	distAuth.GET("/deposit/balance", s.distributorDepositBalance)

	// === Publisher（发行商 /publisher，0.15.0）===
	pub := v1.Group("/publisher")
	pub.POST("/auth/login", s.distributorLogin) // 复用 distributor 登录
	pubAuth := pub.Group("")
	pubAuth.Use(middleware.RequireDistributor(s.cfg))
	pubAuth.Use(s.requireActiveDistributor())
	pubAuth.GET("/me", s.distributorMe)
	pubAuth.PUT("/me", s.distributorUpdateMe)
	pubAuth.GET("/profile/verification", s.distributorVerificationStatus)
	pubAuth.POST("/profile/verification", s.distributorUpdateEnterpriseVerification)
	pubAuth.POST("/upload", s.publisherUpload)
	pubAuth.POST("/uploads/remittance", s.publisherRemittanceUploadSign)
	pubAuth.GET("/dashboard", s.publisherDashboard)
	// 剧集广场
	pubAuth.GET("/dramas", s.publisherListDramas)
	pubAuth.GET("/dramas/:id", s.publisherGetDrama)
	// 认领
	pubAuth.GET("/claims", s.publisherListClaims)
	pubAuth.POST("/claims", s.publisherCreateClaim)
	pubAuth.GET("/claims/:id", s.publisherGetClaim)
	pubAuth.POST("/claims/:id/deposit", s.publisherPayDeposit)
	pubAuth.POST("/claims/:id/submit", s.publisherSubmitClaim)
	// 已认领剧集
	pubAuth.GET("/claimed-dramas", s.publisherListClaimedDramas)
	pubAuth.GET("/claimed-dramas/:id", s.publisherGetClaimedDrama)
	pubAuth.GET("/claimed-dramas/:id/claims", s.publisherClaimedDramaClaims)
	pubAuth.GET("/claimed-dramas/:id/income-records", s.publisherClaimedDramaIncomeRecords)
	pubAuth.GET("/claimed-dramas/:id/deposit-deductions", s.publisherClaimedDramaDepositDeductions)
	pubAuth.GET("/claimed-dramas/:id/download", s.publisherDownloadClaimedDrama)
	// 押金
	pubAuth.GET("/deposit/account", s.publisherDepositAccount)
	pubAuth.POST("/deposit/recharge", s.publisherRecharge)
	pubAuth.GET("/deposit/transactions", s.publisherDepositTransactions)
	// 结算
	pubAuth.GET("/settlements/summary", s.publisherSettlementSummary)
	pubAuth.GET("/settlements", s.publisherListSettlements)
	pubAuth.GET("/settlements/:id", s.publisherGetSettlement)
	pubAuth.POST("/settlements/:id/remittance", s.publisherSubmitRemittance)
	// 提现（已废弃，保留只读）
	pubAuth.GET("/settlements/:id/withdrawal-preview", s.publisherWithdrawalPreview)
	pubAuth.GET("/withdrawals", s.publisherListWithdrawals)
	pubAuth.POST("/withdrawals", s.publisherCreateWithdrawal) // 废弃，返回 409
	pubAuth.GET("/withdrawals/:id", s.publisherGetWithdrawal)

	// === Admin ===
	admin := v1.Group("/admin")
	admin.POST("/auth/login", s.adminLogin)
	adminAuth := admin.Group("")
	adminAuth.Use(middleware.RequireAdmin(s.cfg))
	adminAuth.Use(s.requireActiveAdmin())
	adminAuth.Use(s.auditMiddleware())
	adminAuth.POST("/auth/refresh", s.adminRefreshToken) // 滑动续期：换新 token，避免常用管理员被踢
	adminAuth.GET("/me", s.adminMe)
	adminAuth.GET("/dashboard", s.adminDashboard)

	adminAuth.GET("/categories", s.adminListCategories)
	adminAuth.POST("/categories", s.adminCreateCategory)
	adminAuth.PUT("/categories/:id", s.adminUpdateCategory)
	adminAuth.DELETE("/categories/:id", s.adminDeleteCategory)
	adminAuth.GET("/languages", s.listLanguages)
	adminAuth.POST("/languages", s.adminCreateLanguage)
	adminAuth.PUT("/languages/:id", s.adminUpdateLanguage)
	adminAuth.DELETE("/languages/:id", s.adminDeleteLanguage)

	adminAuth.GET("/dramas", s.adminListDramas)
	adminAuth.POST("/dramas", s.adminCreateDrama)
	adminAuth.GET("/dramas/:id", s.adminGetDrama)
	adminAuth.PUT("/dramas/:id", s.adminUpdateDrama)
	adminAuth.DELETE("/dramas/:id", s.adminDeleteDrama)
	adminAuth.POST("/dramas/:id/publish", s.adminPublishDrama)
	adminAuth.POST("/dramas/:id/offline", s.adminOfflineDrama)
	adminAuth.POST("/dramas/:id/approve", s.requireAdminRole(model.AdminRoleAuditor), s.adminApproveDrama)
	adminAuth.POST("/dramas/:id/reject", s.requireAdminRole(model.AdminRoleAuditor), s.adminRejectDrama)
	adminAuth.POST("/dramas/:id/audit", s.requireAdminRole(model.AdminRoleAuditor), s.adminAuditDrama) // 分批审核：按维度(content/video)通过/驳回
	adminAuth.PUT("/dramas/:id/distributable", s.adminSetDistributable)                                // 开关发行商认领

	adminAuth.GET("/dramas/:id/episodes", s.adminListEpisodes)
	adminAuth.POST("/dramas/:id/episodes", s.adminCreateEpisode)
	adminAuth.POST("/dramas/:id/episodes/batch", s.adminBatchCreateEpisodes)
	adminAuth.PUT("/dramas/:id/episodes/reorder", s.adminReorderEpisodes)
	adminAuth.PUT("/episodes/:id", s.adminUpdateEpisode)
	adminAuth.DELETE("/episodes/:id", s.adminDeleteEpisode)
	adminAuth.POST("/episodes/:id/refresh-vod", s.adminRefreshEpisodeVOD)
	adminAuth.POST("/episodes/:id/retry", s.adminRetryEpisode)
	adminAuth.GET("/episodes/:id/preview", s.adminPreviewEpisode)

	adminAuth.POST("/uploads/vod-sign", s.adminVODUploadSign)
	adminAuth.POST("/uploads/contract-sign", s.adminContractUploadSign)

	adminAuth.GET("/creators", s.adminListCreators)
	adminAuth.POST("/creators", s.adminCreateCreator)
	adminAuth.GET("/creators/template.xlsx", s.adminDownloadCreatorTemplate)   // 批量导入模板下载
	adminAuth.POST("/creators/import", s.adminImportCreators)                  // 批量导入创作者
	adminAuth.GET("/creators/:id", s.adminGetCreator)
	adminAuth.PUT("/creators/:id", s.adminUpdateCreator)
	adminAuth.POST("/creators/:id/ban", s.adminBanCreator)
	adminAuth.POST("/creators/:id/unban", s.adminUnbanCreator)
	adminAuth.POST("/creators/:id/verification/approve", s.requireAdminRole(model.AdminRoleAuditor), s.adminApproveCreatorVerification)
	adminAuth.POST("/creators/:id/verification/reject", s.requireAdminRole(model.AdminRoleAuditor), s.adminRejectCreatorVerification)
	adminAuth.GET("/creator-channel-accounts", s.adminListCreatorChannelAccounts)
	adminAuth.POST("/creator-channel-accounts", s.adminCreateChannelAccount)
	adminAuth.PUT("/creator-channel-accounts/:id", s.adminUpdateChannelAccount)
	adminAuth.DELETE("/creator-channel-accounts/:id", s.adminDeleteChannelAccount)

	adminAuth.GET("/users", s.adminListUsers)
	adminAuth.GET("/users/:id", s.adminGetUser)
	adminAuth.POST("/users/:id/ban", s.adminBanUser)
	adminAuth.POST("/users/:id/unban", s.adminUnbanUser)

	adminAuth.GET("/comments", s.adminListComments)
	adminAuth.DELETE("/comments/:id", s.adminDeleteComment)

	adminAuth.GET("/orders", s.adminListOrders)
	adminAuth.GET("/orders/:order_no", s.adminGetOrder)
	// 退款 / 主动查单:仅财务角色;路径与现有 orders 同前缀,便于按订单聚合权限。
	adminAuth.POST("/orders/:order_no/refund", s.requireAdminRole(model.AdminRoleFinance), s.adminRefundOrder)
	adminAuth.POST("/orders/:order_no/sync", s.requireAdminRole(model.AdminRoleFinance), s.adminSyncOrder)
	// 充值单主动查单:与订单查单对称,webhook 丢失时兜底回写充值状态。
	adminAuth.POST("/distributor-recharges/:recharge_no/sync", s.requireAdminRole(model.AdminRoleFinance), s.adminSyncRecharge)

	adminAuth.GET("/withdrawals", s.adminListWithdrawals)
	adminAuth.GET("/withdrawals/:id", s.adminGetWithdrawal) // 财务提现详情（含完整银行卡号）
	adminAuth.POST("/withdrawals/:id/approve", s.requireAdminRole(model.AdminRoleFinance), s.adminApproveWithdrawal)
	adminAuth.POST("/withdrawals/:id/reject", s.requireAdminRole(model.AdminRoleFinance), s.adminRejectWithdrawal)
	adminAuth.POST("/withdrawals/:id/mark-paid", s.requireAdminRole(model.AdminRoleFinance), s.adminMarkWithdrawalPaid)
	// 2026-07-02 改：流程图步骤 3「合并审核」——财务一次审完 withdrawal + invoice
	adminAuth.POST("/withdrawals/:id/review", s.requireAdminRole(model.AdminRoleFinance), s.adminReviewWithdrawal)
	adminAuth.GET("/withdrawals/:id/download.pdf", s.adminDownloadWithdrawalPDF) // 财务提现单 PDF

	// === Admin 结算（2026-07-01）===
	// 0.14.0 删除发票列表/详情/审核接口（发票跟提现绑定，通过提现记录查看）
	adminAuth.GET("/settlements", s.adminListSettlements)
	adminAuth.GET("/settlements/:id", s.adminGetSettlement)
	adminAuth.GET("/settlements/:id/download.pdf", s.adminDownloadSettlementPDF) // 财务 PDF 对账单
	// 2026-07-28 删除 POST /settlements/generate（邱嘉诚要求，前端已移除）
	adminAuth.POST("/settlements/:id/close", s.requireAdminRole(model.AdminRoleFinance), s.adminCloseSettlement)

	// App 付费收入（平台自有支付分账）：按短剧聚合的毛收入/净收入，订单中心+收益汇总（财务角色）
	adminAuth.GET("/finance/app-income", s.requireAdminRole(model.AdminRoleFinance), s.adminListAppIncome)
	// 订单导出 Excel：财务把 App 用户购买订单导出汇总（订单中心导出，财务角色）
	adminAuth.GET("/finance/orders-export.xlsx", s.requireAdminRole(model.AdminRoleFinance), s.adminExportOrders)
	// 财务 Excel 导入每日收入（财务角色）
	adminAuth.GET("/finance/income/template.xlsx", s.requireAdminRole(model.AdminRoleFinance), s.adminDownloadIncomeTemplate)
	adminAuth.GET("/finance/income/imports", s.requireAdminRole(model.AdminRoleFinance), s.adminListIncomeImports)
	adminAuth.GET("/finance/income/imports/:batch_no", s.requireAdminRole(model.AdminRoleFinance), s.adminGetIncomeImport)
	adminAuth.POST("/finance/income/import", s.requireAdminRole(model.AdminRoleFinance), s.adminImportDailyIncome)
	adminAuth.GET("/finance/channel-incomes", s.requireAdminRole(model.AdminRoleFinance), s.adminListChannelIncomes)
	adminAuth.PUT("/finance/channel-incomes/:id", s.requireAdminRole(model.AdminRoleFinance), s.adminUpdateChannelIncome)
	adminAuth.DELETE("/finance/channel-incomes/:id", s.requireAdminRole(model.AdminRoleFinance), s.adminDeleteChannelIncome)

	// 全局价格配置：读对所有 admin 开放，写仅超管。
	adminAuth.GET("/config/pricing", s.adminGetPricingConfig)
	adminAuth.PUT("/config/pricing", s.requireAdminRole(), s.adminUpdatePricingConfig)
	adminAuth.GET("/config/aigc-tools", s.adminGetAIGCTools)
	adminAuth.PUT("/config/aigc-tools", s.requireAdminRole(), s.adminUpdateAIGCTools)
	adminAuth.GET("/config/hot-search", s.adminGetHotSearch)
	adminAuth.PUT("/config/hot-search", s.requireAdminRole(), s.adminUpdateHotSearch)
	adminAuth.GET("/config/income-share", s.adminGetIncomeShareConfig)
	adminAuth.PUT("/config/income-share", s.requireAdminRole(), s.adminUpdateIncomeShareConfig)
	// 个税阶梯：读对所有 admin 开放，写仅超管。
	adminAuth.GET("/config/tax-brackets", s.adminListTaxBrackets)
	adminAuth.POST("/config/tax-brackets", s.requireAdminRole(), s.adminCreateTaxBracket)
	adminAuth.PUT("/config/tax-brackets/:id", s.requireAdminRole(), s.adminUpdateTaxBracket)
	adminAuth.DELETE("/config/tax-brackets/:id", s.requireAdminRole(), s.adminDeleteTaxBracket)

	adminAuth.GET("/contracts", s.adminListContracts)
	adminAuth.POST("/contracts", s.adminCreateContract)
	adminAuth.GET("/contracts/:id", s.adminGetContract)
	adminAuth.GET("/contracts/:id/scan", s.adminDownloadContractScan)
	adminAuth.PUT("/contracts/:id", s.adminUpdateContract)
	adminAuth.POST("/contracts/:id/cancel", s.adminCancelContract)
	adminAuth.POST("/contracts/:id/esign", s.adminEsignContract)

	// === Admin 发行商管理（0.15.0）===
	adminAuth.GET("/distributors", s.adminListDistributors)
	adminAuth.GET("/distributors/template.xlsx", s.adminDownloadDistributorTemplate) // 批量导入模板下载
	adminAuth.POST("/distributors/import", s.adminImportDistributors)                 // 批量导入发行商
	adminAuth.GET("/distributors/:id", s.adminGetDistributor)
	adminAuth.POST("/distributors/:id/verification/approve", s.requireAdminRole(model.AdminRoleAuditor), s.adminApproveDistributorVerification)
	adminAuth.POST("/distributors/:id/verification/reject", s.requireAdminRole(model.AdminRoleAuditor), s.adminRejectDistributorVerification)
	adminAuth.POST("/distributors/:id/ban", s.adminBanDistributor)
	adminAuth.POST("/distributors/:id/unban", s.adminUnbanDistributor)
	// 认领审核
	adminAuth.GET("/distributor-claims", s.adminListDistributorClaims)
	adminAuth.GET("/distributor-claims/:id", s.adminGetDistributorClaim)
	adminAuth.POST("/distributor-claims/:id/approve", s.requireAdminRole(model.AdminRoleAuditor), s.adminApproveClaim)
	adminAuth.POST("/distributor-claims/:id/reject", s.requireAdminRole(model.AdminRoleAuditor), s.adminRejectClaim)
	adminAuth.POST("/distributor-claims/:id/contract", s.requireAdminRole(model.AdminRoleAuditor), s.adminUploadContract)
	// 2026-07-28 删除 POST /finance/distributor-income/import（邱嘉诚要求，前端已移除）
	// 结算管理
	adminAuth.GET("/distributor-settlements", s.adminListDistributorSettlements)
	adminAuth.GET("/distributor-settlements/:id", s.adminGetDistributorSettlement)
	// 2026-07-28 删除 POST /distributor-settlements/generate（邱嘉诚要求，前端已移除）
	adminAuth.POST("/distributor-settlements/:id/confirm-receipt", s.requireAdminRole(model.AdminRoleFinance), s.adminConfirmDistributorSettlement)
	// 提现管理（已废弃，保留只读）
	adminAuth.GET("/distributor-withdrawals", s.adminListDistributorWithdrawals)
	adminAuth.POST("/distributor-withdrawals/:id/review", s.requireAdminRole(model.AdminRoleFinance), s.adminReviewDistributorWithdrawal)
	adminAuth.POST("/distributor-withdrawals/:id/mark-paid", s.requireAdminRole(model.AdminRoleFinance), s.adminMarkPaidDistributorWithdrawal)

	// === Webhooks（公开，由 provider 自己验签）===
	webhooks := v1.Group("/webhooks")
	webhooks.POST("/wechat/pay", s.webhookWechatPay)
	webhooks.POST("/alipay/pay", s.webhookAlipayPay)
	webhooks.POST("/vod", s.webhookVOD)

	// === Dev-only：一键模拟支付成功 + 一键灌 mock 数据，PAYMENT_DEV_MODE=true 才挂载 ===
	if s.cfg.PaymentDevMode {
		dev := v1.Group("/dev")
		dev.POST("/orders/:order_no/pay", s.devMockPayOrder)
		dev.POST("/orders/:order_no/refund", s.devMockRefundOrder)
		dev.POST("/recharges/:recharge_no/pay", s.devMockPayRecharge)
		dev.POST("/seed", s.devSeed)
		log.Printf("[dev] PAYMENT_DEV_MODE=true，已挂载 dev mock 端点")
	}

	return r
}

// maxRequestBodyBytes 单请求体字节上限（10MB）。
const maxRequestBodyBytes = 10 << 20

// limitRequestBody 用 http.MaxBytesReader 限制请求体大小，超限后读取返回错误、handler 的
// ShouldBindJSON/FormFile 会失败返回 400，避免恶意超大 body 占满内存。
func limitRequestBody(n int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, n)
		c.Next()
	}
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
