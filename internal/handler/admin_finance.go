package handler

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"ai-drama-platform/internal/billing"
	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/payment"
	"ai-drama-platform/internal/response"
	"ai-drama-platform/internal/sms"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Server) adminDashboard(c *gin.Context) {
	var (
		userCount, creatorCount, dramaCount int64
		episodeCount, publishedDramaCount   int64
		episodesThisMonth                   int64
		pendingDramaCount                   int64
		pendingWithdrawCount                int64
		todayIncome                         int64
		todayPlay                           int64
	)
	s.db.Model(&model.User{}).Count(&userCount)
	s.db.Model(&model.Creator{}).Count(&creatorCount)
	s.db.Model(&model.Drama{}).Count(&dramaCount)
	s.db.Model(&model.Drama{}).Where("status = ?", model.DramaStatusPublished).Count(&publishedDramaCount)
	s.db.Model(&model.Episode{}).Count(&episodeCount)
	s.db.Model(&model.Drama{}).Where("status IN ?", []string{model.DramaStatusReviewing, model.DramaStatusAwaitingPublish}).Count(&pendingDramaCount)
	s.db.Model(&model.Withdrawal{}).Where("status = ?", model.WithdrawalStatusPending).Count(&pendingWithdrawCount)
	now := time.Now()
	today := now.Format("2006-01-02")
	monthBegin := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	s.db.Model(&model.Episode{}).Where("created_at >= ?", monthBegin).Count(&episodesThisMonth)
	s.db.Model(&model.CreatorStatsDaily{}).Where("stat_date = ?", today).
		Select("COALESCE(SUM(income_cents),0)").Scan(&todayIncome)
	s.db.Model(&model.CreatorStatsDaily{}).Where("stat_date = ?", today).
		Select("COALESCE(SUM(play_count),0)").Scan(&todayPlay)

	// App 付费毛收入（平台侧实付，口径同 /finance/app-income）：净额 = 实付 - 退款。
	// 总览给「累计 / 本月 / 今日」三档，营收一眼可见。
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	appIncomeNet := func(since *time.Time) int64 {
		q := s.db.Model(&model.Order{}).Where("paid_at IS NOT NULL")
		if since != nil {
			q = q.Where("paid_at >= ?", *since)
		}
		var v int64
		q.Select("COALESCE(SUM(amount_cents),0) - COALESCE(SUM(refund_amount_cents),0)").Scan(&v)
		return v
	}

	response.OK(c, gin.H{
		"user_count":               userCount,
		"creator_count":            creatorCount,
		"drama_count":              dramaCount,
		"published_drama_count":    publishedDramaCount,
		"episode_count":            episodeCount,
		"episodes_this_month":      episodesThisMonth, // 本月（按 episode.created_at）上传集数
		"today_play_count":         todayPlay,
		"today_income_cents":       todayIncome, // 创作者当日分成实得（含第三方渠道导入），口径见 creator_stats_daily
		"app_income_total_cents":   appIncomeNet(nil),
		"app_income_month_cents":   appIncomeNet(&monthBegin),
		"app_income_today_cents":   appIncomeNet(&todayStart),
		"pending_drama_count":      pendingDramaCount,
		"pending_withdrawal_count": pendingWithdrawCount,
	})
}

// === 创作者管理 ===

