package handler

import (
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
	incomes := map[uint64]int64{}
	if len(dramaIDs) > 0 {
		var rows []struct {
			DramaID uint64
			Income  int64
		}
		s.db.Table("creator_stats_daily").
			Select("drama_id, COALESCE(SUM(income_cents),0) as income").
			Where("creator_id = ? AND drama_id IN ?", cid, dramaIDs).
			Group("drama_id").Scan(&rows)
		for _, r := range rows {
			incomes[r.DramaID] = r.Income
		}
	}

	list := make([]gin.H, 0, len(dramas))
	for _, d := range dramas {
		list = append(list, gin.H{
			"id":             d.ID,
			"title":          d.Title,
			"cover_url":      d.CoverURL,
			"status":         d.Status,
			"total_episodes": d.TotalEpisodes,
			"play_count":     d.PlayCount,
			"income_cents":   incomes[d.ID],
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

func (s *Server) creatorIncome(c *gin.Context) {
	cid := middleware.CurrentID(c)
	page, pageSize := paginate(c)

	type aggRow struct {
		StatDate    string
		IncomeCents int64
	}
	var rows []aggRow
	base := s.db.Table("creator_stats_daily").
		Select("stat_date, COALESCE(SUM(income_cents),0) as income_cents").
		Where("creator_id = ?", cid).
		Group("stat_date").
		Order("stat_date desc")

	var total int64
	s.db.Raw("SELECT COUNT(DISTINCT stat_date) FROM creator_stats_daily WHERE creator_id = ?", cid).Scan(&total)

	base.Offset((page - 1) * pageSize).Limit(pageSize).Scan(&rows)

	list := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		list = append(list, gin.H{
			"date":         r.StatDate,
			"income_cents": r.IncomeCents,
		})
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

type creatorProfileRequest struct {
	Name       *string `json:"name"`
	IDCardNo   *string `json:"id_card_no"`
	BankName   *string `json:"bank_name"`
	BankCardNo *string `json:"bank_card_no"`
}

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

	// MVP 暂不接实名 / 银行卡四要素，允许"同一次提交完整资料"后自动置为 verified。
	// 分步上传身份证 / 银行卡时不自动认证，避免前端单字段保存误触发认证状态。
	var creator model.Creator
	if err := s.db.First(&creator, cid).Error; err != nil {
		response.ServerError(c, "查询创作者失败")
		return
	}

	updates := map[string]interface{}{}
	if req.Name != nil && *req.Name != "" {
		updates["name"] = *req.Name
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
	}
	if req.BankCardNo != nil && *req.BankCardNo != "" {
		enc, err := s.cryptor.Encrypt(*req.BankCardNo)
		if err != nil {
			response.ServerError(c, "银行卡加密失败")
			return
		}
		updates["bank_card_no_enc"] = enc
		updates["bank_card_last4"] = secure.Last4(*req.BankCardNo)
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
	if creator.VerifyStatus != model.CreatorVerifyVerified &&
		submittedCompleteProfile &&
		willName != "" && willIDCard != "" && willBankCard != "" && willBankName != "" {
		updates["verify_status"] = model.CreatorVerifyVerified
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
	if cr.BankCardLast4 != "" {
		maskedBank = "***" + cr.BankCardLast4
	}
	return gin.H{
		"id":                  cr.ID,
		"phone":               sms.MaskPhone(cr.Phone),
		"name":                cr.Name,
		"bank_name":           cr.BankName,
		"bank_card_no_masked": maskedBank,
		"verify_status":       cr.VerifyStatus,
		"total_income_cents":  cr.TotalIncomeCents,
		"balance_cents":       cr.BalanceCents,
		"frozen_cents":        cr.FrozenCents,
		"status":              cr.Status,
	}
}
