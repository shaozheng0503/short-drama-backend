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
		pendingDramaCount                   int64
		pendingWithdrawCount                int64
		todayIncome                         int64
		todayPlay                           int64
	)
	s.db.Model(&model.User{}).Count(&userCount)
	s.db.Model(&model.Creator{}).Count(&creatorCount)
	s.db.Model(&model.Drama{}).Count(&dramaCount)
	s.db.Model(&model.Drama{}).Where("status IN ?", []string{model.DramaStatusReviewing, model.DramaStatusAwaitingPublish}).Count(&pendingDramaCount)
	s.db.Model(&model.Withdrawal{}).Where("status = ?", model.WithdrawalStatusPending).Count(&pendingWithdrawCount)
	today := time.Now().Format("2006-01-02")
	s.db.Model(&model.CreatorStatsDaily{}).Where("stat_date = ?", today).
		Select("COALESCE(SUM(income_cents),0)").Scan(&todayIncome)
	s.db.Model(&model.CreatorStatsDaily{}).Where("stat_date = ?", today).
		Select("COALESCE(SUM(play_count),0)").Scan(&todayPlay)

	response.OK(c, gin.H{
		"user_count":               userCount,
		"creator_count":            creatorCount,
		"drama_count":              dramaCount,
		"today_play_count":         todayPlay,
		"today_income_cents":       todayIncome,
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
	Reason string `json:"reason" binding:"required"`
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
	if err := s.db.Model(&cr).Updates(map[string]interface{}{
		"verify_status":        model.CreatorVerifyRejected,
		"verify_reject_reason": strings.TrimSpace(req.Reason),
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
	var total int64
	q.Count(&total)
	var orders []model.Order
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&orders)
	list := make([]gin.H, 0, len(orders))
	for _, o := range orders {
		list = append(list, gin.H{
			"order_no":          o.OrderNo,
			"user_id":           o.UserID,
			"drama_id":          o.DramaID,
			"episode_id":        o.EpisodeID,
			"amount_cents":      o.AmountCents,
			"payment_method":    o.PaymentMethod,
			"status":            o.Status,
			"platform_trade_no": o.PlatformTradeNo,
			"paid_at":           o.PaidAt,
			"created_at":        o.CreatedAt,
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
	response.OK(c, gin.H{
		"order_no":          o.OrderNo,
		"user_id":           o.UserID,
		"drama_id":          o.DramaID,
		"episode_id":        o.EpisodeID,
		"product_id":        o.ProductID,
		"amount_cents":      o.AmountCents,
		"payment_method":    o.PaymentMethod,
		"status":            o.Status,
		"platform_trade_no": o.PlatformTradeNo,
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
	list := make([]gin.H, 0, len(items))
	for _, w := range items {
		v := s.withdrawalView(w)
		v["creator_id"] = w.CreatorID
		list = append(list, v)
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

type withdrawalRemarkRequest struct {
	Remark string `json:"remark"`
}

type withdrawalPaidRequest struct {
	TransactionNo string `json:"transaction_no" binding:"required"`
	Remark        string `json:"remark"`
}

func (s *Server) adminApproveWithdrawal(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	aid := middleware.CurrentID(c)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var w model.Withdrawal
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&w, id).Error; err != nil {
			return err
		}
		if w.Status != model.WithdrawalStatusPending {
			return errWithdrawalStatus
		}
		now := time.Now()
		return tx.Model(&w).Updates(map[string]interface{}{
			"status":      model.WithdrawalStatusApproved,
			"reviewed_by": aid,
			"reviewed_at": now,
		}).Error
	})
	if err == nil {
		s.notifyWithdrawal(id, "提现申请已通过", "您的提现申请（%s）已通过审核，等待打款。")
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
		return tx.Model(&w).Updates(map[string]interface{}{
			"status":      model.WithdrawalStatusRejected,
			"remark":      req.Remark,
			"reviewed_by": aid,
			"reviewed_at": now,
		}).Error
	})
	if err == nil {
		s.notifyWithdrawal(id, "提现申请被驳回", "您的提现申请（%s）被驳回，金额已退回可用余额。")
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

	err := s.db.Transaction(func(tx *gorm.DB) error {
		var w model.Withdrawal
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&w, id).Error; err != nil {
			return err
		}
		if w.Status != model.WithdrawalStatusApproved {
			return errWithdrawalStatus
		}
		var creator model.Creator
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&creator, w.CreatorID).Error; err != nil {
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
		return tx.Model(&w).Updates(map[string]interface{}{
			"status":         model.WithdrawalStatusPaid,
			"transaction_no": req.TransactionNo,
			"remark":         req.Remark,
			"paid_at":        now,
		}).Error
	})
	if err == nil {
		s.notifyWithdrawal(id, "提现已打款", "您的提现（%s）已完成打款，请注意查收。")
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