func (s *Server) adminListCreators(c *gin.Context) {
	page, pageSize := paginate(c)
	q := s.db.Model(&model.Creator{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("verify_status"); v != "" {
		q = q.Where("verify_status = ?", v)
		// 认证待审：pending 仅指「已提交待审」，排除注册默认占位记录。
		if v == model.CreatorVerifyPending {
			q = q.Where("verify_submitted_at IS NOT NULL")
		}
	}
	if v := strings.TrimSpace(c.Query("keyword")); v != "" {
		like := "%" + v + "%"
		q = q.Where("name ILIKE ? OR phone ILIKE ?", like, like)
	}
	var total int64
	q.Count(&total)
	orderClause := "created_at desc"
	if v := c.Query("verify_status"); v == model.CreatorVerifyPending {
		orderClause = "verify_submitted_at desc NULLS LAST, created_at desc"
	}
	var creators []model.Creator
	q.Order(orderClause).Offset((page - 1) * pageSize).Limit(pageSize).Find(&creators)

	ids := make([]uint64, 0, len(creators))
	for _, cr := range creators {
		ids = append(ids, cr.ID)
	}
	dramaCounts := map[uint64]int64{}
	if len(ids) > 0 {
		var rows []struct {
			CreatorID uint64
			Cnt       int64
		}
		s.db.Table("dramas").
			Select("creator_id, COUNT(*) as cnt").
			Where("creator_id IN ?", ids).
			Group("creator_id").Scan(&rows)
		for _, r := range rows {
			dramaCounts[r.CreatorID] = r.Cnt
		}
	}

	list := make([]gin.H, 0, len(creators))
	for _, cr := range creators {
		uid := creatorDisplayUID(cr)
		nickname := cr.Nickname
		if nickname == "" {
			nickname = defaultCreatorNickname(cr.Phone)
		}
		list = append(list, gin.H{
			"id": cr.ID,
			// 管理端列表同详情，返回完整登录手机号，便于运营直接拨号联系；创作者自查仍脱敏。
			"phone":                cr.Phone,
			"login_phone":          cr.Phone,
			"name":                 cr.Name,
			"nickname":             nickname,
			"avatar_url":           creatorAvatarURL(cr),
			"account_uid":          uid,
			"creator_type":         cr.CreatorType,
			"org_name":             cr.OrgName,
			"org_credit_code":      cr.OrgCreditCode,
			"business_license_url": cr.BusinessLicenseURL,
			"bank_license_url":     cr.BankLicenseURL,
			"identity_mid":         uid,
			"identity_role":        cr.IdentityRole,
			"bank_name":            cr.BankName,
			"bank_branch":          cr.BankBranch,
			"id_card_no_masked":    cr.IDCardNoMasked,
			"bank_card_no_masked":  cr.BankCardNoMasked,
			"verify_status":        cr.VerifyStatus,
			"verify_reject_reason": cr.VerifyRejectReason,
			"verify_submitted_at":  cr.VerifySubmittedAt,
			"status":               cr.Status,
			"drama_count":          dramaCounts[cr.ID],
			"total_income_cents":   cr.TotalIncomeCents,
			"balance_cents":        cr.BalanceCents,
			"frozen_cents":         cr.FrozenCents,
		})
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

type adminCreateCreatorRequest struct {
	Phone string `json:"phone" binding:"required"`
	Name  string `json:"name"`
}

func (s *Server) adminCreateCreator(c *gin.Context) {
	var req adminCreateCreatorRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "phone 必填")
		return
	}
	if !sms.ValidPhone(req.Phone) {
		response.InvalidParam(c, "手机号格式不正确")
		return
	}
	creator := model.Creator{
		Phone:        req.Phone,
		Name:         req.Name,
		VerifyStatus: model.CreatorVerifyUnverified,
		Status:       model.StatusActive,
	}
	applyCreatorDefaults(&creator)
	if err := s.db.Create(&creator).Error; err != nil {
		if isUniqueViolation(err) {
			response.Conflict(c, "手机号已存在")
			return
		}
		response.ServerError(c, "创建创作者失败")
		return
	}
	response.OK(c, gin.H{
		"id":            creator.ID,
		"phone":         sms.MaskPhone(creator.Phone),
		"name":          creator.Name,
		"verify_status": creator.VerifyStatus,
		"status":        creator.Status,
	})
}

func (s *Server) adminGetCreator(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var cr model.Creator
	if err := s.db.First(&cr, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "创作者不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	// 管理端创作者详情展示**完整**登录手机号，便于运营在创作者迟迟不加微信时主动电话联系；
	// 创作者自查（creatorFullView 默认）仍脱敏。本接口已走 admin 鉴权 + 操作审计。
	view := creatorFullView(cr)
	view["phone"] = cr.Phone
	view["login_phone"] = cr.Phone
	if ai, ok := view["account_info"].(gin.H); ok {
		ai["login_phone"] = cr.Phone
	}
	response.OK(c, view)
}

type adminUpdateCreatorRequest struct {
	Name               *string `json:"name"`
	Nickname           *string `json:"nickname"`
	AvatarURL          *string `json:"avatar_url"`
	AccountUID         *string `json:"account_uid"`
	CreatorType        *string `json:"creator_type"`
	OrgName            *string `json:"org_name"`
	OrgCreditCode      *string `json:"org_credit_code"`
	BusinessLicenseURL *string `json:"business_license_url"`
	BankLicenseURL     *string `json:"bank_license_url"`
	IdentityMID        *string `json:"identity_mid"`
	IdentityRole       *string `json:"identity_role"`
	BankName           *string `json:"bank_name"`
	BankBranch         *string `json:"bank_branch"`
	VerifyStatus       *string `json:"verify_status"`
}

func (s *Server) adminUpdateCreator(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var cr model.Creator
	if err := s.db.First(&cr, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "创作者不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	var req adminUpdateCreatorRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}
	updates := map[string]interface{}{}
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Nickname != nil {
		updates["nickname"] = *req.Nickname
	}
	if req.AvatarURL != nil {
		updates["avatar_url"] = *req.AvatarURL
	}
	if req.AccountUID != nil {
		updates["account_uid"] = *req.AccountUID
	}
	if req.CreatorType != nil && *req.CreatorType != "" {
		if *req.CreatorType != model.CreatorTypePersonal && *req.CreatorType != model.CreatorTypeOrganization {
			response.InvalidParam(c, "creator_type 只能是 personal / organization")
			return
		}
		updates["creator_type"] = *req.CreatorType
	}
	if req.OrgName != nil {
		updates["org_name"] = *req.OrgName
	}
	if req.OrgCreditCode != nil {
		updates["org_credit_code"] = *req.OrgCreditCode
	}
	if req.BusinessLicenseURL != nil {
		updates["business_license_url"] = *req.BusinessLicenseURL
	}
	if req.BankLicenseURL != nil {
		updates["bank_license_url"] = *req.BankLicenseURL
	}
	if req.IdentityMID != nil {
		updates["identity_mid"] = *req.IdentityMID
	}
	if req.IdentityRole != nil {
		updates["identity_role"] = *req.IdentityRole
	}
	if req.BankName != nil {
		updates["bank_name"] = *req.BankName
	}
	if req.BankBranch != nil {
		updates["bank_branch"] = *req.BankBranch
	}
	if req.VerifyStatus != nil && *req.VerifyStatus != "" {
		switch *req.VerifyStatus {
		case model.CreatorVerifyUnverified, model.CreatorVerifyPending, model.CreatorVerifyVerified, model.CreatorVerifyRejected:
			updates["verify_status"] = *req.VerifyStatus
		default:
			response.InvalidParam(c, "verify_status 非法")
			return
		}
	}
	if len(updates) > 0 {
		if err := s.db.Model(&cr).Updates(updates).Error; err != nil {
			response.ServerError(c, "更新失败")
			return
		}
	}
	s.db.First(&cr, id)
	response.OK(c, creatorFullView(cr))
}

func (s *Server) adminBanCreator(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	res := s.db.Model(&model.Creator{}).Where("id = ?", id).
		Update("status", model.StatusBanned)
	if res.Error != nil {
		response.ServerError(c, "封禁失败")
		return
	}
	if res.RowsAffected == 0 {
		response.NotFound(c, "创作者不存在")
		return
	}
	response.OK(c, gin.H{"id": id, "status": model.StatusBanned})
}

func (s *Server) adminUnbanCreator(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	res := s.db.Model(&model.Creator{}).Where("id = ?", id).
		Update("status", model.StatusActive)
	if res.Error != nil {
		response.ServerError(c, "解封失败")
		return
	}
	if res.RowsAffected == 0 {
		response.NotFound(c, "创作者不存在")
		return
	}
	response.OK(c, gin.H{"id": id, "status": model.StatusActive})
}

func (s *Server) adminApproveCreatorVerification(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var cr model.Creator
	if err := s.db.First(&cr, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "创作者不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	if cr.VerifyStatus == model.CreatorVerifyVerified {
		response.Conflict(c, "创作者已通过实名认证")
		return
	}
	if check := checkCreatorVerificationApprovable(cr); !check.OK {
		respondWithdrawProfileBlock(c, check, false)
		return
	}
	if err := s.db.Model(&cr).Updates(map[string]interface{}{
		"verify_status":        model.CreatorVerifyVerified,
		"verify_reject_reason": "",
	}).Error; err != nil {
		response.ServerError(c, "审核通过失败")
		return
	}
	s.db.First(&cr, id)
	response.OK(c, creatorFullView(cr))
}

type adminRejectCreatorVerificationRequest struct {
	Reason string   `json:"reason" binding:"required"`
	Fields []string `json:"fields"` // 可选：字段级驳回标记（如 ["bank_card_no","org_legal_id_card"]），供前端高亮具体哪项被驳
}

func (s *Server) adminRejectCreatorVerification(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var req adminRejectCreatorVerificationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "reason 必填")
		return
	}
	var cr model.Creator
	if err := s.db.First(&cr, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "创作者不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	rejectFields := make([]string, 0, len(req.Fields))
	for _, f := range req.Fields {
		if f = strings.TrimSpace(f); f != "" {
			rejectFields = append(rejectFields, f)
		}
	}
	if err := s.db.Model(&cr).Updates(map[string]interface{}{
		"verify_status":        model.CreatorVerifyRejected,
		"verify_reject_reason": strings.TrimSpace(req.Reason),
		"verify_reject_fields": strings.Join(rejectFields, ","),
	}).Error; err != nil {
		response.ServerError(c, "审核驳回失败")
		return
	}
	s.db.First(&cr, id)
	response.OK(c, creatorFullView(cr))
}

// === APP 用户管理 ===
//
// MVP 阶段只暴露列表 / 详情 / ban / unban；恶意用户场景必备，无 ban 接口运维只能 SQL。
// 鉴权由 adminAuth 组保障；ban 后用户的 JWT 在 requireActiveApp 中间件下一次请求就 40301 失效。

func (s *Server) adminListUsers(c *gin.Context) {
	page, pageSize := paginate(c)
	q := s.db.Model(&model.User{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("phone"); v != "" {
		q = q.Where("phone LIKE ?", "%"+v+"%")
	}
	var total int64
	q.Count(&total)
	var users []model.User
	if err := q.Order("id desc").
		Limit(pageSize).
		Offset((page - 1) * pageSize).
		Find(&users).Error; err != nil {
		response.ServerError(c, "查询失败")
		return
	}
	list := make([]gin.H, 0, len(users))
	for _, u := range users {
		list = append(list, gin.H{
			"id":         u.ID,
			"phone":      sms.MaskPhone(u.Phone),
			"nickname":   u.Nickname,
			"avatar":     u.Avatar,
			"status":     u.Status,
			"created_at": u.CreatedAt,
		})
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

func (s *Server) adminGetUser(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var u model.User
	if err := s.db.First(&u, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "用户不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	response.OK(c, gin.H{
		"id":         u.ID,
		"phone":      sms.MaskPhone(u.Phone),
		"nickname":   u.Nickname,
		"avatar":     u.Avatar,
		"status":     u.Status,
		"created_at": u.CreatedAt,
		"updated_at": u.UpdatedAt,
	})
}

func (s *Server) adminBanUser(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	res := s.db.Model(&model.User{}).Where("id = ?", id).
		Update("status", model.StatusBanned)
	if res.Error != nil {
		response.ServerError(c, "封禁失败")
		return
	}
	if res.RowsAffected == 0 {
		response.NotFound(c, "用户不存在")
		return
	}
	response.OK(c, gin.H{"id": id, "status": model.StatusBanned})
}

func (s *Server) adminUnbanUser(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	res := s.db.Model(&model.User{}).Where("id = ?", id).
		Update("status", model.StatusActive)
	if res.Error != nil {
		response.ServerError(c, "解封失败")
		return
	}
	if res.RowsAffected == 0 {
		response.NotFound(c, "用户不存在")
		return
	}
	response.OK(c, gin.H{"id": id, "status": model.StatusActive})
}

// === 订单管理 ===

func (s *Server) adminListOrders(c *gin.Context) {
	page, pageSize := paginate(c)
	q := s.db.Model(&model.Order{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("payment_method"); v != "" {
		q = q.Where("payment_method = ?", v)
	}
	if v := parseUint(c.Query("user_id")); v > 0 {
		q = q.Where("user_id = ?", v)
	}
	if v := parseUint(c.Query("drama_id")); v > 0 {
		q = q.Where("drama_id = ?", v)
	}
	if v := parseUint(c.Query("episode_id")); v > 0 {
		q = q.Where("episode_id = ?", v)
	}
	if v := c.Query("order_no"); v != "" {
		// 订单号模糊搜索（LIKE %X%），后端自动加 %，前端不需要转义
		q = q.Where("order_no LIKE ?", "%"+v+"%")
	}
	if v := c.Query("platform_trade_no"); v != "" {
		// 平台流水号（微信/支付宝）模糊搜索
		q = q.Where("platform_trade_no LIKE ?", "%"+v+"%")
	}
	// 剧名模糊搜索（join dramas.title，LIKE %X%）。drama_id 精确过滤与本条件 AND 关系：
	// drama_id 给出时按 ID 锁死；只给 drama_title 时按剧名匹配；都不给则全部。
	if v := strings.TrimSpace(c.Query("drama_title")); v != "" {
		q = q.Joins("LEFT JOIN dramas d_title ON d_title.id = orders.drama_id").
			Where("d_title.title LIKE ?", "%"+v+"%")
	}
	// 集名模糊搜索（join episodes.title，LIKE %X%）。同理：与 episode_id AND。
	if v := strings.TrimSpace(c.Query("episode_title")); v != "" {
		q = q.Joins("LEFT JOIN episodes e_title ON e_title.id = orders.episode_id").
			Where("e_title.title LIKE ?", "%"+v+"%")
	}
	// 日期区间筛选（YYYY-MM-DD；date_to 含当天 => 走次日 0 点闭区间）
	// 注意：必须用 orders.created_at（不能写 created_at），因为本函数后续会
	// LEFT JOIN dramas/episodes，这两张表也都有 created_at 列，不限定表名会
	// 触发 PostgreSQL "column reference is ambiguous"（SQLSTATE 42702）。
	if v := c.Query("date_from"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			q = q.Where("orders.created_at >= ?", t)
		}
	}
	if v := c.Query("date_to"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			// date_to 含当天：end = 次日 00:00:00（半开区间 [from, end)）
			q = q.Where("orders.created_at < ?", t.Add(24*time.Hour))
		}
	}
	// 金额区间筛选（单位：分）
	if v := parseInt64(c.Query("min_amount_cents")); v > 0 {
		q = q.Where("amount_cents >= ?", v)
	}
	if v := parseInt64(c.Query("max_amount_cents")); v > 0 {
		q = q.Where("amount_cents <= ?", v)
	}
	// 已退款 / 未退款筛选（按 refund_amount_cents > 0 判定）
	switch c.Query("has_refund") {
	case "true", "1":
		q = q.Where("refund_amount_cents > 0")
	case "false", "0":
		q = q.Where("refund_amount_cents = 0")
	}
	var total int64
	q.Count(&total)
	// 用 join 一次性把 drama_title / episode_title 一起取出来（LEFT JOIN，
	// 避免订单关联的剧/集被删时整个 list 丢失）
	type orderRow struct {
		// 显式列清单（不用 orders.*）：GORM v2 在 Scan + Model().Select("orders.*") +
		// Join 时可能因 schema 缓存里的别名表 d_view/e_view 没注册而出现空结果，
		// 改成显式列既稳定也避免依赖 schema 缓存。
		ID                uint64
		OrderNo           string
		UserID            uint64
		DramaID           uint64
		EpisodeID         uint64
		AmountCents       int64
		RefundAmountCents int64
		PaymentMethod     string
		PlatformTradeNo   string
		Status            string
		PaidAt            *time.Time
		CreatedAt         time.Time
		DramaTitle        string `gorm:"column:drama_title"`
		EpisodeTitle      string `gorm:"column:episode_title"`
	}
	var rows []orderRow
	q.Select("orders.id, orders.order_no, orders.user_id, orders.drama_id, orders.episode_id, orders.amount_cents, orders.refund_amount_cents, orders.payment_method, orders.platform_trade_no, orders.status, orders.paid_at, orders.created_at, d_view.title AS drama_title, e_view.title AS episode_title").
		Joins("LEFT JOIN dramas d_view ON d_view.id = orders.drama_id").
		Joins("LEFT JOIN episodes e_view ON e_view.id = orders.episode_id").
		Order("orders.created_at desc").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Scan(&rows)
	list := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		list = append(list, gin.H{
			"order_no":          r.OrderNo,
			"user_id":           r.UserID,
			"drama_id":          r.DramaID,
			"drama_title":       r.DramaTitle,
			"episode_id":        r.EpisodeID,
			"episode_title":     r.EpisodeTitle,
			"amount_cents":      r.AmountCents,
			"refund_amount_cents": r.RefundAmountCents,
			"payment_method":    r.PaymentMethod,
			"status":            r.Status,
			"platform_trade_no": r.PlatformTradeNo,
			"paid_at":           r.PaidAt,
			"created_at":        r.CreatedAt,
		})
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

func (s *Server) adminGetOrder(c *gin.Context) {
	orderNo := c.Param("order_no")
	if orderNo == "" {
		response.InvalidParam(c, "order_no 必填")
		return
	}
	var o model.Order
	if err := s.db.Where("order_no = ?", orderNo).First(&o).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "订单不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	// 顺手取 drama_title / episode_title（与 list 接口对齐）
	// 用独立查询避免 join + ambiguous column 坑（episode_id=0 时 episode 查不到，title 为空字符串）
	var dramaTitle, episodeTitle string
	if o.DramaID > 0 {
		var d model.Drama
		if err := s.db.Select("title").First(&d, o.DramaID).Error; err == nil {
			dramaTitle = d.Title
		}
	}
	if o.EpisodeID > 0 {
		var e model.Episode
		if err := s.db.Select("title").First(&e, o.EpisodeID).Error; err == nil {
			episodeTitle = e.Title
		}
	}
	response.OK(c, gin.H{
		"order_no":          o.OrderNo,
		"user_id":           o.UserID,
		"drama_id":          o.DramaID,
		"drama_title":       dramaTitle,
		"episode_id":        o.EpisodeID,
		"episode_title":     episodeTitle,
		"product_id":        o.ProductID,
		"amount_cents":      o.AmountCents,
		"refund_amount_cents": o.RefundAmountCents,
		"payment_method":    o.PaymentMethod,
		"status":            o.Status,
		"platform_trade_no": o.PlatformTradeNo,
		"platform_refund_no": o.PlatformRefundNo,
		"refund_no":         o.RefundNo,
		"refund_reason":     o.RefundReason,
		"refunded_at":       o.RefundedAt,
		"paid_at":           o.PaidAt,
		"expired_at":        o.ExpiredAt,
		"created_at":        o.CreatedAt,
		"updated_at":        o.UpdatedAt,
	})
}

// === 提现审核 ===

func (s *Server) adminListWithdrawals(c *gin.Context) {
	page, pageSize := paginate(c)
	q := s.db.Model(&model.Withdrawal{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := parseUint(c.Query("creator_id")); v > 0 {
		q = q.Where("creator_id = ?", v)
	}
	var total int64
	q.Count(&total)
	var items []model.Withdrawal
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items)

	// 批量查创作者信息（拿完整银行卡号、姓名）
	creatorIDs := make([]uint64, 0, len(items))
	for _, w := range items {
		creatorIDs = append(creatorIDs, w.CreatorID)
	}
	creatorMap := map[uint64]model.Creator{}
	if len(creatorIDs) > 0 {
		var creators []model.Creator
		s.db.Where("id IN ?", creatorIDs).Find(&creators)
		for _, cr := range creators {
			creatorMap[cr.ID] = cr
		}
	}

	list := make([]gin.H, 0, len(items))
	for _, w := range items {
		v := s.withdrawalView(w)
		v["creator_id"] = w.CreatorID
		// admin 侧返回完整银行卡号 + 创作者姓名（财务打款用）
		if cr, ok := creatorMap[w.CreatorID]; ok {
			v["creator_name"] = cr.Name
			if cr.Nickname != "" {
				v["creator_nickname"] = cr.Nickname
			}
			v["creator_phone"] = cr.Phone
			v["bank_name"] = cr.BankName
			v["bank_branch"] = cr.BankBranch
			// 解密完整银行卡号
			if s.cryptor != nil && cr.BankCardNoEnc != "" {
				if full, err := s.cryptor.Decrypt(cr.BankCardNoEnc); err == nil && full != "" {
					v["bank_card_no"] = full
					v["bank_card_no_full"] = full
				}
			}
		}
		list = append(list, v)
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

// adminGetWithdrawal —— GET /v1/admin/withdrawals/:id
// 财务查看提现详情（含完整银行卡号，打款用）
func (s *Server) adminGetWithdrawal(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var w model.Withdrawal
	if err := s.db.First(&w, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "提现记录不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	v := s.withdrawalDetailView(w)
	v["creator_id"] = w.CreatorID
	// admin 侧返回完整创作者信息 + 完整银行卡号
	var cr model.Creator
	if err := s.db.First(&cr, w.CreatorID).Error; err == nil {
		v["creator_name"] = cr.Name
		if cr.Nickname != "" {
			v["creator_nickname"] = cr.Nickname
		}
		v["creator_phone"] = cr.Phone
		v["bank_name"] = cr.BankName
		v["bank_branch"] = cr.BankBranch
		v["id_card_no_masked"] = cr.IDCardNoMasked
		if s.cryptor != nil && cr.BankCardNoEnc != "" {
			if full, err := s.cryptor.Decrypt(cr.BankCardNoEnc); err == nil && full != "" {
				v["bank_card_no"] = full
				v["bank_card_no_full"] = full
			}
		}
	}
	response.OK(c, v)
}

type withdrawalRemarkRequest struct {
	Remark string `json:"remark"`
}

type withdrawalPaidRequest struct {
	TransactionNo string `json:"transaction_no" binding:"required"`
	Remark        string `json:"remark"`
}

// withdrawalApproveRequest 审核通过可选随手带上银行付款流水号：
// 传了流水号 = 审核通过并同步打款（一步到位）；不传 = 仅通过、转 approved 等后续 mark-paid（两步）。
type withdrawalApproveRequest struct {
	TransactionNo string `json:"transaction_no"`
	Remark        string `json:"remark"`
}

// markWithdrawalPaidTx 在事务内把提现置为已打款：校验冻结充足 → 扣减冻结 → 写流水号/备注/打款时间。
// 若该单还没经过 approve（pending 直接打款、或 approve 同步打款），顺带补记审核人与审核时间。
// 调用方需在事务内、对该 withdrawal 已加行锁后传入 w。
func markWithdrawalPaidTx(tx *gorm.DB, w *model.Withdrawal, aid uint64, transactionNo, remark string) error {
	var creator model.Creator
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&creator, w.CreatorID).Error; err != nil {
		return err
	}
	if creator.FrozenCents < w.AmountCents {
		return errFrozenInsufficient
	}
	if err := tx.Model(&model.Creator{}).Where("id = ?", w.CreatorID).
		Update("frozen_cents", gorm.Expr("frozen_cents - ?", w.AmountCents)).Error; err != nil {
		return err
	}
	now := time.Now()
	updates := map[string]interface{}{
		"status":         model.WithdrawalStatusPaid,
		"transaction_no": transactionNo,
		"paid_at":        now,
	}
	if remark != "" {
		updates["remark"] = remark
	}
	if w.ReviewedAt == nil { // pending 直接打款时补审核轨迹
		updates["reviewed_by"] = aid
		updates["reviewed_at"] = now
	}
	if err := tx.Model(w).Updates(updates).Error; err != nil {
		return err
	}
	// 0.14.0 发票联动：提现打款 → 发票 approved
	if w.InvoiceID != nil {
		tx.Model(&model.Invoice{}).Where("id = ? AND status = ?", *w.InvoiceID, model.InvoiceStatusPending).
			Updates(map[string]interface{}{
				"status":      model.InvoiceStatusApproved,
				"reviewed_by": aid,
				"reviewed_at": now,
			})
	}
	return nil
}

func (s *Server) adminApproveWithdrawal(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var req withdrawalApproveRequest
	_ = c.ShouldBindJSON(&req) // body 可选：带 transaction_no = 通过并打款；不带 = 仅通过
	transactionNo := strings.TrimSpace(req.TransactionNo)
	aid := middleware.CurrentID(c)
	paid := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var w model.Withdrawal
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&w, id).Error; err != nil {
			return err
		}
		if w.Status != model.WithdrawalStatusPending {
			return errWithdrawalStatus
		}
		if transactionNo != "" {
			paid = true
			return markWithdrawalPaidTx(tx, &w, aid, transactionNo, req.Remark)
		}
		now := time.Now()
		updates := map[string]interface{}{
			"status":      model.WithdrawalStatusApproved,
			"reviewed_by": aid,
			"reviewed_at": now,
		}
		if strings.TrimSpace(req.Remark) != "" {
			updates["remark"] = req.Remark
		}
		if err := tx.Model(&w).Updates(updates).Error; err != nil {
			return err
		}
		// 0.14.0 发票联动：提现通过 → 发票 approved
		if w.InvoiceID != nil {
			tx.Model(&model.Invoice{}).Where("id = ? AND status = ?", *w.InvoiceID, model.InvoiceStatusPending).
				Updates(map[string]interface{}{
					"status":      model.InvoiceStatusApproved,
					"reviewed_by": aid,
					"reviewed_at": now,
				})
		}
		return nil
	})
	if err == nil {
		if paid {
			s.notifyWithdrawal(id, "提现已打款", "您的提现（%s）已完成打款，请注意查收。")
		} else {
			s.notifyWithdrawal(id, "提现申请已通过", "您的提现申请（%s）已通过审核，等待打款。")
		}
		// 2026-07-06 加 P1-5：时间线
		if paid {
			// 一步到位的：通过+打款
			s.recordTransition("withdrawal", id, model.WithdrawalStatusPending, model.WithdrawalStatusPaid, "admin", &aid, "财务一步通过+打款", map[string]interface{}{
				"transaction_no": transactionNo,
			})
		} else {
			s.recordTransition("withdrawal", id, model.WithdrawalStatusPending, model.WithdrawalStatusApproved, "admin", &aid, "财务审核通过提现", map[string]interface{}{
				"remark": req.Remark,
			})
		}
	}
	s.respondWithdrawalResult(c, id, err)
}

// === 2026-07-03 改：发票和提现一体，财务只审提现 ===
// adminReviewWithdrawal —— POST /v1/admin/withdrawals/:id/review
// 同事反馈：「不能单独审核发票，发票和提现分开审核太复杂」
// 行为：财务只传一个 action，提现和发票"绑定"地一起变：
//   - action=approve：withdrawal → approved (或 paid)，关联 invoice → approved
//   - action=reject：withdrawal → rejected (frozen→balance 回退)，关联 invoice → rejected（创作者可重新提现）
// 入参：
//
//	{
//	  "action":      "approve" | "reject",  // 必填
//	  "transaction_no": "...",  // 可选；带则直接打款
//	  "remark":      "...",
//	}
//
// 兼容：旧版同时支持 action / withdrawal_action 入参（前端可以平滑迁移）
func (s *Server) adminReviewWithdrawal(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var req struct {
		// 2026-07-03 改：只用一个 action（提现和发票联动）
		Action string `json:"action" binding:"required,oneof=approve reject"`
		// 兼容：老接口 withdrawal_action（财务后台老页面可能还传这个）
		WithdrawalAction string `json:"withdrawal_action"`
		// 2026-07-03 改：删 invoice_action（不能再单独审发票）
		TransactionNo string `json:"transaction_no"` // 带则直接打款
		Remark        string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "action 必填，取值 approve/reject")
		return
	}
	// 兼容：优先取新字段 action，没有再回退老字段 withdrawal_action
	if req.Action == "" && req.WithdrawalAction != "" {
		req.Action = req.WithdrawalAction
	}
	aid := middleware.CurrentID(c)
	now := time.Now()
	paid := false
	var invoiceChangedTo string
	var withdrawalChangedTo string

	err := s.db.Transaction(func(tx *gorm.DB) error {
		var w model.Withdrawal
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&w, id).Error; err != nil {
			return err
		}
		if w.Status != model.WithdrawalStatusPending {
			return errWithdrawalStatus
		}
		// === 2026-07-03 改：发票和提现一体，发票状态跟随提现 ===
		// 锁定关联 invoice（如果存在）；后续统一改 status
		var inv *model.Invoice
		if w.InvoiceID != nil {
			var loaded model.Invoice
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&loaded, *w.InvoiceID).Error; err != nil {
				return err
			}
			inv = &loaded
		}
		if req.Action == "reject" {
			// 驳回：frozen → balance 回退；invoice → rejected（创作者可重新提现）
			var creator model.Creator
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				First(&creator, w.CreatorID).Error; err != nil {
				return err
			}
			if creator.FrozenCents < w.AmountCents {
				return errFrozenInsufficient
			}
			if err := tx.Model(&model.Creator{}).Where("id = ?", w.CreatorID).
				Updates(map[string]interface{}{
					"balance_cents": gorm.Expr("balance_cents + ?", w.AmountCents),
					"frozen_cents":  gorm.Expr("frozen_cents - ?", w.AmountCents),
				}).Error; err != nil {
				return err
			}
			if err := tx.Model(&w).Updates(map[string]interface{}{
				"status":      model.WithdrawalStatusRejected,
				"remark":      req.Remark,
				"reviewed_by": aid,
				"reviewed_at": now,
			}).Error; err != nil {
				return err
			}
			withdrawalChangedTo = model.WithdrawalStatusRejected
			// 发票跟随 rejected（可重用：创作者下次可以再传同张 invoice 提现）
			if inv != nil && inv.Status == model.InvoiceStatusPending {
				invUpd := map[string]interface{}{
					"status":        model.InvoiceStatusRejected,
					"reviewed_by":   aid,
					"reviewed_at":   now,
					"reject_reason": req.Remark,
				}
				if err := tx.Model(inv).Updates(invUpd).Error; err != nil {
					return err
				}
				invoiceChangedTo = model.InvoiceStatusRejected
			}
			return nil
		}
		// approve 分支
		// 1) 先把 invoice 推到 approved（如果还在 pending；approved 跳过；rejected 报错）
		if inv != nil {
			if inv.Status == model.InvoiceStatusPending {
				invUpd := map[string]interface{}{
					"status":      model.InvoiceStatusApproved,
					"reviewed_by": aid,
					"reviewed_at": now,
				}
				if err := tx.Model(inv).Updates(invUpd).Error; err != nil {
					return err
				}
				invoiceChangedTo = model.InvoiceStatusApproved
			} else if inv.Status == model.InvoiceStatusRejected {
				// 提现要过但发票已被驳回——不允许（创作者应该重新提交发票再提现）
				return errInvoiceStatus
			}
		}
		// 2) 提现过：带 transaction_no 直接打款，否则只 approved
		transactionNo := strings.TrimSpace(req.TransactionNo)
		if transactionNo != "" {
			paid = true
			return markWithdrawalPaidTx(tx, &w, aid, transactionNo, req.Remark)
		}
		upd := map[string]interface{}{
			"status":      model.WithdrawalStatusApproved,
			"reviewed_by": aid,
			"reviewed_at": now,
		}
		if strings.TrimSpace(req.Remark) != "" {
			upd["remark"] = req.Remark
		}
		if err := tx.Model(&w).Updates(upd).Error; err != nil {
			return err
		}
		withdrawalChangedTo = model.WithdrawalStatusApproved
		return nil
	})
	if err == nil {
		// 通知文案按最终结果选
		switch {
		case paid:
			s.notifyWithdrawal(id, "提现已打款", "您的提现（%s）已完成打款，请注意查收。")
		case withdrawalChangedTo == model.WithdrawalStatusApproved:
			s.notifyWithdrawal(id, "提现申请已通过", "您的提现申请（%s）已通过审核，等待打款。")
		case withdrawalChangedTo == model.WithdrawalStatusRejected:
			s.notifyWithdrawal(id, "提现申请被驳回", "您的提现申请（%s）被驳回，金额已退回可用余额。")
		}
		// 2026-07-06 加 P1-5：时间线（review 接口走的是 7/3 改的"动作合一"逻辑）
		if withdrawalChangedTo != "" {
			s.recordTransition("withdrawal", id, model.WithdrawalStatusPending, withdrawalChangedTo, "admin", &aid, "财务审核提现（review 接口）", map[string]interface{}{
				"remark":         req.Remark,
				"paid_immediate": paid,
			})
		}
		// invoice 联动状态变化（如果 withdrawal 关联了 invoice 且 invoice 状态真的变了）
		if invoiceChangedTo != "" {
			var w model.Withdrawal
			if err := s.db.First(&w, id).Error; err == nil && w.InvoiceID != nil {
				s.recordTransition("invoice", *w.InvoiceID, model.InvoiceStatusPending, invoiceChangedTo, "admin", &aid, "发票随提现审核联动变更", map[string]interface{}{
					"reason":         req.Remark,
					"via_withdrawal": id,
				})
			}
		}
	}
	// 扩展响应：附带 invoice_action 的最终结果
	c.Header("X-Withdrawal-Status", withdrawalChangedTo)
	if invoiceChangedTo != "" {
		c.Header("X-Invoice-Status", invoiceChangedTo)
	}
	s.respondWithdrawalResult(c, id, err)
}

// notifyWithdrawal 按 withdrawal id 读取创作者与金额，给创作者发一条提现状态消息。
// tmpl 中的 %s 会被替换成金额（¥x.xx）。
func (s *Server) notifyWithdrawal(id uint64, title, tmpl string) {
	var w model.Withdrawal
	if err := s.db.First(&w, id).Error; err != nil {
		return
	}
	content := tmpl
	if strings.Contains(tmpl, "%s") {
		content = fmt.Sprintf(tmpl, yuanStr(w.AmountCents))
	}
	if w.Remark != "" {
		content += "备注：" + w.Remark
	}
	s.sendNotification(w.CreatorID, title, content, "")
}

func (s *Server) adminRejectWithdrawal(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var req withdrawalRemarkRequest
	_ = c.ShouldBindJSON(&req)

	aid := middleware.CurrentID(c)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var w model.Withdrawal
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&w, id).Error; err != nil {
			return err
		}
		if w.Status != model.WithdrawalStatusPending {
			return errWithdrawalStatus
		}
		// frozen → balance 回退
		var creator model.Creator
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&creator, w.CreatorID).Error; err != nil {
			return err
		}
		if creator.FrozenCents < w.AmountCents {
			return errFrozenInsufficient
		}
		if err := tx.Model(&model.Creator{}).Where("id = ?", w.CreatorID).
			Updates(map[string]interface{}{
				"balance_cents": gorm.Expr("balance_cents + ?", w.AmountCents),
				"frozen_cents":  gorm.Expr("frozen_cents - ?", w.AmountCents),
			}).Error; err != nil {
			return err
		}
		now := time.Now()
	if err := tx.Model(&w).Updates(map[string]interface{}{
		"status":      model.WithdrawalStatusRejected,
		"remark":      req.Remark,
		"reviewed_by": aid,
		"reviewed_at": now,
	}).Error; err != nil {
		return err
	}
	// 0.14.0 发票联动：提现驳回 → 发票 rejected
	if w.InvoiceID != nil {
		tx.Model(&model.Invoice{}).Where("id = ? AND status = ?", *w.InvoiceID, model.InvoiceStatusPending).
			Updates(map[string]interface{}{
				"status":        model.InvoiceStatusRejected,
				"reviewed_by":   aid,
				"reviewed_at":   now,
				"reject_reason": req.Remark,
			})
	}
	return nil
})
	if err == nil {
		s.notifyWithdrawal(id, "提现申请被驳回", "您的提现申请（%s）被驳回，金额已退回可用余额。")
		// 2026-07-06 加 P1-5：时间线
		s.recordTransition("withdrawal", id, model.WithdrawalStatusPending, model.WithdrawalStatusRejected, "admin", &aid, "财务驳回提现", map[string]interface{}{
			"remark": req.Remark,
		})
	}
	s.respondWithdrawalResult(c, id, err)
}

