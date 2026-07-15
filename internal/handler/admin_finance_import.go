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
	GrossCents      int64  `json:"gross_cents"`     // 总收益
	ShareRatioBP    int    `json:"share_ratio_bp"`  // 实际采用的分成比例（基点）
	IncomeCents     int64  `json:"income_cents"`    // 创作者实得 = 总收益×比例
	Status          string `json:"status"`          // created / updated / unchanged / duplicate / failed
	Message         string `json:"message"`
	ExistingCents   *int64 `json:"existing_cents,omitempty"` // 旧的创作者实得
	DeltaCents      int64  `json:"delta_cents"`
	DramaID         uint64 `json:"drama_id,omitempty"`
	CreatorID       uint64 `json:"creator_id,omitempty"`
	DuplicateOfRow  int    `json:"duplicate_of_row,omitempty"`
	ChannelIncomeID uint64 `json:"channel_income_id,omitempty"`
	// 发行商收益信息（自动生成）
	DistributorID       uint64 `json:"distributor_id,omitempty"`
	DistributorName     string `json:"distributor_name,omitempty"`
	DistributorIncome   int64  `json:"distributor_income_cents,omitempty"`  // 发行商实得（55%）
	DistributorStatus   string `json:"distributor_status,omitempty"`        // created / skipped / failed
	DistributorMessage  string `json:"distributor_message,omitempty"`
}

// channelToPlatform 将中文渠道名映射为发行商系统平台 key。
// 匹配不上返回空串（表示该渠道没有对应发行商平台，只走创作者分成）。
func channelToPlatform(channel string) string {
	switch strings.TrimSpace(channel) {
	case "抖音":
		return model.PlatformDouyin
	case "快手":
		return model.PlatformKuaishou
	case "视频号", "微信视频号":
		return model.PlatformWechatVideo
	case "B站", "哔哩哔哩", "哔哩":
		return model.PlatformBilibili
	default:
		return ""
	}
}

