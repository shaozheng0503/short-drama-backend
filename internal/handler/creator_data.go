package handler

import (
	"fmt"
	"regexp"
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"
	"ai-drama-platform/internal/secure"
	"ai-drama-platform/internal/sms"

	"github.com/gin-gonic/gin"
)

func (s *Server) creatorDashboard(c *gin.Context) {
	cid := middleware.CurrentID(c)
	var creator model.Creator
	if err := s.db.First(&creator, cid).Error; err != nil {
		response.ServerError(c, "查询创作者失败")
		return
	}

	var dramaCount int64
	s.db.Model(&model.Drama{}).Where("creator_id = ?", cid).Count(&dramaCount)

	var totalPlay int64
	s.db.Model(&model.Drama{}).Where("creator_id = ?", cid).
		Select("COALESCE(SUM(play_count),0)").Scan(&totalPlay)

	today := time.Now().Format("2006-01-02")
	var todayIncome int64
	s.db.Model(&model.CreatorStatsDaily{}).
		Where("creator_id = ? AND stat_date = ?", cid, today).
		Select("COALESCE(SUM(income_cents),0)").Scan(&todayIncome)
	var todayPlay int64
	s.db.Model(&model.CreatorStatsDaily{}).
		Where("creator_id = ? AND stat_date = ?", cid, today).
		Select("COALESCE(SUM(play_count),0)").Scan(&todayPlay)

	response.OK(c, gin.H{
		"total_income_cents": creator.TotalIncomeCents,
		"balance_cents":      creator.BalanceCents,
		"frozen_cents":       creator.FrozenCents,
		"drama_count":        dramaCount,
		"total_play_count":   totalPlay,
		"today_income_cents": todayIncome,
		"today_play_count":   todayPlay,
	})
}