func (s *Server) adminMarkWithdrawalPaid(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var req withdrawalPaidRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "transaction_no 必填")
		return
	}
	transactionNo := strings.TrimSpace(req.TransactionNo)
	if transactionNo == "" {
		response.InvalidParam(c, "transaction_no 必填")
		return
	}

	aid := middleware.CurrentID(c)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var w model.Withdrawal
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&w, id).Error; err != nil {
			return err
		}
		// 允许从 approved（先审后打）或 pending（审核+打款一步）直接打款，避免"通过后无处填流水号"。
		if w.Status != model.WithdrawalStatusApproved && w.Status != model.WithdrawalStatusPending {
			return errWithdrawalStatus
		}
		return markWithdrawalPaidTx(tx, &w, aid, transactionNo, req.Remark)
	})
	if err == nil {
		s.notifyWithdrawal(id, "提现已打款", "您的提现（%s）已完成打款，请注意查收。")
		// 2026-07-06 加 P1-5：时间线
		// 注意：from 状态可能是 approved（先审后打）或 pending（一步到位的打款）—— 查表拿真实值
		var wNow model.Withdrawal
		if err := s.db.First(&wNow, id).Error; err == nil {
			s.recordTransition("withdrawal", id, wNow.Status, model.WithdrawalStatusPaid, "admin", &aid, "财务完成打款", map[string]interface{}{
				"transaction_no": transactionNo,
				"paid_at":        wNow.PaidAt,
			})
			// 关联的 settlement：invoiced → paid（如果未变）
			if wNow.InvoiceID != nil {
				var inv model.Invoice
				if err := s.db.First(&inv, *wNow.InvoiceID).Error; err == nil && inv.SettlementID > 0 {
					var stNow model.Settlement
					if err := s.db.First(&stNow, inv.SettlementID).Error; err == nil && stNow.Status != model.SettlementStatusPaid {
						s.recordTransition("settlement", inv.SettlementID, stNow.Status, model.SettlementStatusPaid, "admin", &aid, "结算单完结（打款完成）", map[string]interface{}{
							"withdrawal_id": id,
						})
					}
				}
			}
		}
	}
	s.respondWithdrawalResult(c, id, err)
}

