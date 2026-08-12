package handler

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Server) adminListIncomeImports(c *gin.Context) {
	page, pageSize := paginate(c)
	q := s.db.Model(&model.ChannelIncomeImportBatch{})
	if v := strings.TrimSpace(c.Query("batch_no")); v != "" {
		q = q.Where("batch_no LIKE ?", "%"+v+"%")
	}
	var total int64
	q.Count(&total)
	var items []model.ChannelIncomeImportBatch
	if err := q.Order("created_at desc").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&items).Error; err != nil {
		response.ServerError(c, "查询导入批次失败")
		return
	}
	list := make([]gin.H, 0, len(items))
	for _, item := range items {
		list = append(list, incomeImportBatchView(item, false))
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

func (s *Server) adminGetIncomeImport(c *gin.Context) {
	batchNo := strings.TrimSpace(c.Param("batch_no"))
	if batchNo == "" {
		response.InvalidParam(c, "batch_no 不合法")
		return
	}
	var batch model.ChannelIncomeImportBatch
	if err := s.db.Where("batch_no = ?", batchNo).First(&batch).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "导入批次不存在")
			return
		}
		response.ServerError(c, "查询导入批次失败")
		return
	}
	response.OK(c, incomeImportBatchView(batch, true))
}

func incomeImportBatchView(batch model.ChannelIncomeImportBatch, withReports bool) gin.H {
	view := gin.H{
		"batch_no":           batch.BatchNo,
		"admin_id":           batch.AdminID,
		"file_name":          batch.FileName,
		"processed_rows":     batch.ProcessedRows,
		"created_rows":       batch.CreatedRows,
		"updated_rows":       batch.UpdatedRows,
		"unchanged_rows":     batch.UnchangedRows,
		"duplicate_rows":     batch.DuplicateRows,
		"failed_rows":        batch.FailedRows,
		"income_delta_cents": batch.IncomeDeltaCents,
		"created_at":         batch.CreatedAt,
	}
	if withReports && batch.RowReportsJSON != "" {
		var reports []incomeImportRowReport
		if err := json.Unmarshal([]byte(batch.RowReportsJSON), &reports); err == nil {
			view["row_reports"] = reports
		}
	}
	return view
}