func (s *Server) creatorListDramas(c *gin.Context) {
	cid := middleware.CurrentID(c)
	page, pageSize := paginate(c)

	q := s.db.Model(&model.Drama{}).Where("creator_id = ?", cid)
	var total int64
	q.Count(&total)
	var dramas []model.Drama
	if err := q.Order("updated_at desc").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&dramas).Error; err != nil {
		response.ServerError(c, "查询失败")
		return
	}

	dramaIDs := make([]uint64, 0, len(dramas))
	for _, d := range dramas {
		dramaIDs = append(dramaIDs, d.ID)
	}

	// 一次性预取列表渲染需要的关联：收益 / 多分类 / 合同绑定。
	// 都用 IN 批查，避免 N+1。
	incomes := map[uint64]int64{}
	categoriesByDrama := map[uint64][]gin.H{}
	contractStatus := map[uint64]string{}
	if len(dramaIDs) > 0 {
		// 收益
		var incomeRows []struct {
			DramaID uint64
			Income  int64
		}
		s.db.Table("creator_stats_daily").
			Select("drama_id, COALESCE(SUM(income_cents),0) as income").
			Where("creator_id = ? AND drama_id IN ?", cid, dramaIDs).
			Group("drama_id").Scan(&incomeRows)
		for _, r := range incomeRows {
			incomes[r.DramaID] = r.Income
		}

		// 多分类：drama_tags JOIN categories 拿名字+维度，按 sort_order 排
		var catRows []struct {
			DramaID    uint64
			CategoryID uint64
			Name       string
			Type       string
		}
		s.db.Table("drama_tags").
			Select("drama_tags.drama_id, categories.id as category_id, categories.name, categories.type").
			Joins("JOIN categories ON categories.id = drama_tags.category_id").
			Where("drama_tags.drama_id IN ?", dramaIDs).
			Order("categories.type asc, categories.sort_order asc").
			Scan(&catRows)
		for _, r := range catRows {
			categoriesByDrama[r.DramaID] = append(categoriesByDrama[r.DramaID], gin.H{
				"id":   r.CategoryID,
				"name": r.Name,
				"type": r.Type,
			})
		}

		// 合同状态：当前 creator 名下、绑该剧的最近一份合同的 status；没绑 → 空串（前端展示"未绑定"）
		var contractRows []struct {
			DramaID uint64
			Status  string
		}
		s.db.Table("contracts").
			Select("drama_id, status").
			Where("creator_id = ? AND drama_id IN ?", cid, dramaIDs).
			Order("updated_at desc").
			Scan(&contractRows)
		for _, r := range contractRows {
			if _, ok := contractStatus[r.DramaID]; !ok { // 最近一份覆盖
				contractStatus[r.DramaID] = r.Status
			}
		}
	}

	list := make([]gin.H, 0, len(dramas))
	for _, d := range dramas {
		cats := categoriesByDrama[d.ID]
		if cats == nil {
			cats = []gin.H{} // 前端遍历友好：永远是数组，不出现 null
		}
		list = append(list, gin.H{
			"id":                   d.ID,
			"title":                d.Title,
			"cover_url":            d.CoverURL,
			"status":               d.Status,
			"audit_status":         d.AuditStatus,
			"audit_reason":         d.AuditReason,
			"total_episodes":       d.TotalEpisodes,
			"audience":             d.Audience,
			"categories":           cats,            // [{id,name,type}...]，含 theme/setting/background/audience 四维全部命中标签
			"contract_status":      contractStatus[d.ID], // pending/signing/signed/cancelled；空串=未绑定
			"publish_type":         d.PublishType,
			"scheduled_publish_at": d.ScheduledPublishAt,
			"play_count":           d.PlayCount,
			"income_cents":         incomes[d.ID],
			"created_at":           d.CreatedAt,
			"updated_at":           d.UpdatedAt,
		})
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

func (s *Server) creatorDramaStats(c *gin.Context) {
	cid := middleware.CurrentID(c)
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var drama model.Drama
	if err := s.db.First(&drama, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "短剧不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	if drama.CreatorID == nil || *drama.CreatorID != cid {
		response.Forbidden(c, "无权查看该短剧")
		return
	}

	// 默认 7d
	days := 7
	if v := c.Query("range"); v != "" {
		if v == "30d" {
			days = 30
		} else if v == "7d" {
			days = 7
		}
	}
	since := time.Now().AddDate(0, 0, -days).Format("2006-01-02")

	var summaryPlay, summaryIncome int64
	s.db.Model(&model.CreatorStatsDaily{}).
		Where("creator_id = ? AND drama_id = ?", cid, drama.ID).
		Select("COALESCE(SUM(play_count),0)").Scan(&summaryPlay)
	s.db.Model(&model.CreatorStatsDaily{}).
		Where("creator_id = ? AND drama_id = ?", cid, drama.ID).
		Select("COALESCE(SUM(income_cents),0)").Scan(&summaryIncome)

	var unlockCount int64
	s.db.Model(&model.EpisodeUnlock{}).Where("drama_id = ?", drama.ID).Count(&unlockCount)

	var daily []model.CreatorStatsDaily
	s.db.Where("creator_id = ? AND drama_id = ? AND stat_date >= ?", cid, drama.ID, since).
		Order("stat_date asc").Find(&daily)
	dailyOut := make([]gin.H, 0, len(daily))
	for _, d := range daily {
		dailyOut = append(dailyOut, gin.H{
			"date":         d.StatDate,
			"play_count":   d.PlayCount,
			"income_cents": d.IncomeCents,
		})
	}

	response.OK(c, gin.H{
		"drama": gin.H{"id": drama.ID, "title": drama.Title},
		"summary": gin.H{
			"play_count":   summaryPlay,
			"income_cents": summaryIncome,
			"unlock_count": unlockCount,
		},
		"daily": dailyOut,
	})
}

// creatorIncome —— GET /v1/creator/income
// 返回创作者作品明细（按 日 × 作品 拆行）。每行字段：date / drama_id / drama_title / play_count / income_cents。
// 数据源 creator_stats_daily 的唯一键就是 (creator,drama,date)，无需再聚合，按日倒序、同日内按 drama_id 升序输出。
func (s *Server) creatorIncome(c *gin.Context) {
	cid := middleware.CurrentID(c)
	page, pageSize := paginate(c)

	type aggRow struct {
		StatDate    string `gorm:"column:stat_date"`
		DramaID     uint64 `gorm:"column:drama_id"`
		DramaTitle  string `gorm:"column:drama_title"`
		PlayCount   int64  `gorm:"column:play_count"`
		IncomeCents int64  `gorm:"column:income_cents"`
	}

	var total int64
	s.db.Model(&model.CreatorStatsDaily{}).Where("creator_id = ?", cid).Count(&total)

	var rows []aggRow
	s.db.Table("creator_stats_daily AS csd").
		Select("csd.stat_date, csd.drama_id, COALESCE(d.title,'') AS drama_title, csd.play_count, csd.income_cents").
		Joins("LEFT JOIN dramas AS d ON d.id = csd.drama_id").
		Where("csd.creator_id = ?", cid).
		Order("csd.stat_date desc, csd.drama_id asc").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Scan(&rows)

	list := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		list = append(list, gin.H{
			"date":         r.StatDate,
			"drama_id":     r.DramaID,
			"drama_title":  r.DramaTitle,
			"play_count":   r.PlayCount,
			"income_cents": r.IncomeCents,
		})
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

type creatorProfileRequest struct {
	Name               *string `json:"name"`
	Nickname           *string `json:"nickname"`
	AvatarURL          *string `json:"avatar_url"`
	AccountUID         *string `json:"account_uid"`
	CreatorType        *string `json:"creator_type"`         // personal / organization
	OrgName            *string `json:"org_name"`             // 机构名称（机构类型）
	OrgCreditCode      *string `json:"org_credit_code"`      // 统一社会信用代码
	BusinessLicenseURL *string `json:"business_license_url"` // 营业执照图片 URL
	IdentityMID        *string `json:"identity_mid"`         // 创作者身份信息 MID
	IdentityRole       *string `json:"identity_role"`        // 版权人 / 制作方等
	IDCardNo           *string `json:"id_card_no"`
	BankName           *string `json:"bank_name"`
	BankCardNo         *string `json:"bank_card_no"`
	SMSCode            *string `json:"sms_code"` // 修改已绑定银行卡时必填，scene=bank_card_change
}

// idCardRegex / bankCardRegex 入参做最小可用本地校验。
// MVP 阶段不接腾讯实人 / 银联四要素，但前置格式校验能挡 99% 的乱填，避免：
//   - 脏数据落库后接入实人认证时全部失败
//   - 太长字符串直接吃 DB column 长度上限报 500
var (
	idCardRegex     = regexp.MustCompile(`^[1-9]\d{16}[\dXx]$`) // 18 位，末位允许 X/x
	bankCardRegex   = regexp.MustCompile(`^\d{16,19}$`)
	creditCodeRegex = regexp.MustCompile(`^[0-9A-Z]{18}$`)
)

const (
	creatorNameMaxRune     = 50
	creatorBankNameMaxRune = 50
)

func runeLen(s string) int { return len([]rune(s)) }

func (s *Server) creatorUpdateProfile(c *gin.Context) {
	cid := middleware.CurrentID(c)
	if s.cryptor == nil {
		response.ServerError(c, "服务端未配置加密密钥，暂不能保存敏感资料")
		return
	}
	var req creatorProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}

	// 入参格式校验：长度 / 身份证 / 银行卡。失败统一返 40001，避免吃 DB 错误 → 500。
	if req.Name != nil && *req.Name != "" {
		if runeLen(*req.Name) > creatorNameMaxRune {
			response.InvalidParam(c, fmt.Sprintf("name 长度不能超过 %d 个字符", creatorNameMaxRune))
			return
		}
	}
	if req.Nickname != nil && runeLen(*req.Nickname) > creatorNameMaxRune {
		response.InvalidParam(c, fmt.Sprintf("nickname 长度不能超过 %d 个字符", creatorNameMaxRune))
		return
	}
	if req.AvatarURL != nil && len(*req.AvatarURL) > 512 {
		response.InvalidParam(c, "avatar_url 过长")
		return
	}
	if req.BusinessLicenseURL != nil && len(*req.BusinessLicenseURL) > 512 {
		response.InvalidParam(c, "business_license_url 过长")
		return
	}
	if req.AccountUID != nil {
		response.InvalidParam(c, "account_uid 不允许修改")
		return
	}
	if req.OrgCreditCode != nil && *req.OrgCreditCode != "" {
		if !creditCodeRegex.MatchString(*req.OrgCreditCode) {
			response.InvalidParam(c, "org_credit_code 必须是 18 位大写字母/数字统一社会信用代码")
			return
		}
	}
	if req.IdentityMID != nil && len(*req.IdentityMID) > 64 {
		response.InvalidParam(c, "identity_mid 过长")
		return
	}
	if req.IdentityRole != nil && len(*req.IdentityRole) > 32 {
		response.InvalidParam(c, "identity_role 过长")
		return
	}
	if req.BankName != nil && *req.BankName != "" {
		if runeLen(*req.BankName) > creatorBankNameMaxRune {
			response.InvalidParam(c, fmt.Sprintf("bank_name 长度不能超过 %d 个字符", creatorBankNameMaxRune))
			return
		}
	}
	if req.IDCardNo != nil && *req.IDCardNo != "" {
		if !idCardRegex.MatchString(*req.IDCardNo) {
			response.InvalidParam(c, "id_card_no 必须是 18 位身份证号（末位可为 X）")
			return
		}
	}
	if req.BankCardNo != nil && *req.BankCardNo != "" {
		if !bankCardRegex.MatchString(*req.BankCardNo) {
			response.InvalidParam(c, "bank_card_no 必须是 16-19 位数字")
			return
		}
	}

	// MVP 暂不接实名 / 银行卡四要素，允许"同一次提交完整资料"后自动置为 verified。
	// 分步上传身份证 / 银行卡时不自动认证，避免前端单字段保存误触发认证状态。
	var creator model.Creator
	if err := s.db.First(&creator, cid).Error; err != nil {
		response.ServerError(c, "查询创作者失败")
		return
	}

	if req.CreatorType != nil && *req.CreatorType != "" &&
		*req.CreatorType != model.CreatorTypePersonal && *req.CreatorType != model.CreatorTypeOrganization {
		response.InvalidParam(c, "creator_type 只能是 personal / organization")
		return
	}
	targetCreatorType := creator.CreatorType
	if req.CreatorType != nil && *req.CreatorType != "" {
		targetCreatorType = *req.CreatorType
	}
	if targetCreatorType == "" {
		targetCreatorType = model.CreatorTypePersonal
	}
	hasPersonalPayload := (req.Name != nil && *req.Name != "") || (req.IDCardNo != nil && *req.IDCardNo != "")
	hasEnterprisePayload := (req.OrgName != nil && *req.OrgName != "") ||
		(req.OrgCreditCode != nil && *req.OrgCreditCode != "") ||
		(req.BusinessLicenseURL != nil && *req.BusinessLicenseURL != "")
	if targetCreatorType == model.CreatorTypePersonal && hasEnterprisePayload {
		response.InvalidParam(c, "个人实名与企业认证二选一，personal 类型不能提交企业认证字段")
		return
	}
	if targetCreatorType == model.CreatorTypeOrganization && hasPersonalPayload {
		response.InvalidParam(c, "个人实名与企业认证二选一，organization 类型不能提交真实姓名/身份证字段")
		return
	}

	updates := map[string]interface{}{}
	if req.Name != nil && *req.Name != "" {
		updates["name"] = *req.Name
	}
	if req.Nickname != nil {
		updates["nickname"] = *req.Nickname
	}
	if req.AvatarURL != nil {
		updates["avatar_url"] = *req.AvatarURL
	}
	if req.CreatorType != nil && *req.CreatorType != "" {
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
	if req.IdentityMID != nil {
		updates["identity_mid"] = *req.IdentityMID
	}
	if req.IdentityRole != nil {
		updates["identity_role"] = *req.IdentityRole
	}
	if req.BankName != nil {
		if *req.BankName == "" && creator.VerifyStatus == model.CreatorVerifyVerified {
			response.InvalidParam(c, "已实名的创作者不能清空 bank_name；如需修改请填新值")
			return
		}
		updates["bank_name"] = *req.BankName
	}
	if req.IDCardNo != nil && *req.IDCardNo != "" {
		enc, err := s.cryptor.Encrypt(*req.IDCardNo)
		if err != nil {
			response.ServerError(c, "身份证加密失败")
			return
		}
		updates["id_card_no_enc"] = enc
		updates["id_card_no_masked"] = maskIDCard(*req.IDCardNo)
	}
	if req.BankCardNo != nil && *req.BankCardNo != "" {
		if creator.BankCardNoEnc != "" && creator.BankCardNoMasked != "" && maskBankCard(*req.BankCardNo) != creator.BankCardNoMasked {
			if req.SMSCode == nil || *req.SMSCode == "" {
				response.InvalidParam(c, "修改银行卡号需先调用 POST /creator/bank-card/send-sms 获取验证码")
				return
			}
			if err := s.sms.Verify(creator.Phone, model.SMSSceneBankCardChange, *req.SMSCode); err != nil {
				response.InvalidParam(c, "短信验证码错误或已过期")
				return
			}
		}
		enc, err := s.cryptor.Encrypt(*req.BankCardNo)
		if err != nil {
			response.ServerError(c, "银行卡加密失败")
			return
		}
		updates["bank_card_no_enc"] = enc
		updates["bank_card_last4"] = secure.Last4(*req.BankCardNo)
		updates["bank_card_no_masked"] = maskBankCard(*req.BankCardNo)
	}

	willName := creator.Name
	if v, ok := updates["name"].(string); ok {
		willName = v
	}
	willIDCard := creator.IDCardNoEnc
	if v, ok := updates["id_card_no_enc"].(string); ok {
		willIDCard = v
	}
	willBankCard := creator.BankCardNoEnc
	if v, ok := updates["bank_card_no_enc"].(string); ok {
		willBankCard = v
	}
	willBankName := creator.BankName
	if v, ok := updates["bank_name"].(string); ok {
		willBankName = v
	}
	submittedCompleteProfile := req.Name != nil && *req.Name != "" &&
		req.IDCardNo != nil && *req.IDCardNo != "" &&
		req.BankName != nil && *req.BankName != "" &&
		req.BankCardNo != nil && *req.BankCardNo != ""
	submittedEnterpriseProfile := req.OrgName != nil && *req.OrgName != "" &&
		req.OrgCreditCode != nil && *req.OrgCreditCode != "" &&
		req.BusinessLicenseURL != nil && *req.BusinessLicenseURL != "" &&
		req.BankName != nil && *req.BankName != "" &&
		req.BankCardNo != nil && *req.BankCardNo != ""
	if creator.VerifyStatus != model.CreatorVerifyVerified &&
		((submittedCompleteProfile && willName != "" && willIDCard != "" && willBankCard != "" && willBankName != "") ||
			(submittedEnterpriseProfile && willBankCard != "" && willBankName != "")) {
		updates["verify_status"] = model.CreatorVerifyPending
		updates["verify_reject_reason"] = ""
		updates["verify_submitted_at"] = nowTimePtr()
	}

	if len(updates) > 0 {
		if err := s.db.Model(&creator).Updates(updates).Error; err != nil {
			if isUniqueViolation(err) {
				response.Conflict(c, "资料冲突")
				return
			}
			response.ServerError(c, "更新资料失败")
			return
		}
	}
	s.db.First(&creator, cid)
	response.OK(c, creatorFullView(creator))
}

func creatorFullView(cr model.Creator) gin.H {
	maskedBank := ""
	if cr.BankCardNoMasked != "" {
		maskedBank = cr.BankCardNoMasked
	} else if cr.BankCardLast4 != "" {
		maskedBank = "***" + cr.BankCardLast4
	}
	return gin.H{
		"id":                   cr.ID,
		"phone":                sms.MaskPhone(cr.Phone),
		"login_phone":          sms.MaskPhone(cr.Phone),
		"name":                 cr.Name,
		"nickname":             cr.Nickname,
		"avatar_url":           creatorAvatarURL(cr),
		"account_uid":          cr.AccountUID,
		"creator_type":         cr.CreatorType,
		"org_name":             cr.OrgName,
		"org_credit_code":      cr.OrgCreditCode,
		"business_license_url": cr.BusinessLicenseURL,
		"identity_mid":         cr.IdentityMID,
		"identity_role":        cr.IdentityRole,
		"bank_name":            cr.BankName,
		"id_card_no_masked":    cr.IDCardNoMasked,
		"bank_card_no_masked":  maskedBank,
		"verify_status":        cr.VerifyStatus,
		"verify_reject_reason": cr.VerifyRejectReason,
		"verify_submitted_at":  cr.VerifySubmittedAt,
		"total_income_cents":   cr.TotalIncomeCents,
		"balance_cents":        cr.BalanceCents,
		"frozen_cents":         cr.FrozenCents,
		"status":               cr.Status,
		"account_info": gin.H{
			"avatar_url":  creatorAvatarURL(cr),
			"nickname":    cr.Nickname,
			"account_uid": cr.AccountUID,
			"login_phone": sms.MaskPhone(cr.Phone),
		},
		"real_name_info": gin.H{
			"real_name":           cr.Name,
			"id_card_no_masked":   cr.IDCardNoMasked,
			"bank_card_no_masked": maskedBank,
			"bank_name":           cr.BankName,
		},
		"enterprise_info": gin.H{
			"org_name":             cr.OrgName,
			"org_credit_code":      cr.OrgCreditCode,
			"business_license_url": cr.BusinessLicenseURL,
		},
		"identity_info": gin.H{
			"identity_mid":  cr.IdentityMID,
			"identity_role": cr.IdentityRole,
		},
	}
}

func maskIDCard(id string) string {
	if len(id) < 10 {
		return "***"
	}
	return id[:6] + "****" + id[len(id)-4:]
}

func maskBankCard(card string) string {
	if len(card) < 8 {
		return "***"
	}
	return card[:4] + "****" + card[len(card)-4:]
}
