package handler

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type incomeImportRowReport struct {
	RowNo           int    `json:"row_no"`
	Title           string `json:"title"`
	Channel         string `json:"channel"`
	StatDate        string `json:"stat_date"`
	IncomeCents     int64  `json:"income_cents"`
	Status          string `json:"status"` // created / updated / unchanged / duplicate / failed
	Message         string `json:"message"`
	ExistingCents   *int64 `json:"existing_cents,omitempty"`
	DeltaCents      int64  `json:"delta_cents"`
	DramaID         uint64 `json:"drama_id,omitempty"`
	CreatorID       uint64 `json:"creator_id,omitempty"`
	DuplicateOfRow  int    `json:"duplicate_of_row,omitempty"`
	ChannelIncomeID uint64 `json:"channel_income_id,omitempty"`
}

// adminDownloadIncomeTemplate —— GET /v1/admin/finance/income/template.xlsx
// 生成「短剧名称 + 渠道 + 收益 + 日期 + 短剧ID(选填)」五列收益导入模板。
// E 列短剧ID 用于解决名称重复时的歧义；不填则按名称匹配，名称唯一才能定位。
func (s *Server) adminDownloadIncomeTemplate(c *gin.Context) {
	xl := excelize.NewFile()
	defer xl.Close()

	sheet := "Sheet1"
	headers := []string{"短剧名称", "渠道", "收益", "日期", "短剧ID(选填,名称重复时必填)"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = xl.SetCellValue(sheet, cell, h)
	}
	samples := [][]interface{}{
		{"总裁的逆袭新娘", "抖音", 123.45, "2026-05-26", ""},
		{"总裁的逆袭新娘", "快手", 88.00, "2026-05-27", 42},
	}
	for r, row := range samples {
		for col, v := range row {
			cell, _ := excelize.CoordinatesToCellName(col+1, r+2)
			_ = xl.SetCellValue(sheet, cell, v)
		}
	}
	_ = xl.SetColWidth(sheet, "A", "A", 28)
	_ = xl.SetColWidth(sheet, "B", "D", 20)
	_ = xl.SetColWidth(sheet, "E", "E", 30)

	var buf bytes.Buffer
	if err := xl.Write(&buf); err != nil {
		response.ServerError(c, "生成收益导入模板失败")
		return
	}
	filename := "收益导入模板.xlsx"
	escaped := url.QueryEscape(filename)
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"income-template.xlsx\"; filename*=UTF-8''%s", escaped))
	c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", buf.Bytes())
}