func (s *Server) adminListChannelIncomes(c *gin.Context) {
	page, pageSize := paginate(c)
	q := s.db.Model(&model.ChannelIncomeDaily{})
	if v := parseUint(c.Query("drama_id")); v > 0 {
		q = q.Where("drama_id = ?", v)
	}
	if v := parseUint(c.Query("creator_id")); v > 0 {
		q = q.Where("creator_id = ?", v)
	}
	if v := strings.TrimSpace(c.Query("channel")); v != "" {
		q = q.Where("channel = ?", v)
	}
	if v := strings.TrimSpace(c.Query("stat_date_from")); v != "" {
		q = q.Where("stat_date >= ?", v)
	}
	if v := strings.TrimSpace(c.Query("stat_date_to")); v != "" {
		q = q.Where("stat_date <= ?", v)
	}
	if v := strings.TrimSpace(c.Query("batch_no")); v != "" {
		q = q.Where("batch_no = ?", v)
	}
	var total int64
	q.Count(&total)
	var items []model.ChannelIncomeDaily
	if err := q.Order("stat_date desc, id desc").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&items).Error; err != nil {
		response.ServerError(c, "查询渠道收益失败")
		return
	}
	dramaTitles := s.attachDramaTitlesForIncomes(items)
	creatorNames := s.attachCreatorNamesForIncomes(items)
	list := make([]gin.H, 0, len(items))
	for _, item := range items {
		list = append(list, channelIncomeView(item, dramaTitles[item.DramaID], creatorNames[item.CreatorID]))
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

func (s *Server) attachDramaTitlesForIncomes(items []model.ChannelIncomeDaily) map[uint64]string {
	ids := make([]uint64, 0)
	seen := map[uint64]bool{}
	for _, item := range items {
		if !seen[item.DramaID] {
			ids = append(ids, item.DramaID)
			seen[item.DramaID] = true
		}
	}
	titles := map[uint64]string{}
	if len(ids) == 0 {
		return titles
	}
	var rows []struct {
		ID    uint64
		Title string
	}
	s.db.Table("dramas").Select("id, title").Where("id IN ?", ids).Scan(&rows)
	for _, r := range rows {
		titles[r.ID] = r.Title
	}
	return titles
}

func (s *Server) attachCreatorNamesForIncomes(items []model.ChannelIncomeDaily) map[uint64]string {
	ids := make([]uint64, 0)
	seen := map[uint64]bool{}
	for _, item := range items {
		if item.CreatorID == 0 || seen[item.CreatorID] {
			continue
		}
		ids = append(ids, item.CreatorID)
		seen[item.CreatorID] = true
	}
	names := map[uint64]string{}
	if len(ids) == 0 {
		return names
	}
	var creators []model.Creator
	s.db.Where("id IN ?", ids).Find(&creators)
	for _, cr := range creators {
		names[cr.ID] = creatorDisplayName(cr)
	}
	return names
}

func channelIncomeView(item model.ChannelIncomeDaily, dramaTitle, creatorName string) gin.H {
	return gin.H{
		"id":            item.ID,
		"drama_id":      item.DramaID,
		"drama_title":   dramaTitle,
		"channel":       item.Channel,
		"stat_date":     item.StatDate,
		"creator_id":    item.CreatorID,
		"creator_name":  creatorName,
		"gross_cents":    item.GrossCents,
		"share_ratio_bp": item.ShareRatioBP,
		"income_cents":  item.IncomeCents,
		"batch_no":      item.BatchNo,
		"import_row_no": item.ImportRowNo,
		"created_at":    item.CreatedAt,
		"updated_at":    item.UpdatedAt,
	}
}

type adminUpdateChannelIncomeRequest struct {
	IncomeCents *int64 `json:"income_cents"`
}

func (s *Server) adminUpdateChannelIncome(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var req adminUpdateChannelIncomeRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.IncomeCents == nil {
		response.InvalidParam(c, "income_cents 必填")
		return
	}
	if *req.IncomeCents < 0 {
		response.InvalidParam(c, "income_cents 不能为负")
		return
	}
	var updated model.ChannelIncomeDaily
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var row model.ChannelIncomeDaily
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, id).Error; err != nil {
			return err
		}
		delta := *req.IncomeCents - row.IncomeCents
		if delta == 0 {
			updated = row
			return nil
		}
		if err := tx.Model(&model.ChannelIncomeDaily{}).Where("id = ?", id).
			Update("income_cents", *req.IncomeCents).Error; err != nil {
			return err
		}
		if err := s.bumpCreatorStatsIncome(tx, row.CreatorID, row.DramaID, row.StatDate, delta); err != nil {
			return err
		}
		if err := tx.Model(&model.Creator{}).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", row.CreatorID).
			Updates(map[string]interface{}{
				"total_income_cents": gorm.Expr("total_income_cents + ?", delta),
				"balance_cents":      gorm.Expr("balance_cents + ?", delta),
			}).Error; err != nil {
			return err
		}
		if err := tx.First(&updated, id).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		if isNotFound(err) {
			response.NotFound(c, "渠道收益记录不存在")
			return
		}
		response.ServerError(c, "更新渠道收益失败")
		return
	}
	titles := s.attachDramaTitlesForIncomes([]model.ChannelIncomeDaily{updated})
	creatorNames := s.attachCreatorNamesForIncomes([]model.ChannelIncomeDaily{updated})
	response.OK(c, channelIncomeView(updated, titles[updated.DramaID], creatorNames[updated.CreatorID]))
}

func (s *Server) adminDeleteChannelIncome(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var row model.ChannelIncomeDaily
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, id).Error; err != nil {
			return err
		}
		delta := -row.IncomeCents
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
		if delta != 0 {
			if err := s.bumpCreatorStatsIncome(tx, row.CreatorID, row.DramaID, row.StatDate, delta); err != nil {
				return err
			}
			if err := tx.Model(&model.Creator{}).
				Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ?", row.CreatorID).
				Updates(map[string]interface{}{
					"total_income_cents": gorm.Expr("total_income_cents + ?", delta),
					"balance_cents":      gorm.Expr("balance_cents + ?", delta),
				}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if isNotFound(err) {
			response.NotFound(c, "渠道收益记录不存在")
			return
		}
		response.ServerError(c, "删除渠道收益失败")
		return
	}
	response.OK(c, gin.H{"id": id, "deleted": true})
}

