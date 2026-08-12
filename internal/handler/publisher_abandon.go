package handler

import (
	"fmt"
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ============================================================
// 放弃认领流程（Abandon）
// ============================================================

// calcAbandonRefund 计算放弃平台的退还押金金额
// originalDeposit: 原始押金总额（分）
// base: 基础押金（分）
// totalPlatforms: 原始平台总数
// abandonCount: 放弃的平台数
func (s *Server) calcAbandonRefund(originalDeposit int64, base int64, totalPlatforms int, abandonCount int) int64 {
	remaining := totalPlatforms - abandonCount
	if remaining <= 0 {
		return originalDeposit
	}
	rateBP := int64(1500) // 15%
	newDeposit := base * (10000 + rateBP*int64(remaining-1)) / 10000
	return originalDeposit - newDeposit
}

// abandonView 构造放弃申请的 JSON 视图
func (s *Server) abandonView(ar model.DistributorAbandonRequest) gin.H {
	v := gin.H{
		"id":                    ar.ID,
		"abandon_no":            ar.AbandonNo,
		"distributor_id":        ar.DistributorID,
		"drama_id":              ar.DramaID,
		"distributor_drama_id":  ar.DistributorDramaID,
		"platforms":             parsePlatforms(ar.Platforms),
		"original_platforms":    parsePlatforms(ar.OriginalPlatforms),
		"refund_amount_cents":   ar.RefundAmountCents,
		"original_deposit_cents": ar.OriginalDepositCents,
		"reason":                ar.Reason,
		"reason_images":         ar.ReasonImages,
		"status":                ar.Status,
		"reject_reason":         ar.RejectReason,
		"reviewed_by":           ar.ReviewedBy,
		"reviewed_at":           ar.ReviewedAt,
		"created_at":            ar.CreatedAt,
		"updated_at":            ar.UpdatedAt,
	}
	// 剧集标题 + 封面
	var drama model.Drama
	if err := s.db.Select("id, title, cover_url").First(&drama, ar.DramaID).Error; err == nil {
		v["drama_title"] = drama.Title
		v["drama_cover_url"] = drama.CoverURL
	}
	return v
}

// POST /v1/publisher/claimed-dramas/:id/abandon —— 创建放弃认领申请
func (s *Server) publisherCreateAbandon(c *gin.Context) {
	distID := middleware.CurrentID(c)
	ddID := parseUint(c.Param("id"))

	var req struct {
		Platforms    []string `json:"platforms"`
		Reason       string   `json:"reason"`
		ReasonImages []string `json:"reason_images"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "platforms 和 reason 必填")
		return
	}
	if len(req.Platforms) == 0 {
		response.InvalidParam(c, "至少选择一个要放弃的平台")
		return
	}
	if len(req.Reason) < 2 || len(req.Reason) > 500 {
		response.InvalidParam(c, "放弃原因需 2-500 字")
		return
	}
	if len(req.ReasonImages) > 9 {
		response.InvalidParam(c, "放弃原因截图最多 9 张")
		return
	}

	// 校验平台有效性
	for _, p := range req.Platforms {
		switch p {
		case model.PlatformDouyin, model.PlatformKuaishou, model.PlatformWechatVideo:
		default:
			response.InvalidParam(c, "无效的发行平台: "+p)
			return
		}
	}

	// 查找 distributor_dramas 记录，确认属于当前发行商且状态为 active
	var dd model.DistributorDrama
	if err := s.db.Where("id = ? AND distributor_id = ?", ddID, distID).First(&dd).Error; err != nil {
		response.NotFound(c, "认领记录不存在")
		return
	}
	if dd.Status != model.DistDramaActive && dd.Status != model.DistDramaAuthorized {
		response.Conflict(c, fmt.Sprintf("当前授权状态不可放弃（%s），仅发行中或已授权状态可申请放弃", dd.Status))
		return
	}

	// 校验所选平台是当前已认领平台的子集
	currentPlatforms := parsePlatforms(dd.Platforms)
	currentSet := map[string]bool{}
	for _, p := range currentPlatforms {
		currentSet[p] = true
	}
	for _, p := range req.Platforms {
		if !currentSet[p] {
			response.InvalidParam(c, "平台 "+platformLabel(p)+" 不在已认领平台列表中")
			return
		}
	}

	// 校验不能放弃全部平台时，如果是放弃全部则直接走全部放弃逻辑
	totalPlatforms := len(currentPlatforms)
	abandonCount := len(req.Platforms)

	// 检查是否已有待审核的放弃申请
	var existingCount int64
	s.db.Model(&model.DistributorAbandonRequest{}).
		Where("distributor_drama_id = ? AND status = ?", ddID, model.AbandonPending).
		Count(&existingCount)
	if existingCount > 0 {
		response.Conflict(c, "该剧已有待审核的放弃申请，请先等待审核结果")
		return
	}

	// 检查是否有未完成的结算单
	var pendingSettlements int64
	s.db.Model(&model.DistributorSettlement{}).
		Where("distributor_id = ? AND status = ?",
			distID,
			model.DistSettlementUnsettled).
		Count(&pendingSettlements)
	if pendingSettlements > 0 {
		response.Conflict(c, "存在未完成的结算单，请先完成结算后再申请放弃认领")
		return
	}

	// 计算退还押金
	var drama model.Drama
	if err := s.db.First(&drama, dd.DramaID).Error; err != nil {
		response.NotFound(c, "剧集不存在")
		return
	}
	base := s.depositBaseCents(drama)
	refundAmount := s.calcAbandonRefund(dd.DepositAmountCents, base, totalPlatforms, abandonCount)

	// 创建放弃申请（非事务操作，不涉及资金变动）
	ar := model.DistributorAbandonRequest{
		AbandonNo:          generateBusinessNo("AB"),
		DistributorID:      distID,
		DramaID:            dd.DramaID,
		DistributorDramaID: ddID,
		Platforms:          platformsToJSON(req.Platforms),
		OriginalPlatforms:  dd.Platforms,
		RefundAmountCents:  refundAmount,
		OriginalDepositCents: dd.DepositAmountCents,
		Reason:             req.Reason,
		ReasonImages:       req.ReasonImages,
		Status:             model.AbandonPending,
	}

	if err := s.db.Create(&ar).Error; err != nil {
		response.ServerError(c, "创建放弃申请失败")
		return
	}

	response.OK(c, s.abandonView(ar))
}

// GET /v1/publisher/abandon-requests —— 发行商查看放弃申请列表
func (s *Server) publisherListAbandonRequests(c *gin.Context) {
	distID := middleware.CurrentID(c)
	page, pageSize := paginate(c)
	q := s.db.Model(&model.DistributorAbandonRequest{}).Where("distributor_id = ?", distID)
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	var total int64
	q.Count(&total)
	var items []model.DistributorAbandonRequest
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items)

	// 批量查剧名 + 封面
	dramaIDs := make([]uint64, 0, len(items))
	for _, ar := range items {
		dramaIDs = append(dramaIDs, ar.DramaID)
	}
	type dramaInfo struct {
		Title    string
		CoverURL string
	}
	dramaMap := map[uint64]dramaInfo{}
	if len(dramaIDs) > 0 {
		var dramas []model.Drama
		s.db.Select("id, title, cover_url").Where("id IN ?", dramaIDs).Find(&dramas)
		for _, d := range dramas {
			dramaMap[d.ID] = dramaInfo{Title: d.Title, CoverURL: d.CoverURL}
		}
	}

	list := make([]gin.H, 0, len(items))
	for _, ar := range items {
		v := s.abandonView(ar)
		v["drama_title"] = dramaMap[ar.DramaID].Title
		v["drama_cover_url"] = dramaMap[ar.DramaID].CoverURL
		list = append(list, v)
	}

	response.OK(c, gin.H{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"items":     list,
	})
}

// GET /v1/publisher/abandon-requests/:id —— 发行商查看放弃申请详情
func (s *Server) publisherGetAbandonRequest(c *gin.Context) {
	distID := middleware.CurrentID(c)
	arID := parseUint(c.Param("id"))
	var ar model.DistributorAbandonRequest
	if err := s.db.Where("id = ? AND distributor_id = ?", arID, distID).First(&ar).Error; err != nil {
		response.NotFound(c, "放弃申请不存在")
		return
	}
	response.OK(c, s.abandonView(ar))
}

// ============================================================
// Admin 放弃认领审核
// ============================================================

// GET /admin/distributor-abandons —— 管理端放弃申请列表
func (s *Server) adminListAbandonRequests(c *gin.Context) {
	page, pageSize := paginate(c)
	q := s.db.Model(&model.DistributorAbandonRequest{})
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	var total int64
	q.Count(&total)
	var items []model.DistributorAbandonRequest
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items)

	// 批量查发行商名 + 剧名
	distIDs := make([]uint64, 0, len(items))
	dramaIDs := make([]uint64, 0, len(items))
	for _, ar := range items {
		distIDs = append(distIDs, ar.DistributorID)
		dramaIDs = append(dramaIDs, ar.DramaID)
	}
	distMap := map[uint64]string{}
	if len(distIDs) > 0 {
		var dists []model.Distributor
		s.db.Select("id, name, org_name, nickname, phone").Where("id IN ?", distIDs).Find(&dists)
		for _, d := range dists {
			distMap[d.ID] = distributorName(&d)
		}
	}
	type dramaInfo struct {
		Title    string
		CoverURL string
	}
	dramaMap := map[uint64]dramaInfo{}
	if len(dramaIDs) > 0 {
		var dramas []model.Drama
		s.db.Select("id, title, cover_url").Where("id IN ?", dramaIDs).Find(&dramas)
		for _, d := range dramas {
			dramaMap[d.ID] = dramaInfo{Title: d.Title, CoverURL: d.CoverURL}
		}
	}

	list := make([]gin.H, 0, len(items))
	for _, ar := range items {
		v := s.abandonView(ar)
		v["distributor_name"] = distMap[ar.DistributorID]
		v["drama_title"] = dramaMap[ar.DramaID].Title
		v["drama_cover_url"] = dramaMap[ar.DramaID].CoverURL
		list = append(list, v)
	}

	response.OK(c, gin.H{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"items":     list,
	})
}

// GET /admin/distributor-abandons/:id —— 管理端放弃申请详情
func (s *Server) adminGetAbandonRequest(c *gin.Context) {
	arID := parseUint(c.Param("id"))
	var ar model.DistributorAbandonRequest
	if err := s.db.First(&ar, arID).Error; err != nil {
		response.NotFound(c, "放弃申请不存在")
		return
	}
	v := s.abandonView(ar)

	// 补充发行商信息
	var dist model.Distributor
	if err := s.db.Select("id, name, org_name, nickname, phone").First(&dist, ar.DistributorID).Error; err == nil {
		v["distributor_name"] = distributorName(&dist)
		v["distributor_phone"] = dist.Phone
	}

	response.OK(c, v)
}

// POST /admin/distributor-abandons/:id/approve —— 审核通过（退还押金）
func (s *Server) adminApproveAbandon(c *gin.Context) {
	adminID := middleware.CurrentID(c)
	arID := parseUint(c.Param("id"))

	var ar model.DistributorAbandonRequest
	if err := s.db.First(&ar, arID).Error; err != nil {
		response.NotFound(c, "放弃申请不存在")
		return
	}

	now := time.Now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 行锁放弃申请，防止并发重复审核
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&ar, arID).Error; err != nil {
			return err
		}
		if ar.Status != model.AbandonPending {
			return fmt.Errorf("仅待审核状态可审核通过（当前: %s）", ar.Status)
		}

		// 行锁 distributor_dramas，防止并发操作
		var dd model.DistributorDrama
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&dd, ar.DistributorDramaID).Error; err != nil {
			return fmt.Errorf("认领记录不存在")
		}
		if dd.Status != model.DistDramaActive && dd.Status != model.DistDramaAuthorized {
			return fmt.Errorf("授权记录状态异常（%s），无法放弃", dd.Status)
		}

		// 行锁 distributor，更新钱包
		var dist model.Distributor
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&dist, ar.DistributorID).Error; err != nil {
			return err
		}

		// 冻结余额校验
		if dist.DepositFrozenCents < ar.RefundAmountCents {
			return fmt.Errorf("发行商冻结押金余额不足（需要 %.2f 元，冻结 %.2f 元），无法退回",
				float64(ar.RefundAmountCents)/100, float64(dist.DepositFrozenCents)/100)
		}

		abandonPlatforms := parsePlatforms(ar.Platforms)
		currentPlatforms := parsePlatforms(dd.Platforms)

		// 判断是全部放弃还是部分放弃
		isAllAbandoned := len(abandonPlatforms) >= len(currentPlatforms)

		if isAllAbandoned {
			// 全部放弃：status -> revoked, deposit_status -> released
			if err := tx.Model(&dd).Updates(map[string]interface{}{
				"status":         model.DistDramaRevoked,
				"deposit_status": model.ClaimDepositReleased,
				"released_at":    now,
			}).Error; err != nil {
				return err
			}
		} else {
			// 部分放弃：从 platforms 中移除被放弃的平台
			abandonSet := map[string]bool{}
			for _, p := range abandonPlatforms {
				abandonSet[p] = true
			}
			var remainingPlatforms []string
			for _, p := range currentPlatforms {
				if !abandonSet[p] {
					remainingPlatforms = append(remainingPlatforms, p)
				}
			}
			if err := tx.Model(&dd).Updates(map[string]interface{}{
				"platforms":            platformsToJSON(remainingPlatforms),
				"deposit_amount_cents": dd.DepositAmountCents - ar.RefundAmountCents,
			}).Error; err != nil {
				return err
			}
		}

		// 钱包更新：冻结 -= refund，可用 += refund
		dist.DepositFrozenCents -= ar.RefundAmountCents
		dist.DepositAvailableCents += ar.RefundAmountCents
		if err := tx.Save(&dist).Error; err != nil {
			return err
		}

		// 记录押金流水
		if err := s.recordDepositTx(tx, ar.DistributorID, model.DepositTxUnfreeze,
			ar.RefundAmountCents, dist.DepositAvailableCents,
			"abandon", ar.AbandonNo, "放弃认领退还押金", ar.DramaID); err != nil {
			return err
		}

		// 更新放弃申请状态
		return tx.Model(&ar).Updates(map[string]interface{}{
			"status":       model.AbandonApproved,
			"reviewed_by":  adminID,
			"reviewed_at":  now,
		}).Error
	})
	if err != nil {
		if isNotFound(err) {
			response.NotFound(c, "放弃申请不存在")
		} else {
			response.Conflict(c, err.Error())
		}
		return
	}

	response.OK(c, gin.H{
		"id":                 arID,
		"status":             model.AbandonApproved,
		"refund_amount_cents": ar.RefundAmountCents,
	})
}

// POST /admin/distributor-abandons/:id/reject —— 审核驳回
func (s *Server) adminRejectAbandon(c *gin.Context) {
	adminID := middleware.CurrentID(c)
	arID := parseUint(c.Param("id"))

	var req struct {
		Reason string `json:"reason" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "reason 必填")
		return
	}

	var ar model.DistributorAbandonRequest
	if err := s.db.First(&ar, arID).Error; err != nil {
		response.NotFound(c, "放弃申请不存在")
		return
	}

	now := time.Now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 行锁放弃申请，防止并发重复审核
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&ar, arID).Error; err != nil {
			return err
		}
		if ar.Status != model.AbandonPending {
			return fmt.Errorf("仅待审核状态可驳回（当前: %s）", ar.Status)
		}
		return tx.Model(&ar).Updates(map[string]interface{}{
			"status":         model.AbandonRejected,
			"reject_reason":  req.Reason,
			"reviewed_by":    adminID,
			"reviewed_at":    now,
		}).Error
	})
	if err != nil {
		if isNotFound(err) {
			response.NotFound(c, "放弃申请不存在")
		} else {
			response.Conflict(c, err.Error())
		}
		return
	}

	response.OK(c, gin.H{
		"id":            arID,
		"status":        model.AbandonRejected,
		"reject_reason": req.Reason,
	})
}