func (s *Server) respondWithdrawalResult(c *gin.Context, id uint64, err error) {
	if err != nil {
		switch {
		case errors.Is(err, errWithdrawalStatus):
			response.Conflict(c, "当前状态不允许该操作")
		case errors.Is(err, errFrozenInsufficient):
			response.Conflict(c, "创作者冻结余额不足，账目异常，请先对账")
		case errors.Is(err, errInvoiceStatus):
			response.Conflict(c, "该发票已审核过（approved/rejected），请创作者重新提交新发票后再提现")
		case errors.Is(err, gorm.ErrRecordNotFound):
			response.NotFound(c, "提现申请不存在")
		default:
			response.ServerError(c, "操作失败")
		}
		return
	}
	var w model.Withdrawal
	s.db.First(&w, id)
	view := s.withdrawalView(w)
	view["creator_id"] = w.CreatorID
	response.OK(c, view)
}

var (
	errWithdrawalStatus   = errors.New("withdrawal status invalid")
	errFrozenInsufficient = errors.New("frozen balance insufficient")
	// 2026-07-03 改：发票状态非法（如 rejected 不能再 approve）
	errInvoiceStatus = errors.New("invoice status invalid for review")
)

// === 订单退款 / 主动查单 (管理端) ===

type adminRefundOrderRequest struct {
	AmountCents int64  `json:"amount_cents" binding:"required,gt=0"`
	Reason      string `json:"reason"`
	// 客户端可显式带 refund_no 做强幂等;为空时由服务端生成"REF-{orderNo}-{ts}-{rand}"。
	RefundNo string `json:"refund_no"`
}

