package handler

import (
	"encoding/json"
	"strings"

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
	list := make([]gin.H, 0, len(items))
	for _, item := range items {
		list = append(list, channelIncomeView(item, dramaTitles[item.DramaID]))
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

func channelIncomeView(item model.ChannelIncomeDaily, dramaTitle string) gin.H {
	return gin.H{
		"id":            item.ID,
		"drama_id":      item.DramaID,
		"drama_title":   dramaTitle,
		"channel":       item.Channel,
		"stat_date":     item.StatDate,
		"creator_id":    item.CreatorID,
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
	response.OK(c, channelIncomeView(updated, titles[updated.DramaID]))
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