// adminDownloadIncomeTemplate —— GET /v1/admin/finance/income/template.xlsx
// 生成「短剧名称 + 渠道 + 总收益 + 分成比例 + 日期 + 短剧ID(选填)」六列收益导入模板。
// 分成比例(D 列)：支持 50 / 50% / 0.5 三种写法，均表示 50%；留空则按该渠道的全局配置比例。
// 短剧ID(F 列)用于解决名称重复时的歧义；不填则按名称匹配，名称唯一才能定位。
func (s *Server) adminDownloadIncomeTemplate(c *gin.Context) {
	xl := excelize.NewFile()
	defer xl.Close()

	sheet := "Sheet1"
	headers := []string{"短剧名称", "渠道", "总收益", "分成比例(如50或50%或0.5,留空按配置)", "日期", "短剧ID(选填,名称重复时必填)"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = xl.SetCellValue(sheet, cell, h)
	}
	samples := [][]interface{}{
		{"总裁的逆袭新娘", "抖音", 123.45, "50%", "2026-05-26", ""},
		{"总裁的逆袭新娘", "快手", 88.00, "", "2026-05-27", 42},
	}
	for r, row := range samples {
		for col, v := range row {
			cell, _ := excelize.CoordinatesToCellName(col+1, r+2)
			_ = xl.SetCellValue(sheet, cell, v)
		}
	}
	_ = xl.SetColWidth(sheet, "A", "A", 28)
	_ = xl.SetColWidth(sheet, "B", "C", 16)
	_ = xl.SetColWidth(sheet, "D", "D", 30)
	_ = xl.SetColWidth(sheet, "E", "E", 16)
	_ = xl.SetColWidth(sheet, "F", "F", 30)

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
//	A 列：短剧名称   B 列：渠道(抖音/快手/腾讯/B站/视频号…)   C 列：总收益
//	D 列：分成比例(50 / 50% / 0.5，留空按渠道全局配置)   E 列：日期   F 列：短剧ID(选填)
//
// 分成计算：创作者实得 = round(总收益 × 比例)。比例优先取行内 D 列；D 列留空则取
//
//	该渠道的全局配置(income.share_ratio.<渠道> → income.share_ratio.default)；
//	都没配置则回落 100% 并在该行给出 warning，避免漏配时金额归零。
//
// 剧目匹配：F 列短剧ID 优先 —— 填了就按 ID 直接定位（仍校验名称一致性，不一致只给 warning）；
// 没填则按 A 列名称匹配，名称在库内不唯一时该行 fail 并提示填 F 列。
// 增量导入：
//   - 文件内同一 (剧ID, 渠道, 日期) 重复行会跳过并在 row_reports 标记 duplicate。
//   - 库内已存在且「创作者实得」相同 → unchanged，不重复入账。
//   - 库内已存在但不同 → updated，按差额调整创作者账面。
//   - 库内不存在 → created，新增并按实得入账。
//
// dry_run：带 ?dry_run=1 时只解析+试算，给出逐行 delta 报告，不写库、不入账、不存批次。
func (s *Server) adminImportDailyIncome(c *gin.Context) {
	dryRun := c.Query("dry_run") == "1" || c.Query("dry_run") == "true"
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
	// 2026-07-06 改：吴建棉反馈"收益报表上传，日期对不上"
	// 决定走默认 GetRows（不加 RawCellValue: true）—— 让 excelize 按 cell.NumFmt 格式化成字符串
	// 配合 parseExcelDateCell 中的 normalizeDate 接受 m/d/yy / yyyy/m/d / yyyy-mm-dd 等格式
	// 为什么不用 RawCellValue:
	//   - RawCellValue 拿序列号 (float string)，需要 excelize.ExcelDateToTime 转
	//   - 但 excelize.ExcelDateToTime 跟 WPS 真实算法差几天（已验证 sn=46204 差 2 天）
	//   - 改成自己实现 Excel 序列号算法又有 Go AddDate 闰年 bug
	// 务实：让 excelize 帮我们格式化 → normalizeDate 接受格式化后的字符串
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
		rowNo           int
		title           string
		channel         string
		grossCents      int64
		ratioBP         int  // 行内填的比例；rowHasRatio=false 时无效，待回落配置
		rowHasRatio     bool // D 列是否填了比例
		statDate        string
		explicitDramaID uint64 // F 列填了才非 0
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
		if len(row) < 5 {
			rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Status: "failed", Message: "列数不足（需 短剧名称/渠道/总收益/分成比例/日期，F 列短剧ID 选填；比例可留空）"})
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
			rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Title: title, Channel: channel, Status: "failed", Message: "总收益金额不合法"})
			continue
		}
		ratioBP, rowHasRatio, ratioErr := parseShareRatioBP(strings.TrimSpace(row[3]))
		if ratioErr != "" {
			rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Title: title, Channel: channel, Status: "failed", Message: ratioErr})
			continue
		}
		// 2026-07-06 改：先按 Excel 序列号解析（RawCellValue 模式下日期类型是 float 字符串），
		// 解析失败再走字符串日期解析（兼容财务手打 "2024/7/3" 的情况）
		statDate, ok := parseExcelDateCell(strings.TrimSpace(row[4]))
		if !ok {
			rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Title: title, Channel: channel, Status: "failed", Message: "日期格式应为 YYYY-MM-DD 或 Excel 日期单元格"})
			continue
		}
		var explicitDramaID uint64
		if len(row) >= 6 {
			if idStr := strings.TrimSpace(row[5]); idStr != "" {
				// Excel 中数字单元格读出来可能是 "42" 或 "42.0"，TrimRight 一下小数点尾巴
				if dot := strings.IndexByte(idStr, '.'); dot >= 0 {
					tail := strings.TrimRight(idStr[dot+1:], "0")
					if tail == "" {
						idStr = idStr[:dot]
					}
				}
				v, eID := strconv.ParseUint(idStr, 10, 64)
				if eID != nil || v == 0 {
					rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Title: title, Channel: channel, StatDate: statDate, Status: "failed", Message: "F 列 短剧ID 不合法"})
					continue
				}
				explicitDramaID = v
			}
		}
		grossCents := int64(math.Round(yuan * 100))
		key := incomeImportKey(title, explicitDramaID, channel, statDate)
		if firstRow, exists := seenRows[key]; exists {
			rowReports = append(rowReports, incomeImportRowReport{
				RowNo:          lineNo,
				Title:          title,
				Channel:        channel,
				StatDate:       statDate,
				GrossCents:     grossCents,
				DramaID:        explicitDramaID,
				Status:         "duplicate",
				Message:        fmt.Sprintf("同一文件内重复，已跳过；首次出现在第%d行", firstRow),
				DuplicateOfRow: firstRow,
			})
			continue
		}
		seenRows[key] = lineNo
		parsed = append(parsed, parsedRow{
			rowNo:           lineNo,
			title:           title,
			channel:         channel,
			grossCents:      grossCents,
			ratioBP:         ratioBP,
			rowHasRatio:     rowHasRatio,
			statDate:        statDate,
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
			// 解析比例：行内优先；行内留空则查渠道配置；都没有回落 100% 并 warning。
			ratioBP := pr.ratioBP
			var ratioWarn string
			if !pr.rowHasRatio {
				if cfgBP, ok := s.channelShareRatioBP(pr.channel); ok {
					ratioBP = cfgBP
				} else {
					ratioBP = model.ShareRatioBPFull
					ratioWarn = "未填比例且渠道未配置分成比例，按 100% 入账"
				}
			}
			incomeCents := int64(math.Round(float64(pr.grossCents) * float64(ratioBP) / float64(model.ShareRatioBPFull)))
			report := incomeImportRowReport{
				RowNo:        pr.rowNo,
				Title:        pr.title,
				Channel:      pr.channel,
				StatDate:     pr.statDate,
				GrossCents:   pr.grossCents,
				ShareRatioBP: ratioBP,
				IncomeCents:  incomeCents,
				Message:      ratioWarn,
			}

			// mergeWarn 把比例/名称等 warning 拼到状态说明前面，避免被覆盖丢失。
			mergeWarn := func(msg string) string {
				if report.Message != "" {
					return report.Message + "；" + msg
				}
				return msg
			}

			// 剧目匹配：F 列 dramaID 优先 → 否则按名称且要求唯一。
			var drama model.Drama
			if pr.explicitDramaID != 0 {
				if err := tx.Select("id", "title", "creator_id").
					Where("id = ?", pr.explicitDramaID).First(&drama).Error; err != nil {
					if isNotFound(err) {
						report.Status = "failed"
						report.Message = fmt.Sprintf("F 列 短剧ID=%d 不存在，已跳过", pr.explicitDramaID)
						rowReports = append(rowReports, report)
						continue
					}
					return err
				}
				if drama.Title != pr.title {
					// 名称与库内不一致只 warning：以 ID 为准
					report.Message = mergeWarn(fmt.Sprintf("名称与库内不一致（库内=%q，以 ID=%d 为准）", drama.Title, pr.explicitDramaID))
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
					report.Message = fmt.Sprintf("短剧名称不唯一（匹配到 %d 部，ID=[%s]），请在 F 列填写「短剧ID」定位", len(dramas), strings.Join(ids, ","))
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

			// 渠道明细：覆盖语义，按「创作者实得」算差额。
			var existing model.ChannelIncomeDaily
			errFind := tx.Where("drama_id = ? AND channel = ? AND stat_date = ?",
				drama.ID, pr.channel, pr.statDate).First(&existing).Error
			var delta int64
			if errFind == nil {
				report.ChannelIncomeID = existing.ID
				old := existing.IncomeCents
				report.ExistingCents = &old
				delta = incomeCents - existing.IncomeCents
				if delta == 0 {
					report.Status = "unchanged"
					report.Message = mergeWarn("库内已存在且创作者实得相同，未重复入账")
					unchangedRows++
				} else {
					if !dryRun {
						if err := tx.Model(&model.ChannelIncomeDaily{}).Where("id = ?", existing.ID).
							Updates(map[string]interface{}{
								"gross_cents":    pr.grossCents,
								"share_ratio_bp": ratioBP,
								"income_cents":   incomeCents,
								"creator_id":     creatorID,
								"batch_no":       batchNo,
								"import_row_no":  pr.rowNo,
							}).Error; err != nil {
							return err
						}
					}
					report.Status = "updated"
					report.Message = mergeWarn("库内已存在，已按差额覆盖更新")
					updatedRows++
				}
			} else if isNotFound(errFind) {
				delta = incomeCents
				newRow := model.ChannelIncomeDaily{
					DramaID: drama.ID, Channel: pr.channel, StatDate: pr.statDate,
					CreatorID: creatorID, GrossCents: pr.grossCents, ShareRatioBP: ratioBP,
					IncomeCents: incomeCents,
					BatchNo:     batchNo, ImportRowNo: pr.rowNo,
				}
				if !dryRun {
					if err := tx.Create(&newRow).Error; err != nil {
						return err
					}
					report.ChannelIncomeID = newRow.ID
				}
				report.Status = "created"
				report.Message = mergeWarn("新增渠道收益记录")
				createdRows++
			} else {
				return errFind
			}
			report.DeltaCents = delta

			if delta != 0 {
				if !dryRun {
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
				}
				totalDelta += delta
		}

		// ---- 自动生成发行商收益记录 ----
		// 渠道映射到发行商平台 key；匹配不上则跳过（该渠道没有发行商体系）
		platformKey := channelToPlatform(pr.channel)
		if platformKey != "" && report.Status != "failed" {
			distID, distInc, distStatus, distMsg := s.generateDistributorIncome(tx, drama.ID, platformKey, pr.statDate, pr.grossCents, batchNo, pr.rowNo, dryRun)
			report.DistributorID = distID
			report.DistributorIncome = distInc
			report.DistributorStatus = distStatus
			report.DistributorMessage = distMsg
			// 填充发行商名称
			if distID > 0 && (distStatus == "created" || distStatus == "skipped") {
				var dist model.Distributor
				tx.Select("id, name, org_name, nickname").Where("id = ?", distID).First(&dist)
				report.DistributorName = distributorName(&dist)
			}
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
		"dry_run":            dryRun,
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
	// dry_run 只试算不落库，也不记录批次。
	if dryRun {
		result["batch_no"] = ""
		result["note"] = "试算结果（dry_run），未写库、未入账、未记录批次。去掉 dry_run 参数即可正式导入。"
		response.OK(c, result)
		return
	}
	if err := s.saveIncomeImportBatch(c, batchNo, fileHeader.Filename, result, rowReports); err != nil {
		response.ServerError(c, "导入成功但批次记录保存失败")
		return
	}
	response.OK(c, result)
}

// parseShareRatioBP 解析分成比例单元格 → 基点(10000=100%)。
// 支持 "50" / "50%" / "0.5" 三种写法均表示 50%；空字符串返回 (0,false,"") 表示行内未填、待回落配置。
// 返回 (基点, 是否填了, 错误说明)。
func parseShareRatioBP(raw string) (int, bool, string) {
	if raw == "" {
		return 0, false, ""
	}
	s := strings.TrimSpace(raw)
	isPercent := strings.HasSuffix(s, "%")
	s = strings.TrimSuffix(s, "%")
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v < 0 {
		return 0, false, "分成比例不合法（支持 50 / 50% / 0.5）"
	}
	var bp int
	switch {
	case isPercent:
		bp = int(math.Round(v * 100)) // 50% → 5000
	case v <= 1:
		bp = int(math.Round(v * 10000)) // 0.5 → 5000
	default:
		bp = int(math.Round(v * 100)) // 50 → 5000
	}
	if bp < 0 || bp > model.ShareRatioBPFull {
		return 0, false, "分成比例须在 0~100% 之间"
	}
	return bp, true, ""
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

// generateDistributorIncome 在导入渠道收益时，自动为认领了该剧该平台的发行商生成收益记录。
// 返回 (发行商ID, 发行商实得分, 状态, 消息)。状态：created=新建 / skipped=无人认领或已存在 / failed=出错。
func (s *Server) generateDistributorIncome(tx *gorm.DB, dramaID uint64, platform, statDate string, grossCents int64, batchNo string, rowNo int, dryRun bool) (uint64, int64, string, string) {
	// 查找认领了该剧该平台的活跃发行商
	var dds []model.DistributorDrama
	tx.Where("drama_id = ? AND status IN ?", dramaID, []string{"authorized", "active"}).Find(&dds)
	var matchedDD *model.DistributorDrama
	for i := range dds {
		platforms := parsePlatforms(dds[i].Platforms)
		for _, p := range platforms {
			if p == platform {
				matchedDD = &dds[i]
				break
			}
		}
		if matchedDD != nil {
			break
		}
	}
	if matchedDD == nil {
		return 0, 0, "skipped", "该剧该平台无发行商认领，跳过发行商收益"
	}

	// 检查是否已存在同日记录（幂等，不重复入账）
	var existing model.DistributorIncomeDaily
	errFind := tx.Where("distributor_id = ? AND drama_id = ? AND platform = ? AND stat_date = ?",
		matchedDD.DistributorID, dramaID, platform, statDate).First(&existing).Error
	if errFind == nil {
		return matchedDD.DistributorID, 0, "skipped", fmt.Sprintf("发行商收益已存在（batch=%s），跳过", existing.BatchNo)
	}

	shareBP := 5500 // 发行商 55%
	incomeCents := grossCents * int64(shareBP) / 10000

	if dryRun {
		return matchedDD.DistributorID, incomeCents, "created", fmt.Sprintf("试算：发行商实得 %.2f 元（55%%）", float64(incomeCents)/100)
	}

	inc := model.DistributorIncomeDaily{
		DistributorID: matchedDD.DistributorID,
		DramaID:       dramaID,
		Platform:      platform,
		StatDate:      statDate,
		GrossCents:    grossCents,
		ShareRatioBP:  shareBP,
		IncomeCents:   incomeCents,
		BatchNo:       batchNo,
		ImportRowNo:   rowNo,
	}
	if err := tx.Create(&inc).Error; err != nil {
		return matchedDD.DistributorID, 0, "failed", fmt.Sprintf("创建发行商收益失败: %v", err)
	}

	// 累加发行商收益
	if err := tx.Model(&model.Distributor{}).
		Where("id = ?", matchedDD.DistributorID).
		UpdateColumns(map[string]interface{}{
			"total_income_cents": gorm.Expr("total_income_cents + ?", incomeCents),
			"balance_cents":      gorm.Expr("balance_cents + ?", incomeCents),
		}).Error; err != nil {
		return matchedDD.DistributorID, 0, "failed", fmt.Sprintf("更新发行商余额失败: %v", err)
	}

	return matchedDD.DistributorID, incomeCents, "created", fmt.Sprintf("发行商实得 %.2f 元（55%%）", float64(incomeCents)/100)
}

// normalizeDate 把常见日期写法归一成 YYYY-MM-DD。
// Excel 把日期格式化输出时会用本地短格式（如 2026/5/26 无前导零），全部兼容。
// parseExcelDateCell 解析"日期类型"或"日期字符串"单元格
// 2026-07-06 改：吴建棉反馈"收益报表上传，日期对不上"
//
// 背景：WPS 写日期按"Excel 算法"（假装 1900 闰年），
//       excelize v2.10.1 的 ExcelDateToTime 按"真实日历"算（1899-12-30 + N 天），
//       两者差几天。WPS 真实序列号映射：
//         "2026/7/3" → sn=46204（Excel 算法）→ ExcelDateToTime 给 2026-07-01（差 2 天）
//
// 务实修法：完全不用 excelize.ExcelDateToTime，不用 RawCellValue。
//   - excelize 默认 GetRows 把日期单元格按 cell.NumFmt 格式化成字符串
//   - normalizeDate 接受所有常见日期格式（m/d/yy / mm/dd/yyyy / yyyy-mm-dd 等）
//   - 财务手打 "2024/7/3" 字符串也走 normalizeDate
//
// GetRows 行为（默认模式，不加 RawCellValue: true）：
//   - NumFmt=14 (m/d/yy)     -> "07-15-24"   → normalizeDate 接受
//   - NumFmt 自定义 yyyy/m/d -> "2024/7/15"  → normalizeDate 接受
//   - 文本 "2024/7/3"        -> "2024/7/3"   → normalizeDate 接受
func parseExcelDateCell(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	// 字符串日期（统一走 normalizeDate，支持 m/d/yy / mm/dd/yyyy / yyyy-mm-dd / yyyy/m/d 等）
	return normalizeDate(s)
}

func normalizeDate(s string) (string, bool) {
	// 2026-07-06 改：吴建棉反馈"日期对不上"
	// 增加 Excel/WPS 格式化输出的 layout：
	//   "07-15-24"     → NumFmt=14 (m/d/yy)
	//   "7/15/26"      → NumFmt=m/d/yy 自定义
	//   "07/15/2026"   → 一些 Excel 版本
	//   "7-15-2026"    → 自定义
	//   "2026年7月3日" → 中文版 Excel/WPS（罕见）
	// 2 位年按 2000 年后处理（与 Excel 行为一致）
	layouts := []string{
		// 标准
		"2006-01-02", "2006/01/02", "2006.01.02",
		"2006-1-2", "2006/1/2", "2006.1.2",
		"2006-1-02", "2006/1/02",
		"2006-01-2", "2006/01/2",
		// 美式短格式
		"1-2-06", "1/2/06", "1.2.06", // 1/3/24 = 2024-01-03
		"01-02-06", "01/02/06", "01.02.06",
		"1-02-06", "1/02/06", "1.02.06",
		"01-2-06", "01/2/06", "01.2.06",
		// 美式 4 位年
		"1/2/2006", "1-2-2006", "1.2.2006",
		"01/02/2006", "01-02-2006", "01.02.2006",
		"1/02/2006", "1-02-2006", "1.02.2006",
		"01/2/2006", "01-2-2006", "01.2.2006",
		// 中文
		"2006年1月2日", "2006年01月02日",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			// 2 位年按 2000 年后（与 Excel 行为一致：sn=1~59 默认归 1900，但 sn>=60 归 1900/2000+）
			// 我们的格式是 m/d/yy，2 位年 < 30 归 2000+，>= 30 归 1900+（与 Excel 1900 闰年 bug 兼容）
			year := t.Year()
			if year < 100 {
				if year < 30 {
					year += 2000
				} else {
					year += 1900
				}
				t = time.Date(year, t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
			}
			return t.Format("2006-01-02"), true
		}
	}
	return "", false
}