// adminRefundOrder POST /v1/admin/orders/:order_no/refund
// 仅财务角色可调用。支持部分退;同一 refund_no 重入幂等。
func (s *Server) adminRefundOrder(c *gin.Context) {
	orderNo := c.Param("order_no")
	if orderNo == "" {
		response.InvalidParam(c, "order_no 必填")
		return
	}
	var req adminRefundOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法:"+err.Error())
		return
	}
	refundNo := strings.TrimSpace(req.RefundNo)
	if refundNo == "" {
		refundNo = generateRefundNo(orderNo)
	}

	order, err := s.billing.RefundOrder(orderNo, refundNo, req.AmountCents, req.Reason)
	if err != nil {
		switch {
		case errors.Is(err, billing.ErrOrderNotFound):
			response.NotFound(c, "订单不存在")
		case errors.Is(err, billing.ErrRefundNotAllowed):
			response.Conflict(c, "订单状态不允许退款")
		case errors.Is(err, billing.ErrRefundAmountInvalid):
			response.InvalidParam(c, "退款金额非法,可能超出剩余可退金额")
		case errors.Is(err, billing.ErrRefundNoRequired):
			response.InvalidParam(c, "refund_no 必填")
		case errors.Is(err, payment.ErrRefundFailed):
			response.ServerError(c, "渠道侧退款失败,请稍后重试或查询渠道日志")
		case errors.Is(err, payment.ErrProviderUnavailable):
			response.ServerError(c, "支付渠道不可用,请检查密钥配置")
		default:
			response.ServerError(c, "退款失败:"+err.Error())
		}
		return
	}
	response.OK(c, gin.H{
		"order_no":            order.OrderNo,
		"status":              order.Status,
		"amount_cents":        order.AmountCents,
		"refund_amount_cents": order.RefundAmountCents,
		"refund_no":           order.RefundNo,
		"platform_refund_no":  order.PlatformRefundNo,
		"refunded_at":         order.RefundedAt,
		"refund_reason":       order.RefundReason,
	})
}

