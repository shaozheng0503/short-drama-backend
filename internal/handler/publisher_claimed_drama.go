package handler

import (
	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

// GET /v1/publisher/claimed-dramas —— 已认领剧集列表（按剧聚合，一剧一行）
func (s *Server) publisherListClaimedDramas(c *gin.Context) {
	id := middleware.CurrentID(c)
	page, pageSize := paginate(c)

	// 按 drama_id 聚合：先查该发行商所有 distributor_dramas，按 drama_id 分组
	var allDDs []model.DistributorDrama
	s.db.Where("distributor_id = ?", id).Order("created_at desc").Find(&allDDs)

	// 按 drama_id 聚合
	type dramaAggregate struct {
		DramaID         uint64
		DDs             []model.DistributorDrama
		AllPlatforms    map[string]bool
		TotalFrozenCents int64
	}
	dramaAggMap := map[uint64]*dramaAggregate{}
	dramaOrder := []uint64{} // 保持顺序
	for _, dd := range allDDs {
		agg, ok := dramaAggMap[dd.DramaID]
		if !ok {
			agg = &dramaAggregate{
				DramaID:      dd.DramaID,
				AllPlatforms: map[string]bool{},
			}
			dramaAggMap[dd.DramaID] = agg
			dramaOrder = append(dramaOrder, dd.DramaID)
		}
		agg.DDs = append(agg.DDs, dd)
		for _, p := range parsePlatforms(dd.Platforms) {
			agg.AllPlatforms[p] = true
		}
		if dd.Status == model.DistDramaAuthorized || dd.Status == model.DistDramaActive {
			agg.TotalFrozenCents += dd.DepositAmountCents
		}
	}

	// 也查该发行商审核中的认领（追加认领场景）
	var pendingApps []model.DistributorApplication
	s.db.Where("distributor_id = ? AND status IN ?", id, []string{
		model.ClaimDepositPending, model.ClaimAuthPending,
		model.ClaimReviewPending, model.ClaimContractPending,
	}).Find(&pendingApps)
	pendingByDrama := map[uint64][]model.DistributorApplication{}
	for _, app := range pendingApps {
		pendingByDrama[app.DramaID] = append(pendingByDrama[app.DramaID], app)
	}

	// 筛选
	filterStatus := c.Query("status")
	filterPlatform := c.Query("platform")
	filteredDramaIDs := []uint64{}
	for _, did := range dramaOrder {
		agg := dramaAggMap[did]
		// 计算剧级状态
		aggStatus := s.computeDramaLevelStatus(agg.DDs, pendingByDrama[did])
		if filterStatus != "" && aggStatus != filterStatus {
			continue
		}
		if filterPlatform != "" && !agg.AllPlatforms[filterPlatform] {
			// 也要检查 pending 中的平台
			found := false
			for _, app := range pendingByDrama[did] {
				for _, p := range parsePlatforms(app.Platforms) {
					if p == filterPlatform {
						found = true
						break
					}
				}
			}
			if !found {
				continue
			}
		}
		filteredDramaIDs = append(filteredDramaIDs, did)
	}

	total := len(filteredDramaIDs)
	// 分页
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	pageDramaIDs := filteredDramaIDs[start:end]

	// 批量查剧信息
	dramaMap := map[uint64]model.Drama{}
	if len(pageDramaIDs) > 0 {
		var dramas []model.Drama
		s.db.Where("id IN ?", pageDramaIDs).Find(&dramas)
		for _, d := range dramas {
			dramaMap[d.ID] = d
		}
	}

	list := make([]gin.H, 0, len(pageDramaIDs))
	for _, did := range pageDramaIDs {
		agg := dramaAggMap[did]
		drama := dramaMap[did]
		aggStatus := s.computeDramaLevelStatus(agg.DDs, pendingByDrama[did])

		// 所有平台（已授权 + 审核中）
		allPlatforms := make([]string, 0, len(agg.AllPlatforms))
		for p := range agg.AllPlatforms {
			allPlatforms = append(allPlatforms, p)
		}
		for _, app := range pendingByDrama[did] {
			for _, p := range parsePlatforms(app.Platforms) {
				allPlatforms = appendUniqueString(allPlatforms, p)
			}
		}

		// 累计收益
		var totalIncome int64
		s.db.Model(&model.DistributorIncomeDaily{}).Where("distributor_id = ? AND drama_id = ?", id, did).
			Select("COALESCE(SUM(income_cents),0)").Scan(&totalIncome)

		// 累计抵扣押金
		var totalDeducted int64
		s.db.Model(&model.DistributorDepositTransaction{}).
			Where("distributor_id = ? AND type = ?", id, model.DepositTxDeduct).
			Select("COALESCE(SUM(ABS(amount_cents)),0)").Scan(&totalDeducted)

		// 剩余冻结押金 = 冻结总额 - 已抵扣
		remainingFrozen := agg.TotalFrozenCents - totalDeducted
		if remainingFrozen < 0 {
			remainingFrozen = 0
		}

		// 净盈余 = 收入 - 已抵扣押金
		netSurplus := totalIncome - totalDeducted

		// 第一个 dd 的 id 作为代表 id
		representativeID := uint64(0)
		if len(agg.DDs) > 0 {
			representativeID = agg.DDs[0].ID
		}

		v := gin.H{
			"id":                            representativeID,
			"drama_id":                      did,
			"drama_title":                   drama.Title,
			"cover_url":                     drama.CoverURL,
			"episode_count":                 drama.TotalEpisodes,
			"platforms":                     allPlatforms,
			"status":                        aggStatus,
			"total_frozen_deposit_cents":    agg.TotalFrozenCents,
			"remaining_frozen_deposit_cents": remainingFrozen,
			"revenue_cents":                 totalIncome,
			"net_surplus_cents":             netSurplus,
			"claim_count":                   len(agg.DDs),
		}
		list = append(list, v)
	}
	response.OK(c, pageResp(list, page, pageSize, int64(total)))
}

// computeDramaLevelStatus 计算剧级聚合状态
// active=全部已授权/活跃 / appending=有活跃认领且有审核中认领 / pending=全部审核中 / revoked=全部已撤销
func (s *Server) computeDramaLevelStatus(dds []model.DistributorDrama, pendingApps []model.DistributorApplication) string {
	hasActive := false
	hasPending := len(pendingApps) > 0
	for _, dd := range dds {
		if dd.Status == model.DistDramaAuthorized || dd.Status == model.DistDramaActive {
			hasActive = true
		}
	}
	if hasActive && hasPending {
		return "appending"
	}
	if hasPending && !hasActive {
		return "pending"
	}
	if hasActive {
		return "active"
	}
	return "revoked"
}

// GET /v1/publisher/claimed-dramas/:id —— 已认领剧集详情（剧级汇总）
func (s *Server) publisherGetClaimedDrama(c *gin.Context) {
	id := middleware.CurrentID(c)
	ddID := parseUint(c.Param("id"))
	var dd model.DistributorDrama
	if err := s.db.Where("id = ? AND distributor_id = ?", ddID, id).First(&dd).Error; err != nil {
		response.NotFound(c, "已认领剧集不存在")
		return
	}

	// 查该剧的所有认领记录
	var allDDs []model.DistributorDrama
	s.db.Where("distributor_id = ? AND drama_id = ?", id, dd.DramaID).Order("created_at asc").Find(&allDDs)

	var drama model.Drama
	s.db.First(&drama, dd.DramaID)

	// 所有平台
	allPlatforms := map[string]bool{}
	for _, d := range allDDs {
		for _, p := range parsePlatforms(d.Platforms) {
			allPlatforms[p] = true
		}
	}
	// 审核中的认领平台
	var pendingApps []model.DistributorApplication
	s.db.Where("distributor_id = ? AND drama_id = ? AND status IN ?", id, dd.DramaID, []string{
		model.ClaimDepositPending, model.ClaimAuthPending,
		model.ClaimReviewPending, model.ClaimContractPending,
	}).Find(&pendingApps)
	for _, app := range pendingApps {
		for _, p := range parsePlatforms(app.Platforms) {
			allPlatforms[p] = true
		}
	}
	platformsList := make([]string, 0, len(allPlatforms))
	for p := range allPlatforms {
		platformsList = append(platformsList, p)
	}

	// 整剧冻结押金总额
	var totalFrozen int64
	for _, d := range allDDs {
		if d.Status == model.DistDramaAuthorized || d.Status == model.DistDramaActive {
			totalFrozen += d.DepositAmountCents
		}
	}

	// 累计收益
	var totalIncome int64
	s.db.Model(&model.DistributorIncomeDaily{}).Where("distributor_id = ? AND drama_id = ?", id, dd.DramaID).Select("COALESCE(SUM(income_cents),0)").Scan(&totalIncome)

	// 累计抵扣押金
	var totalDeducted int64
	s.db.Model(&model.DistributorDepositTransaction{}).
		Where("distributor_id = ? AND type = ?", id, model.DepositTxDeduct).
		Select("COALESCE(SUM(ABS(amount_cents)),0)").Scan(&totalDeducted)

	remainingFrozen := totalFrozen - totalDeducted
	if remainingFrozen < 0 {
		remainingFrozen = 0
	}
	netSurplus := totalIncome - totalDeducted

	// 剧级状态
	aggStatus := s.computeDramaLevelStatus(allDDs, pendingApps)

	// 最近结算周期
	var lastSettlement model.DistributorSettlement
	s.db.Where("distributor_id = ?", id).Order("created_at desc").First(&lastSettlement)

	// 合同（取第一个有合同的）
	var contract *model.DistributorContract
	for _, d := range allDDs {
		if d.ContractID != nil {
			var ct model.DistributorContract
			if err := s.db.First(&ct, *d.ContractID).Error; err == nil {
				contract = &ct
				break
			}
		}
	}

	v := gin.H{
		"id":                             dd.ID,
		"drama_id":                       dd.DramaID,
		"drama_title":                    drama.Title,
		"cover_url":                      drama.CoverURL,
		"episode_count":                  drama.TotalEpisodes,
		"platforms":                      platformsList,
		"status":                         aggStatus,
		"total_frozen_deposit_cents":     totalFrozen,
		"remaining_frozen_deposit_cents": remainingFrozen,
		"revenue_cents":                  totalIncome,
		"net_surplus_cents":              netSurplus,
		"total_deducted_deposit_cents":   totalDeducted,
		"last_cycle_key":                 lastSettlement.CycleKey,
		"claim_count":                    len(allDDs),
		"created_at":                     allDDs[0].CreatedAt,
	}
	if contract != nil {
		v["contract_status"] = contract.Status
		v["contract_file_url"] = s.contractPresignedURL(contract.FileKey)
	} else {
		v["contract_status"] = "pending"
	}
	response.OK(c, v)
}

// GET /v1/publisher/claimed-dramas/:id/claims —— 剧集认领明细列表
func (s *Server) publisherClaimedDramaClaims(c *gin.Context) {
	id := middleware.CurrentID(c)
	ddID := parseUint(c.Param("id"))
	var dd model.DistributorDrama
	if err := s.db.Where("id = ? AND distributor_id = ?", ddID, id).First(&dd).Error; err != nil {
		response.NotFound(c, "已认领剧集不存在")
		return
	}

	// 查该剧所有认领记录（含已完成的 distributor_dramas + 审核中的 applications）
	var allDDs []model.DistributorDrama
	s.db.Where("distributor_id = ? AND drama_id = ?", id, dd.DramaID).Order("created_at asc").Find(&allDDs)

	var pendingApps []model.DistributorApplication
	s.db.Where("distributor_id = ? AND drama_id = ? AND status IN ?", id, dd.DramaID, []string{
		model.ClaimDepositPending, model.ClaimAuthPending,
		model.ClaimReviewPending, model.ClaimContractPending,
	}).Order("created_at asc").Find(&pendingApps)

	// 合并成认领明细列表
	list := make([]gin.H, 0, len(allDDs)+len(pendingApps))
	for _, app := range pendingApps {
		list = append(list, gin.H{
			"type":                 "pending",
			"application_id":       app.ID,
			"application_no":       app.ApplicationNo,
			"platforms":            parsePlatforms(app.Platforms),
			"deposit_amount_cents": app.DepositAmountCents,
			"deposit_status":       app.DepositStatus,
			"status":               app.Status,
			"contract_status":      app.ContractStatus,
			"created_at":           app.CreatedAt,
		})
	}
	for _, d := range allDDs {
		// 查认领申请
		var app model.DistributorApplication
		s.db.First(&app, d.ApplicationID)

		contractStatus := "pending"
		contractFileURL := ""
		if d.ContractID != nil {
			var ct model.DistributorContract
			if err := s.db.First(&ct, *d.ContractID).Error; err == nil {
				contractStatus = ct.Status
				contractFileURL = s.contractPresignedURL(ct.FileKey)
			}
		}
		list = append(list, gin.H{
			"type":                 "completed",
			"dd_id":                d.ID,
			"application_id":       app.ID,
			"application_no":       app.ApplicationNo,
			"platforms":            parsePlatforms(d.Platforms),
			"deposit_amount_cents": d.DepositAmountCents,
			"deposit_status":       d.DepositStatus,
			"status":               d.Status,
			"contract_status":      contractStatus,
			"contract_file_url":    contractFileURL,
			"authorized_at":        d.AuthorizedAt,
			"created_at":           d.CreatedAt,
		})
	}
	response.OK(c, gin.H{"list": list, "total": len(list)})
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
			"id":             r.ID,
			"stat_date":      r.StatDate,
			"platform":       r.Platform,
			"gross_cents":    r.GrossCents,
			"share_ratio_bp": r.ShareRatioBP,
			"income_cents":   r.IncomeCents,
			"batch_no":       r.BatchNo,
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

	page, pageSize := paginate(c)
	q := s.db.Model(&model.DistributorDepositTransaction{}).Where("distributor_id = ? AND type = ?", id, model.DepositTxDeduct)
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

// appendUniqueString 向 slice 追加不重复的字符串
func appendUniqueString(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}