// adminImportDailyIncome —— POST /v1/admin/finance/income/import
// 财务上传 xlsx，导入**第三方渠道**每日收益。本平台自有付费收入走支付分账，无需导入。
//
// 表格列（第 1 行表头，从第 2 行起读）：
//
//	A 列：短剧名称   B 列：渠道(抖音/快手/腾讯/B站/视频号…)   C 列：收益   D 列：日期
//	E 列：短剧ID(选填)
//
// 剧目匹配：E 列短剧ID 优先 —— 填了就按 ID 直接定位（仍校验名称一致性，不一致只给 warning）；
// 没填则按 A 列名称匹配，名称在库内不唯一时该行 fail 并提示填 E 列。
// 增量导入：
//   - 文件内同一 (剧ID, 渠道, 日期) 重复行会跳过并在 row_reports 标记 duplicate。
//   - 库内已存在且金额相同 → unchanged，不重复入账。
//   - 库内已存在但金额不同 → updated，按差额调整创作者账面。
//   - 库内不存在 → created，新增并全额入账。
func (s *Server) adminImportDailyIncome(c *gin.Context) {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		response.InvalidParam(c, "请在 form-data 的 file 字段上传 xlsx 文件")
		return
	}
	f, err := fileHeader.Open()
	if err != nil {
		response.ServerError(c, "读取上传文件失败")
		return
	}
	defer f.Close()

	xl, err := excelize.OpenReader(f)
	if err != nil {
		response.InvalidParam(c, "文件不是有效的 xlsx")
		return
	}
	defer xl.Close()

	sheet := xl.GetSheetName(0)
	if sheet == "" {
		response.InvalidParam(c, "xlsx 没有可读的工作表")
		return
	}
	rows, err := xl.GetRows(sheet)
	if err != nil {
		response.ServerError(c, "解析表格失败")
		return
	}
	if len(rows) <= 1 {
		response.InvalidParam(c, "表格没有数据行（第 1 行为表头）")
		return
	}

	type parsedRow struct {
		rowNo          int
		title          string
		channel        string
		incomeCents    int64
		statDate       string
		explicitDramaID uint64 // E 列填了才非 0
	}
	var parsed []parsedRow
	rowReports := make([]incomeImportRowReport, 0)
	seenRows := map[string]int{}

	for i := 1; i < len(rows); i++ { // 跳过表头
		row := rows[i]
		lineNo := i + 1
		// 完全空行（包括所有 cell 为空白）silently skip，避免财务夹空行报一堆 failed
		if rowIsBlank(row) {
			continue
		}
		if len(row) < 4 {
			rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Status: "failed", Message: "列数不足（需 短剧名称/渠道/收益/日期，E 列短剧ID 选填）"})
			continue
		}
		title := strings.TrimSpace(row[0])
		channel := strings.TrimSpace(row[1])
		if title == "" || channel == "" {
			rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Title: title, Channel: channel, Status: "failed", Message: "短剧名称/渠道不能为空"})
			continue
		}
		if runeLen(channel) > 32 {
			rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Title: title, Channel: channel, Status: "failed", Message: "渠道字段过长（最多 32 个字符）"})
			continue
		}
		yuanRaw := strings.ReplaceAll(strings.TrimSpace(row[2]), ",", "") // 容忍千分位
		yuan, eInc := strconv.ParseFloat(yuanRaw, 64)
		if eInc != nil || yuan < 0 {
			rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Title: title, Channel: channel, Status: "failed", Message: "收益金额不合法"})
			continue
		}
		statDate, ok := normalizeDate(strings.TrimSpace(row[3]))
		if !ok {
			rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Title: title, Channel: channel, Status: "failed", Message: "日期格式应为 YYYY-MM-DD"})
			continue
		}
		var explicitDramaID uint64
		if len(row) >= 5 {
			if idStr := strings.TrimSpace(row[4]); idStr != "" {
				// Excel 中数字单元格读出来可能是 "42" 或 "42.0"，TrimRight 一下小数点尾巴
				if dot := strings.IndexByte(idStr, '.'); dot >= 0 {
					tail := strings.TrimRight(idStr[dot+1:], "0")
					if tail == "" {
						idStr = idStr[:dot]
					}
				}
				v, eID := strconv.ParseUint(idStr, 10, 64)
				if eID != nil || v == 0 {
					rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Title: title, Channel: channel, StatDate: statDate, Status: "failed", Message: "E 列 短剧ID 不合法"})
					continue
				}
				explicitDramaID = v
			}
		}
		key := incomeImportKey(title, explicitDramaID, channel, statDate)
		if firstRow, exists := seenRows[key]; exists {
			rowReports = append(rowReports, incomeImportRowReport{
				RowNo:          lineNo,
				Title:          title,
				Channel:        channel,
				StatDate:       statDate,
				IncomeCents:    int64(math.Round(yuan * 100)),
				DramaID:        explicitDramaID,
				Status:         "duplicate",
				Message:        fmt.Sprintf("同一文件内重复，已跳过；首次出现在第%d行", firstRow),
				DuplicateOfRow: firstRow,
			})
			continue
		}
		seenRows[key] = lineNo
		parsed = append(parsed, parsedRow{
			rowNo:          lineNo,
			title:          title,
			channel:        channel,
			incomeCents:    int64(math.Round(yuan * 100)),
			statDate:       statDate,
			explicitDramaID: explicitDramaID,
		})
	}

	batchNo := generateIncomeImportBatchNo()
	createdRows := 0
	updatedRows := 0
	unchangedRows := 0
	duplicateRows := 0
	failedRows := 0
	var totalDelta int64
	err = s.db.Transaction(func(tx *gorm.DB) error {
		for _, pr := range parsed {
			report := incomeImportRowReport{
				RowNo:       pr.rowNo,
				Title:       pr.title,
				Channel:     pr.channel,
				StatDate:    pr.statDate,
				IncomeCents: pr.incomeCents,
			}

			// 剧目匹配：E 列 dramaID 优先 → 否则按名称且要求唯一。
			var drama model.Drama
			if pr.explicitDramaID != 0 {
				if err := tx.Select("id", "title", "creator_id").
					Where("id = ?", pr.explicitDramaID).First(&drama).Error; err != nil {
					if isNotFound(err) {
						report.Status = "failed"
						report.Message = fmt.Sprintf("E 列 短剧ID=%d 不存在，已跳过", pr.explicitDramaID)
						rowReports = append(rowReports, report)
						continue
					}
					return err
				}
				if drama.Title != pr.title {
					// 名称与库内不一致只 warning：以 ID 为准
					report.Message = fmt.Sprintf("名称与库内不一致（库内=%q，以 ID=%d 为准）", drama.Title, pr.explicitDramaID)
				}
			} else {
				var dramas []model.Drama
				if err := tx.Select("id", "title", "creator_id").Where("title = ?", pr.title).Find(&dramas).Error; err != nil {
					return err
				}
				if len(dramas) == 0 {
					report.Status = "failed"
					report.Message = "短剧不存在，已跳过"
					rowReports = append(rowReports, report)
					continue
				}
				if len(dramas) > 1 {
					ids := make([]string, 0, len(dramas))
					for _, d := range dramas {
						ids = append(ids, strconv.FormatUint(d.ID, 10))
					}
					report.Status = "failed"
					report.Message = fmt.Sprintf("短剧名称不唯一（匹配到 %d 部，ID=[%s]），请在 E 列填写「短剧ID」定位", len(dramas), strings.Join(ids, ","))
					rowReports = append(rowReports, report)
					continue
				}
				drama = dramas[0]
			}
			report.DramaID = drama.ID
			if drama.CreatorID == nil {
				report.Status = "failed"
				report.Message = "短剧未绑定创作者，已跳过"
				rowReports = append(rowReports, report)
				continue
			}
			creatorID := *drama.CreatorID
			report.CreatorID = creatorID

			// 渠道明细：覆盖语义，算差额。
			var existing model.ChannelIncomeDaily
			errFind := tx.Where("drama_id = ? AND channel = ? AND stat_date = ?",
				drama.ID, pr.channel, pr.statDate).First(&existing).Error
			var delta int64
			if errFind == nil {
				report.ChannelIncomeID = existing.ID
				old := existing.IncomeCents
				report.ExistingCents = &old
				delta = pr.incomeCents - existing.IncomeCents
				if delta == 0 {
					report.Status = "unchanged"
					report.Message = "库内已存在且金额相同，未重复入账"
					unchangedRows++
				} else {
					if err := tx.Model(&model.ChannelIncomeDaily{}).Where("id = ?", existing.ID).
						Updates(map[string]interface{}{
							"income_cents":  pr.incomeCents,
							"creator_id":    creatorID,
							"batch_no":      batchNo,
							"import_row_no": pr.rowNo,
						}).Error; err != nil {
						return err
					}
					report.Status = "updated"
					report.Message = "库内已存在，已按差额覆盖更新"
					updatedRows++
				}
			} else if isNotFound(errFind) {
				delta = pr.incomeCents
				newRow := model.ChannelIncomeDaily{
					DramaID: drama.ID, Channel: pr.channel, StatDate: pr.statDate,
					CreatorID: creatorID, IncomeCents: pr.incomeCents,
					BatchNo: batchNo, ImportRowNo: pr.rowNo,
				}
				if err := tx.Create(&newRow).Error; err != nil {
					return err
				}
				report.ChannelIncomeID = newRow.ID
				report.Status = "created"
				report.Message = "新增渠道收益记录"
				createdRows++
			} else {
				return errFind
			}
			report.DeltaCents = delta

			if delta != 0 {
				// creator_stats_daily 按 (creator,drama,date) 累加差额，保证创作者收益/看板含第三方收入。
				if err := s.bumpCreatorStatsIncome(tx, creatorID, drama.ID, pr.statDate, delta); err != nil {
					return err
				}
				if err := tx.Model(&model.Creator{}).
					Clauses(clause.Locking{Strength: "UPDATE"}).
					Where("id = ?", creatorID).
					Updates(map[string]interface{}{
						"total_income_cents": gorm.Expr("total_income_cents + ?", delta),
						"balance_cents":      gorm.Expr("balance_cents + ?", delta),
					}).Error; err != nil {
					return err
				}
				totalDelta += delta
			}
			rowReports = append(rowReports, report)
		}
		return nil
	})
	if err != nil {
		response.ServerError(c, "导入失败，已回滚")
		return
	}

	for _, report := range rowReports {
		switch report.Status {
		case "duplicate":
			duplicateRows++
		case "failed":
			failedRows++
		}
	}

	result := gin.H{
		"batch_no":           batchNo,
		"processed_rows":     len(rowReports),
		"imported_rows":      createdRows + updatedRows,
		"created_rows":       createdRows,
		"updated_rows":       updatedRows,
		"unchanged_rows":     unchangedRows,
		"duplicate_rows":     duplicateRows,
		"failed_rows":        failedRows,
		"income_delta_cents": totalDelta,
		"row_reports":        rowReports,
		"errors":             incomeImportErrors(rowReports),
	}
	if err := s.saveIncomeImportBatch(c, batchNo, fileHeader.Filename, result, rowReports); err != nil {
		response.ServerError(c, "导入成功但批次记录保存失败")
		return
	}
	response.OK(c, result)
}