// adminSyncOrder POST /v1/admin/orders/:order_no/sync
// 兜底:webhook 长时间未到或丢失时,主动调渠道查单回写本地。
func (s *Server) adminSyncOrder(c *gin.Context) {
	orderNo := c.Param("order_no")
	if orderNo == "" {
		response.InvalidParam(c, "order_no 必填")
		return
	}
	order, err := s.billing.SyncOrderStatus(orderNo)
	if err != nil {
		switch {
		case errors.Is(err, billing.ErrOrderNotFound):
			response.NotFound(c, "订单不存在")
		case errors.Is(err, payment.ErrUnsupportedMethod):
			response.InvalidParam(c, "订单支付方式不支持查单")
		case errors.Is(err, payment.ErrProviderUnavailable):
			response.ServerError(c, "支付渠道不可用,请检查密钥配置")
		default:
			response.ServerError(c, "查单失败:"+err.Error())
		}
		return
	}
	response.OK(c, gin.H{
		"order_no":            order.OrderNo,
		"status":              order.Status,
		"amount_cents":        order.AmountCents,
		"platform_trade_no":   order.PlatformTradeNo,
		"paid_at":             order.PaidAt,
		"refund_amount_cents": order.RefundAmountCents,
		"refunded_at":         order.RefundedAt,
	})
}

// generateRefundNo 退款单号:REF-{orderNo}-{Unix 秒}-{4 位随机}。
// 仅作幂等键,不进 DB 唯一约束(同一笔多次部分退款会有多个 refund_no);
// 客户端可自行传入以做强幂等。
func generateRefundNo(orderNo string) string {
	return fmt.Sprintf("REF-%s-%d-%04d", orderNo, time.Now().Unix(), rand.Intn(10000))
}
