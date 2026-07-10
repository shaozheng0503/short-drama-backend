package handler

import (
	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

// GET /v1/publisher/claimed-dramas —— 已认领剧集列表
func (s *Server) publisherListClaimedDramas(c *gin.Context) {
	id := middleware.CurrentID(c)
	page, pageSize := paginate(c)
	q := s.db.Model(&model.DistributorDrama{}).Where("distributor_id = ?", id)
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	var total int64
	q.Count(&total)
	var dds []model.DistributorDrama
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&dds)

	// 批量查剧名
	dramaIDs := make([]uint64, 0, len(dds))
	for _, dd := range dds {
		dramaIDs = append(dramaIDs, dd.DramaID)
	}
	dramaMap := map[uint64]model.Drama{}
	if len(dramaIDs) > 0 {
		var dramas []model.Drama
		s.db.Where("id IN ?", dramaIDs).Find(&dramas)
		for _, d := range dramas {
			dramaMap[d.ID] = d
		}
	}

	list := make([]gin.H, 0, len(dds))
	for _, dd := range dds {
		v := gin.H{
			"id":             dd.ID,
			"drama_id":       dd.DramaID,
			"platform":       parsePlatforms(dd.Platforms),
			"claim_status":   dd.Status,
			"deposit_status": dd.DepositStatus,
			"claimed_at":     dd.CreatedAt,
		}
		if d, ok := dramaMap[dd.DramaID]; ok {
			v["drama_title"] = d.Title
			v["cover_url"] = d.CoverURL
		}
		// 合同状态
		if dd.ContractID != nil {
			var ct model.DistributorContract
			if err := s.db.First(&ct, *dd.ContractID).Error; err == nil {
				v["contract_status"] = ct.Status
				v["contract_file_url"] = ct.FileURL
			}
		}
		list = append(list, v)
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

// GET /v1/publisher/claimed-dramas/:id —— 已认领剧集详情
func (s *Server) publisherGetClaimedDrama(c *gin.Context) {
	id := middleware.CurrentID(c)
	ddID := parseUint(c.Param("id"))
	var dd model.DistributorDrama
	if err := s.db.Where("id = ? AND distributor_id = ?", ddID, id).First(&dd).Error; err != nil {
		response.NotFound(c, "已认领剧集不存在")
		return
	}

	var drama model.Drama
	s.db.First(&drama, dd.DramaID)

	// 累计收益
	var totalIncome int64
	s.db.Model(&model.DistributorIncomeDaily{}).Where("distributor_id = ? AND drama_id = ?", id, dd.DramaID).Select("COALESCE(SUM(income_cents),0)").Scan(&totalIncome)

	// 累计可出账（已结算的 withdrawable_cents 合计）
	var totalWithdrawable int64
	s.db.Model(&model.DistributorSettlement{}).Where("distributor_id = ? AND status IN ?", id, []string{"open", "invoiced", "paid"}).Select("COALESCE(SUM(withdrawable_cents),0)").Scan(&totalWithdrawable)

	// 最近结算周期
	var lastSettlement model.DistributorSettlement
	s.db.Where("distributor_id = ?", id).Order("created_at desc").First(&lastSettlement)

	// 合同
	var contract *model.DistributorContract
	if dd.ContractID != nil {
		var ct model.DistributorContract
		if err := s.db.First(&ct, *dd.ContractID).Error; err == nil {
			contract = &ct
		}
	}

	v := gin.H{
		"id":                  dd.ID,
		"drama_id":            dd.DramaID,
		"drama_title":         drama.Title,
		"cover_url":           drama.CoverURL,
		"episode_count":       drama.TotalEpisodes,
		"platform":            parsePlatforms(dd.Platforms),
		"claim_status":        dd.Status,
		"deposit_amount_cents": dd.DepositAmountCents,
		"deposit_status":      dd.DepositStatus,
		"total_income_cents":  totalIncome,
		"total_withdrawable_cents": totalWithdrawable,
		"last_cycle_key":      lastSettlement.CycleKey,
		"authorized_at":       dd.AuthorizedAt,
		"created_at":          dd.CreatedAt,
	}
	if contract != nil {
		v["contract_status"] = contract.Status
		v["contract_file_url"] = contract.FileURL
	} else {
		v["contract_status"] = "pending"
	}
	response.OK(c, v)
}

// GET /v1/publisher/claimed-dramas/:id/income-records —— 剧集收益记录
func (s *Server) publisherClaimedDramaIncomeRecords(c *gin.Context) {
	id := middleware.CurrentID(c)
	ddID := parseUint(c.Param("id"))
	var dd model.DistributorDrama
	if err := s.db.Where("id = ? AND distributor_id = ?", ddID, id).First(&dd).Error; err != nil {
		response.NotFound(c, "已认领剧集不存在")
		return
	}
	page, pageSize := paginate(c)
	q := s.db.Model(&model.DistributorIncomeDaily{}).Where("distributor_id = ? AND drama_id = ?", id, dd.DramaID)
	var total int64
	q.Count(&total)
	var items []model.DistributorIncomeDaily
	q.Order("stat_date desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items)

	list := make([]gin.H, 0, len(items))
	for _, r := range items {
		list = append(list, gin.H{
			"id":            r.ID,
			"stat_date":     r.StatDate,
			"platform":      r.Platform,
			"gross_cents":   r.GrossCents,
			"share_ratio_bp": r.ShareRatioBP,
			"income_cents":  r.IncomeCents,
			"batch_no":      r.BatchNo,
		})
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

// GET /v1/publisher/claimed-dramas/:id/deposit-deductions —— 剧集押金抵扣记录
func (s *Server) publisherClaimedDramaDepositDeductions(c *gin.Context) {
	id := middleware.CurrentID(c)
	ddID := parseUint(c.Param("id"))
	var dd model.DistributorDrama
	if err := s.db.Where("id = ? AND distributor_id = ?", ddID, id).First(&dd).Error; err != nil {
		response.NotFound(c, "已认领剧集不存在")
		return
	}
	// 查认领申请号
	var app model.DistributorApplication
	s.db.First(&app, dd.ApplicationID)
	appNo := app.ApplicationNo

	page, pageSize := paginate(c)
	q := s.db.Model(&model.DistributorDepositTransaction{}).Where("distributor_id = ? AND related_type = ? AND related_business_no LIKE ?", id, "deduct", "%"+appNo+"%")
	var total int64
	q.Count(&total)
	var items []model.DistributorDepositTransaction
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items)

	list := make([]gin.H, 0, len(items))
	for _, r := range items {
		list = append(list, gin.H{
			"id":                  r.ID,
			"type":                r.Type,
			"amount_cents":        r.AmountCents,
			"balance_after_cents": r.BalanceAfterCents,
			"related_business_no": r.RelatedBusinessNo,
			"remark":              r.Remark,
			"created_at":          r.CreatedAt,
		})
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}
