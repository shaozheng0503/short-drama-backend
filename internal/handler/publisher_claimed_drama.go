package handler

import (
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

// GET /v1/publisher/claimed-dramas —— 已认领剧集列表（按剧聚合，一剧一行）
// 含已授权发行 + 审核中认领 + 已驳回认领（让发行商看到驳回原因）
func (s *Server) publisherListClaimedDramas(c *gin.Context) {
	id := middleware.CurrentID(c)
	page, pageSize := paginate(c)

	// 按 drama_id 聚合：先查该发行商所有 distributor_dramas，按 drama_id 分组
	var allDDs []model.DistributorDrama
	s.db.Where("distributor_id = ?", id).Order("created_at desc").Find(&allDDs)

	// 按 drama_id 聚合
	type dramaAggregate struct {
		DramaID          uint64
		DDs              []model.DistributorDrama
		AllPlatforms     map[string]bool
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

	// 查该发行商审核中 + 已驳回的认领
	var pendingApps []model.DistributorApplication
	s.db.Where("distributor_id = ? AND status IN ?", id, []string{
		model.ClaimDepositPending, model.ClaimAuthPending,
		model.ClaimReviewPending, model.ClaimContractPending,
	}).Find(&pendingApps)
	pendingByDrama := map[uint64][]model.DistributorApplication{}
	for _, app := range pendingApps {
		pendingByDrama[app.DramaID] = append(pendingByDrama[app.DramaID], app)
		// 若该剧不在 dramaOrder 中（无 DD），补进去
		if _, ok := dramaAggMap[app.DramaID]; !ok {
			dramaAggMap[app.DramaID] = &dramaAggregate{
				DramaID:      app.DramaID,
				AllPlatforms: map[string]bool{},
			}
			dramaOrder = append(dramaOrder, app.DramaID)
		}
		for _, p := range parsePlatforms(app.Platforms) {
			dramaAggMap[app.DramaID].AllPlatforms[p] = true
		}
	}

	// 已驳回的认领（单独查，用于展示驳回原因）
	var rejectedApps []model.DistributorApplication
	s.db.Where("distributor_id = ? AND status = ?", id, model.ClaimRejected).Find(&rejectedApps)
	rejectedByDrama := map[uint64][]model.DistributorApplication{}
	for _, app := range rejectedApps {
		rejectedByDrama[app.DramaID] = append(rejectedByDrama[app.DramaID], app)
		// 若该剧不在 dramaOrder 中（无 DD、无 pending），补进去
		if _, ok := dramaAggMap[app.DramaID]; !ok {
			dramaAggMap[app.DramaID] = &dramaAggregate{
				DramaID:      app.DramaID,
				AllPlatforms: map[string]bool{},
			}
			dramaOrder = append(dramaOrder, app.DramaID)
		}
		for _, p := range parsePlatforms(app.Platforms) {
			dramaAggMap[app.DramaID].AllPlatforms[p] = true
		}
	}

	// 筛选
	filterStatus := c.Query("status")
	filterPlatform := c.Query("platform")
	filteredDramaIDs := []uint64{}
	for _, did := range dramaOrder {
		agg := dramaAggMap[did]
		// 计算剧级状态
		aggStatus := s.computeDramaLevelStatus(agg.DDs, pendingByDrama[did], rejectedByDrama[did])
		if filterStatus != "" && aggStatus != filterStatus {
			continue
		}
		if filterPlatform != "" && !agg.AllPlatforms[filterPlatform] {
			continue
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
		aggStatus := s.computeDramaLevelStatus(agg.DDs, pendingByDrama[did], rejectedByDrama[did])

		// 所有平台（已授权 + 审核中 + 已驳回）
		allPlatforms := make([]string, 0, len(agg.AllPlatforms))
		for p := range agg.AllPlatforms {
			allPlatforms = append(allPlatforms, p)
		}

		// 累计收益
		var totalIncome int64
		s.db.Model(&model.DistributorIncomeDaily{}).Where("distributor_id = ? AND drama_id = ?", id, did).
			Select("COALESCE(SUM(income_cents),0)").Scan(&totalIncome)

		// 累计抵扣押金（按剧隔离，不混入其他剧的抵扣）
		var totalDeducted int64
		s.db.Model(&model.DistributorDepositTransaction{}).
			Where("distributor_id = ? AND type = ? AND drama_id = ?", id, model.DepositTxDeduct, did).
			Select("COALESCE(SUM(ABS(amount_cents)),0)").Scan(&totalDeducted)

		// 剩余冻结押金 = 冻结总额 - 已抵扣
		remainingFrozen := agg.TotalFrozenCents - totalDeducted
		if remainingFrozen < 0 {
			remainingFrozen = 0
		}

		// 净盈余 = 收入 - 已抵扣押金
		netSurplus := totalIncome - totalDeducted

		// 代表 id：优先用第一个 DD 的 id，无 DD 时用最近申请的 id
		representativeID := uint64(0)
		if len(agg.DDs) > 0 {
			representativeID = agg.DDs[0].ID
		} else if len(pendingByDrama[did]) > 0 {
			representativeID = pendingByDrama[did][0].ID
		} else if len(rejectedByDrama[did]) > 0 {
			representativeID = rejectedByDrama[did][0].ID
		}

		v := gin.H{
			"id":                             representativeID,
			"drama_id":                       did,
			"drama_title":                    drama.Title,
			"cover_url":                      drama.CoverURL,
			"episode_count":                  drama.TotalEpisodes,
			"platforms":                      allPlatforms,
			"status":                         aggStatus,
			"total_frozen_deposit_cents":     agg.TotalFrozenCents,
			"remaining_frozen_deposit_cents": remainingFrozen,
			"revenue_cents":                  totalIncome,
			"net_surplus_cents":              netSurplus,
			"claim_count":                    len(agg.DDs) + len(pendingByDrama[did]) + len(rejectedByDrama[did]),
		}

		// 若有驳回认领，附上最近一条驳回原因
		if rejectedApps := rejectedByDrama[did]; len(rejectedApps) > 0 {
			latest := rejectedApps[0]
			for _, app := range rejectedApps {
				if app.CreatedAt.After(latest.CreatedAt) {
					latest = app
				}
			}
			v["latest_reject_reason"] = latest.RejectReason
			v["latest_rejected_application_id"] = latest.ID
		}

		list = append(list, v)
	}
	response.OK(c, pageResp(list, page, pageSize, int64(total)))
}

// computeDramaLevelStatus 计算剧级聚合状态
// active=已授权/活跃 / appending=有活跃认领且有审核中认领 / pending=审核中无活跃 /
// rejected=仅驳回 / revoked=全部已撤销
func (s *Server) computeDramaLevelStatus(dds []model.DistributorDrama, pendingApps []model.DistributorApplication, rejectedApps []model.DistributorApplication) string {
	hasActive := false
	hasPending := len(pendingApps) > 0
	hasRejected := len(rejectedApps) > 0
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
	if hasRejected {
		return "rejected"
	}
	return "revoked"
}

// GET /v1/publisher/claimed-dramas/:id —— 已认领剧集详情（剧级汇总）
// :id 可以是 distributor_drama.id 或 distributor_application.id（无 DD 时）
func (s *Server) publisherGetClaimedDrama(c *gin.Context) {
	id := middleware.CurrentID(c)
	rawID := parseUint(c.Param("id"))

	// 先尝试按 DD id 查找
	var dramaID uint64
	var allDDs []model.DistributorDrama
	var dd model.DistributorDrama
	if err := s.db.Where("id = ? AND distributor_id = ?", rawID, id).First(&dd).Error; err == nil {
		dramaID = dd.DramaID
	} else {
		// 回退：按 application id 查找
		var app model.DistributorApplication
		if err := s.db.Where("id = ? AND distributor_id = ?", rawID, id).First(&app).Error; err != nil {
			response.NotFound(c, "已认领剧集不存在")
			return
		}
		dramaID = app.DramaID
	}

	// 查该剧的所有认领记录
	s.db.Where("distributor_id = ? AND drama_id = ?", id, dramaID).Order("created_at asc").Find(&allDDs)

	var drama model.Drama
	s.db.First(&drama, dramaID)

	// 所有平台
	allPlatforms := map[string]bool{}
	for _, d := range allDDs {
		for _, p := range parsePlatforms(d.Platforms) {
			allPlatforms[p] = true
		}
	}
	// 审核中 + 已驳回的认领平台
	var pendingApps []model.DistributorApplication
	s.db.Where("distributor_id = ? AND drama_id = ? AND status IN ?", id, dramaID, []string{
		model.ClaimDepositPending, model.ClaimAuthPending,
		model.ClaimReviewPending, model.ClaimContractPending,
	}).Find(&pendingApps)
	for _, app := range pendingApps {
		for _, p := range parsePlatforms(app.Platforms) {
			allPlatforms[p] = true
		}
	}
	var rejectedApps []model.DistributorApplication
	s.db.Where("distributor_id = ? AND drama_id = ? AND status = ?", id, dramaID, model.ClaimRejected).Find(&rejectedApps)
	for _, app := range rejectedApps {
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
	s.db.Model(&model.DistributorIncomeDaily{}).Where("distributor_id = ? AND drama_id = ?", id, dramaID).Select("COALESCE(SUM(income_cents),0)").Scan(&totalIncome)

	// 累计抵扣押金（按剧隔离，不混入其他剧的抵扣）
	var totalDeducted int64
	s.db.Model(&model.DistributorDepositTransaction{}).
		Where("distributor_id = ? AND type = ? AND drama_id = ?", id, model.DepositTxDeduct, dramaID).
		Select("COALESCE(SUM(ABS(amount_cents)),0)").Scan(&totalDeducted)

	remainingFrozen := totalFrozen - totalDeducted
	if remainingFrozen < 0 {
		remainingFrozen = 0
	}
	netSurplus := totalIncome - totalDeducted

	// 剧级状态
	aggStatus := s.computeDramaLevelStatus(allDDs, pendingApps, rejectedApps)

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

	// 代表 id
	representativeID := uint64(0)
	if len(allDDs) > 0 {
		representativeID = allDDs[0].ID
	} else if len(pendingApps) > 0 {
		representativeID = pendingApps[0].ID
	} else if len(rejectedApps) > 0 {
		representativeID = rejectedApps[0].ID
	}

	// 最早创建时间
	var createdAt time.Time
	if len(allDDs) > 0 {
		createdAt = allDDs[0].CreatedAt
	} else if len(pendingApps) > 0 {
		createdAt = pendingApps[0].CreatedAt
	} else if len(rejectedApps) > 0 {
		createdAt = rejectedApps[0].CreatedAt
	}

	v := gin.H{
		"id":                             representativeID,
		"drama_id":                       dramaID,
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
		"claim_count":                    len(allDDs) + len(pendingApps) + len(rejectedApps),
		"created_at":                     createdAt,
	}
	if contract != nil {
		v["contract_status"] = contract.Status
		v["contract_file_url"] = s.contractPresignedURL(contract.FileKey)
	} else {
		v["contract_status"] = model.ClaimContractNone
	}

	// 若有驳回认领，附上最近一条驳回原因
	if len(rejectedApps) > 0 {
		latest := rejectedApps[0]
		for _, app := range rejectedApps {
			if app.CreatedAt.After(latest.CreatedAt) {
				latest = app
			}
		}
		v["latest_reject_reason"] = latest.RejectReason
		v["latest_rejected_application_id"] = latest.ID
	}

	// 2026-07-28 会议：在已认领剧集详情中返回脱敏后的剧目详细信息（对齐管理端，排除敏感字段）
	// 仅对当前发行商已认领（含审核中/已驳回等可见态）的剧集开放，访问控制已由 resolveClaimedDramaID 保证。
	v["drama"] = s.dramaPublisherView(drama)

	response.OK(c, v)
}

// GET /v1/publisher/claimed-dramas/:id/claims —— 剧集认领明细列表
// :id 可以是 distributor_drama.id 或 distributor_application.id（无 DD 时）
// 含审核中 + 已驳回认领（驳回记录带 reject_reason）
func (s *Server) publisherClaimedDramaClaims(c *gin.Context) {
	id := middleware.CurrentID(c)
	rawID := parseUint(c.Param("id"))

	// 先尝试按 DD id 查找
	var dramaID uint64
	var dd model.DistributorDrama
	if err := s.db.Where("id = ? AND distributor_id = ?", rawID, id).First(&dd).Error; err == nil {
		dramaID = dd.DramaID
	} else {
		// 回退：按 application id 查找
		var app model.DistributorApplication
		if err := s.db.Where("id = ? AND distributor_id = ?", rawID, id).First(&app).Error; err != nil {
			response.NotFound(c, "已认领剧集不存在")
			return
		}
		dramaID = app.DramaID
	}

	// 查该剧所有认领记录（含已完成的 distributor_dramas + 审核中 + 已驳回 applications）
	var allDDs []model.DistributorDrama
	s.db.Where("distributor_id = ? AND drama_id = ?", id, dramaID).Order("created_at asc").Find(&allDDs)

	var pendingApps []model.DistributorApplication
	s.db.Where("distributor_id = ? AND drama_id = ? AND status IN ?", id, dramaID, []string{
		model.ClaimDepositPending, model.ClaimAuthPending,
		model.ClaimReviewPending, model.ClaimContractPending,
	}).Order("created_at asc").Find(&pendingApps)

	var rejectedApps []model.DistributorApplication
	s.db.Where("distributor_id = ? AND drama_id = ? AND status = ?", id, dramaID, model.ClaimRejected).Order("created_at asc").Find(&rejectedApps)

	// 合并成认领明细列表
	list := make([]gin.H, 0, len(allDDs)+len(pendingApps)+len(rejectedApps))
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
			"authorization_confirmed": app.AuthorizationConfirmed,
			"authorized_at":        app.AuthorizedAt,
			"reject_reason":        app.RejectReason,
			"created_at":           app.CreatedAt,
		})
	}
	for _, app := range rejectedApps {
		list = append(list, gin.H{
			"type":                    "rejected",
			"application_id":          app.ID,
			"application_no":          app.ApplicationNo,
			"platforms":               parsePlatforms(app.Platforms),
			"deposit_amount_cents":    app.DepositAmountCents,
			"deposit_status":          app.DepositStatus,
			"status":                  app.Status,
			"contract_status":         app.ContractStatus,
			"authorization_confirmed": app.AuthorizationConfirmed,
			"authorized_at":           app.AuthorizedAt,
			"reject_reason":           app.RejectReason,
			"reviewed_at":             app.ReviewedAt,
			"created_at":              app.CreatedAt,
		})
	}
	for _, d := range allDDs {
		// 查认领申请
		var app model.DistributorApplication
		s.db.First(&app, d.ApplicationID)

		contractStatus := model.ClaimContractNone
		contractFileURL := ""
		if d.ContractID != nil {
			var ct model.DistributorContract
			if err := s.db.First(&ct, *d.ContractID).Error; err == nil {
				contractStatus = ct.Status
				contractFileURL = s.contractPresignedURL(ct.FileKey)
			}
		}
		list = append(list, gin.H{
			"type":                    "completed",
			"dd_id":                   d.ID,
			"application_id":          app.ID,
			"application_no":          app.ApplicationNo,
			"platforms":               parsePlatforms(d.Platforms),
			"deposit_amount_cents":    d.DepositAmountCents,
			"deposit_status":          d.DepositStatus,
			"status":                  d.Status,
			"contract_status":         contractStatus,
			"contract_file_url":       contractFileURL,
			"authorization_confirmed": app.AuthorizationConfirmed,
			"authorized_at":           d.AuthorizedAt,
			"reject_reason":           app.RejectReason,
			"created_at":              d.CreatedAt,
		})
	}
	response.OK(c, gin.H{"list": list, "total": len(list)})
}

// GET /v1/publisher/claimed-dramas/:id/income-records —— 剧集收益记录
func (s *Server) publisherClaimedDramaIncomeRecords(c *gin.Context) {
	id := middleware.CurrentID(c)
	dramaID, ok := s.resolveClaimedDramaID(c, id)
	if !ok {
		response.NotFound(c, "已认领剧集不存在")
		return
	}
	page, pageSize := paginate(c)
	q := s.db.Model(&model.DistributorIncomeDaily{}).Where("distributor_id = ? AND drama_id = ?", id, dramaID)
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
	dramaID, ok := s.resolveClaimedDramaID(c, id)
	if !ok {
		response.NotFound(c, "已认领剧集不存在")
		return
	}

	page, pageSize := paginate(c)
	q := s.db.Model(&model.DistributorDepositTransaction{}).
		Where("distributor_id = ? AND type = ? AND drama_id = ?", id, model.DepositTxDeduct, dramaID)
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

// resolveClaimedDramaID 从 URL :id 参数解析 drama_id。
// 先尝试 distributor_drama.id，回退到 distributor_application.id。
func (s *Server) resolveClaimedDramaID(c *gin.Context, distributorID uint64) (uint64, bool) {
	rawID := parseUint(c.Param("id"))
	var dd model.DistributorDrama
	if err := s.db.Where("id = ? AND distributor_id = ?", rawID, distributorID).First(&dd).Error; err == nil {
		return dd.DramaID, true
	}
	var app model.DistributorApplication
	if err := s.db.Where("id = ? AND distributor_id = ?", rawID, distributorID).First(&app).Error; err == nil {
		return app.DramaID, true
	}
	return 0, false
}

// GET /v1/publisher/claimed-dramas/:id/download —— 已认领剧集下载
//
// 2026-07-28 会议决策：发行商完成剧集认领且经平台授权后，开放已领剧集一键下载入口。
// 下载包仅包含：剧集视频、封面、角色信息。
// 下载包不包含：权属文件、不侵权声明等敏感文件，避免信息泄露。
// 仅 authorized/active 状态可下载，审核中/已驳回不可下载。
func (s *Server) publisherDownloadClaimedDrama(c *gin.Context) {
	id := middleware.CurrentID(c)
	dramaID, ok := s.resolveClaimedDramaID(c, id)
	if !ok {
		response.NotFound(c, "已认领剧集不存在")
		return
	}

	// 校验授权状态：只有 authorized/active 的才能下载
	var authCount int64
	s.db.Model(&model.DistributorDrama{}).
		Where("distributor_id = ? AND drama_id = ? AND status IN ?",
			id, dramaID, []string{model.DistDramaAuthorized, model.DistDramaActive}).
		Count(&authCount)
	if authCount == 0 {
		response.Forbidden(c, "仅已授权剧集可下载，请等待平台审核通过后再试")
		return
	}

	var drama model.Drama
	if err := s.db.First(&drama, dramaID).Error; err != nil {
		response.NotFound(c, "剧集不存在")
		return
	}

	// 1. 剧集视频列表（仅已就绪的可下载）
	var episodes []model.Episode
	s.db.Where("drama_id = ? AND status = ?", dramaID, "ready").
		Order("episode_no asc").Find(&episodes)
	episodeList := make([]gin.H, 0, len(episodes))
	for _, ep := range episodes {
		episodeList = append(episodeList, gin.H{
			"episode_no":       ep.EpisodeNo,
			"title":            ep.Title,
			"video_url":        ep.VideoURL,
			"duration_seconds": ep.DurationSeconds,
		})
	}

	// 2. 封面列表（主封面 + 多图封面）
	coverList := []string{}
	if drama.CoverURL != "" {
		coverList = append(coverList, drama.CoverURL)
	}
	var covers []model.DramaCover
	s.db.Where("drama_id = ?", dramaID).Order("sort_order asc").Find(&covers)
	for _, cv := range covers {
		// 去重（CoverURL 可能与第一张 DramaCover 重复）
		seen := false
		for _, u := range coverList {
			if u == cv.URL {
				seen = true
				break
			}
		}
		if !seen {
			coverList = append(coverList, cv.URL)
		}
	}

	// 3. 角色信息
	var characters []model.DramaCharacter
	s.db.Where("drama_id = ?", dramaID).Order("sort_order asc").Find(&characters)
	characterList := make([]gin.H, 0, len(characters))
	for _, ch := range characters {
		characterList = append(characterList, gin.H{
			"name":      ch.Name,
			"photo_url": ch.PhotoURL,
			"intro":     ch.Intro,
			"role":      ch.Role,
		})
	}

	// 不包含：权属文件（copyright_files）、不侵权声明等敏感文件
	response.OK(c, gin.H{
		"drama_id":      dramaID,
		"drama_title":   drama.Title,
		"episode_count": len(episodeList),
		"cover_count":   len(coverList),
		"episodes":      episodeList,
		"covers":        coverList,
		"characters":    characterList,
		"notice":        "下载包仅包含剧集视频、封面和角色信息，权属文件等敏感内容不纳入下载范围",
	})
}

// dramaPublisherView 构建发行商可见的脱敏剧目详情视图。
//
// 2026-07-28 会议信息展示规则：对齐管理端 DramaInfoSection，但排除以下敏感字段：
//   - 排除：free_episodes / price_cents / scheduled_publish_at
//   - 排除：制作人员/作品属性（is_ai / aigc_tools / language_id / audience / alias_paid /
//     alias_free / production_org / producer / director / screenwriter /
//     production_cost_cents / is_ip_adaptation / publish_type）
//   - 排除：cost_config_url（成本配置图）/ non_infringement_url（不侵权承诺函）
//
//   - 保留：标题/简介/创作者/分类/总集数、封面列表、角色信息、权属文件、剧集视频（含播放地址）
func (s *Server) dramaPublisherView(drama model.Drama) gin.H {
	covers, characters := s.loadDramaExtras(drama.ID)

	// 权属文件：nil → 空数组
	copyrightFiles := drama.CopyrightFileURLs
	if copyrightFiles == nil {
		copyrightFiles = []string{}
	}

	// 剧集视频列表（含播放地址，对齐管理端 episodeAdminView 口径）
	var episodes []model.Episode
	s.db.Where("drama_id = ?", drama.ID).Order("episode_no asc").Find(&episodes)
	epList := make([]gin.H, 0, len(episodes))
	for _, ep := range episodes {
		epList = append(epList, gin.H{
			"id":               ep.ID,
			"episode_no":       ep.EpisodeNo,
			"title":            ep.Title,
			"video_url":        ep.VideoURL,
			"duration_seconds": ep.DurationSeconds,
			"status":           ep.Status,
		})
	}

	return gin.H{
		// 剧目基础
		"id":              drama.ID,
		"title":           drama.Title,
		"description":     drama.Description,
		"cover_url":       drama.CoverURL,
		"category_id":     drama.CategoryID,
		"category_name":   s.nameOfCategory(drama.CategoryID),
		"creator_id":      drama.CreatorID,
		"creator_name":    s.nameOfCreator(drama.CreatorID),
		"total_episodes":  drama.TotalEpisodes,
		// 封面列表
		"covers": covers,
		// 角色信息
		"characters": characters,
		// 权属文件
		"copyright_file_urls": copyrightFiles,
		// 剧集视频
		"episodes": epList,
		// 排除：free_episodes / price_cents / scheduled_publish_at
		// 排除：is_ai / aigc_tools / language_id / audience / alias_paid / alias_free /
		//       production_org / producer / director / screenwriter /
		//       production_cost_cents / is_ip_adaptation / publish_type
		// 排除：cost_config_url / non_infringement_url
	}
}