func (s *Server) saveIncomeImportBatch(c *gin.Context, batchNo, fileName string, result gin.H, reports []incomeImportRowReport) error {
	reportsJSON, err := json.Marshal(reports)
	if err != nil {
		return err
	}
	batch := model.ChannelIncomeImportBatch{
		BatchNo:          batchNo,
		AdminID:          middleware.CurrentID(c),
		FileName:         fileName,
		ProcessedRows:    intFromAny(result["processed_rows"]),
		CreatedRows:      intFromAny(result["created_rows"]),
		UpdatedRows:      intFromAny(result["updated_rows"]),
		UnchangedRows:    intFromAny(result["unchanged_rows"]),
		DuplicateRows:    intFromAny(result["duplicate_rows"]),
		FailedRows:       intFromAny(result["failed_rows"]),
		IncomeDeltaCents: int64FromAny(result["income_delta_cents"]),
		RowReportsJSON:   string(reportsJSON),
	}
	return s.db.Create(&batch).Error
}

func intFromAny(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

func int64FromAny(v interface{}) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	default:
		return 0
	}
}

// adminIncomePeriodSummary —— GET /v1/admin/finance/income/period-summary?start=2026-08-01&end=2026-08-15
// 2026-08-12 新增：财务导入收入前查看某周期的汇总，判断是否可以生成结算单。
// 返回每日创作者收入、发行商收入、渠道数、短剧数，以及该周期结算单状态。
func (s *Server) adminIncomePeriodSummary(c *gin.Context) {
	startStr := strings.TrimSpace(c.Query("start"))
	endStr := strings.TrimSpace(c.Query("end"))
	if startStr == "" || endStr == "" {
		response.InvalidParam(c, "start 和 end 必填（格式 YYYY-MM-DD）")
		return
	}

	// --- 创作者收入按天汇总 ---
	type creatorDailyAgg struct {
		StatDate     string
		GrossCents   int64
		IncomeCents  int64
		ChannelCount int64
		DramaCount   int64
	}
	var creatorAggs []creatorDailyAgg
	s.db.Table("channel_income_daily").
		Select("stat_date, COALESCE(SUM(gross_cents),0) AS gross_cents, COALESCE(SUM(income_cents),0) AS income_cents, COUNT(DISTINCT channel) AS channel_count, COUNT(DISTINCT drama_id) AS drama_count").
		Where("stat_date >= ? AND stat_date <= ?", startStr, endStr).
		Group("stat_date").
		Order("stat_date asc").
		Scan(&creatorAggs)

	// --- 发行商收入按天汇总 ---
	type distDailyAgg struct {
		StatDate     string
		GrossCents   int64
		IncomeCents  int64
		PlatformCount int64
		DramaCount   int64
	}
	var distAggs []distDailyAgg
	s.db.Table("distributor_income_daily").
		Select("stat_date, COALESCE(SUM(gross_cents),0) AS gross_cents, COALESCE(SUM(income_cents),0) AS income_cents, COUNT(DISTINCT platform) AS platform_count, COUNT(DISTINCT drama_id) AS drama_count").
		Where("stat_date >= ? AND stat_date <= ?", startStr, endStr).
		Group("stat_date").
		Order("stat_date asc").
		Scan(&distAggs)

	distMap := map[string]distDailyAgg{}
	for _, d := range distAggs {
		distMap[d.StatDate] = d
	}

	// --- 合并每日数据 ---
	dailyList := make([]gin.H, 0, len(creatorAggs))
	var totalCreatorGross, totalCreatorIncome int64
	var totalDistGross, totalDistIncome int64
	for _, ca := range creatorAggs {
		da := distMap[ca.StatDate]
		dailyList = append(dailyList, gin.H{
			"stat_date":            ca.StatDate,
			"creator_gross_cents":  ca.GrossCents,
			"creator_income_cents": ca.IncomeCents,
			"creator_channel_count": ca.ChannelCount,
			"creator_drama_count":  ca.DramaCount,
			"distributor_gross_cents":  da.GrossCents,
			"distributor_income_cents": da.IncomeCents,
			"distributor_platform_count": da.PlatformCount,
			"distributor_drama_count": da.DramaCount,
		})
		totalCreatorGross += ca.GrossCents
		totalCreatorIncome += ca.IncomeCents
		totalDistGross += da.GrossCents
		totalDistIncome += da.IncomeCents
	}

	// --- 检查结算单状态 ---
	// 根据日期范围推断 cycle_key
	startDate, _ := time.Parse("2006-01-02", startStr)
	endDate, _ := time.Parse("2006-01-02", endStr)
	cycleKeys := map[string]struct{}{}
	for d := startDate; !d.After(endDate); d = d.AddDate(0, 0, 1) {
		day := d.Day()
		half := "H1"
		if day > 15 {
			half = "H2"
		}
		ck := fmt.Sprintf("%04d-%02d-%s", d.Year(), int(d.Month()), half)
		cycleKeys[ck] = struct{}{}
	}

	// 排序 cycle_key 确保输出有序
	sortedCycles := make([]string, 0, len(cycleKeys))
	for ck := range cycleKeys {
		sortedCycles = append(sortedCycles, ck)
	}
	sort.Strings(sortedCycles)

	// periodStatus 计算整体周期状态（简化视图）
	//   not_generated: 结算单总数 = 0（尚未生成）
	//   pending:       有未结算，无已结算
	//   partial:       既有未结算又有已结算
	//   completed:     全部已结算或作废（无未结算）
	periodStatus := func(unsettled, settled, voidC int64) string {
		total := unsettled + settled + voidC
		if total == 0 {
			return "not_generated"
		}
		if unsettled == 0 {
			return "completed"
		}
		if settled == 0 {
			return "pending"
		}
		return "partial"
	}

	settlementStatuses := make([]gin.H, 0, len(sortedCycles))
	for _, ck := range sortedCycles {
		// --- 创作者结算单 ---
		// 财务周期汇总使用简化视图：将底层 5 个状态映射为 3 个汇总桶
		// unsettled = draft + open + invoiced（未完结）
		// settled   = paid（已付款，终态）
		// void      = void（作废）
		var unsettledCount, settledCount, voidCount int64
		s.db.Table("settlements").Where("cycle_key = ? AND status IN ?", ck, []string{
			model.SettlementStatusDraft, model.SettlementStatusOpen, model.SettlementStatusInvoiced,
		}).Count(&unsettledCount)
		s.db.Table("settlements").Where("cycle_key = ? AND status = ?", ck, model.SettlementStatusPaid).Count(&settledCount)
		s.db.Table("settlements").Where("cycle_key = ? AND status = ?", ck, model.SettlementStatusVoid).Count(&voidCount)

		// 创作者结算单金额汇总
		var creatorAmount struct {
			GrossCents int64
			NetCents   int64
		}
		s.db.Table("settlements").
			Select("COALESCE(SUM(gross_cents),0) AS gross_cents, COALESCE(SUM(net_cents),0) AS net_cents").
			Where("cycle_key = ?", ck).Scan(&creatorAmount)

		// --- 发行商结算单 ---
		// 简化视图：pending_payment + payment_submitted → unsettled, settled → settled
		var distUnsettled, distSettled int64
		s.db.Table("distributor_settlements").Where("cycle_key = ? AND status IN ?", ck, []string{
			model.DistSettlementPendingPayment, model.DistSettlementPaymentSubmitted,
		}).Count(&distUnsettled)
		s.db.Table("distributor_settlements").Where("cycle_key = ? AND status = ?", ck, model.DistSettlementSettled).Count(&distSettled)

		// 发行商结算单金额汇总
		var distAmount struct {
			GrossCents  int64
			PayableCents int64
		}
		s.db.Table("distributor_settlements").
			Select("COALESCE(SUM(gross_cents),0) AS gross_cents, COALESCE(SUM(payable_cents),0) AS payable_cents").
			Where("cycle_key = ?", ck).Scan(&distAmount)

		settlementStatuses = append(settlementStatuses, gin.H{
			"cycle_key":           ck,
			"unsettled_count":     unsettledCount,
			"settled_count":       settledCount,
			"void_count":          voidCount,
			"total_count":         unsettledCount + settledCount + voidCount,
			"period_status":       periodStatus(unsettledCount, settledCount, voidCount),
			"gross_cents":         creatorAmount.GrossCents,
			"net_cents":           creatorAmount.NetCents,
			"distributor": gin.H{
				"unsettled_count": distUnsettled,
				"settled_count":   distSettled,
				"total_count":     distUnsettled + distSettled,
				"period_status":   periodStatus(distUnsettled, distSettled, 0),
				"gross_cents":     distAmount.GrossCents,
				"payable_cents":   distAmount.PayableCents,
			},
		})
	}

	response.OK(c, gin.H{
		"start": startStr,
		"end":   endStr,
		"daily": dailyList,
		"totals": gin.H{
			"creator_gross_cents":      totalCreatorGross,
			"creator_income_cents":     totalCreatorIncome,
			"distributor_gross_cents":  totalDistGross,
			"distributor_income_cents": totalDistIncome,
		},
		"settlement_statuses": settlementStatuses,
	})
}