// rowIsBlank 判断一行 Excel 是否全为空白（excelize.GetRows 偶尔会把尾部全空行也返回）。
func rowIsBlank(row []string) bool {
	for _, cell := range row {
		if strings.TrimSpace(cell) != "" {
			return false
		}
	}
	return true
}

func incomeImportKey(title string, dramaID uint64, channel, statDate string) string {
	return strings.TrimSpace(title) + "\x00" +
		strconv.FormatUint(dramaID, 10) + "\x00" +
		strings.TrimSpace(channel) + "\x00" + statDate
}

func incomeImportErrors(reports []incomeImportRowReport) []string {
	out := make([]string, 0)
	for _, report := range reports {
		if report.Status == "failed" || report.Status == "duplicate" {
			out = append(out, fmt.Sprintf("第%d行：%s", report.RowNo, report.Message))
		}
	}
	return out
}

func generateIncomeImportBatchNo() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("IMP%s", time.Now().Format("20060102150405"))
	}
	return fmt.Sprintf("IMP%s%s", time.Now().Format("20060102150405"), strings.ToUpper(hex.EncodeToString(b[:])))
}

// bumpCreatorStatsIncome 对 (creator,drama,date) 的 creator_stats_daily.income_cents 累加 delta；行不存在则建。
func (s *Server) bumpCreatorStatsIncome(tx *gorm.DB, creatorID, dramaID uint64, statDate string, delta int64) error {
	var row model.CreatorStatsDaily
	err := tx.Where("creator_id = ? AND drama_id = ? AND stat_date = ?", creatorID, dramaID, statDate).First(&row).Error
	if err == nil {
		return tx.Model(&model.CreatorStatsDaily{}).Where("id = ?", row.ID).
			Update("income_cents", gorm.Expr("income_cents + ?", delta)).Error
	}
	if !isNotFound(err) {
		return err
	}
	return tx.Create(&model.CreatorStatsDaily{
		CreatorID: creatorID, DramaID: dramaID, StatDate: statDate, IncomeCents: delta,
	}).Error
}

// normalizeDate 把常见日期写法归一成 YYYY-MM-DD。
// Excel 把日期格式化输出时会用本地短格式（如 2026/5/26 无前导零），全部兼容。
func normalizeDate(s string) (string, bool) {
	layouts := []string{
		"2006-01-02", "2006/01/02", "2006.01.02",
		"2006-1-2", "2006/1/2", "2006.1.2",
		"2006-1-02", "2006/1/02",
		"2006-01-2", "2006/01/2",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02"), true
		}
	}
	return "", false
}
