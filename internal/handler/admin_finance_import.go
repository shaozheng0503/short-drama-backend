package handler

import (
	"bytes"
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

// adminDownloadIncomeTemplate —— GET /v1/admin/finance/income/template.xlsx
// 生成「短剧名称 + 渠道 + 收益 + 日期」四列收益导入模板。
func (s *Server) adminDownloadIncomeTemplate(c *gin.Context) {
	xl := excelize.NewFile()
	defer xl.Close()

	sheet := "Sheet1"
	headers := []string{"短剧名称", "渠道", "收益金额(元)", "日期(YYYY-MM-DD)"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = xl.SetCellValue(sheet, cell, h)
	}
	samples := [][]interface{}{
		{"总裁的逆袭新娘", "抖音", 123.45, "2026-05-26"},
		{"总裁的逆袭新娘", "快手", 88.00, "2026-05-27"},
	}
	for r, row := range samples {
		for col, v := range row {
			cell, _ := excelize.CoordinatesToCellName(col+1, r+2)
			_ = xl.SetCellValue(sheet, cell, v)
		}
	}
	_ = xl.SetColWidth(sheet, "A", "A", 28)
	_ = xl.SetColWidth(sheet, "B", "D", 20)

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
//	A 列：短剧名称   B 列：渠道(抖音/快手/腾讯/B站/视频号…)   C 列：收益金额(元)   D 列：日期(YYYY-MM-DD)
//
// 按短剧名称匹配剧目（名称不唯一的行会跳过并报错，建议保证标题唯一或后续改用 ID）。
// 幂等：同一 (剧目, 渠道, 日期) 重复导入按「覆盖」处理——以本次值为准，按差额调整创作者账面。
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
		title       string
		channel     string
		incomeCents int64
		statDate    string
	}
	var parsed []parsedRow
	rowErrors := make([]string, 0)

	for i := 1; i < len(rows); i++ { // 跳过表头
		row := rows[i]
		lineNo := i + 1
		if len(row) < 4 {
			rowErrors = append(rowErrors, fmt.Sprintf("第%d行：列数不足（需 短剧名称/渠道/收益/日期）", lineNo))
			continue
		}
		title := strings.TrimSpace(row[0])
		channel := strings.TrimSpace(row[1])
		if title == "" || channel == "" {
			rowErrors = append(rowErrors, fmt.Sprintf("第%d行：短剧名称/渠道不能为空", lineNo))
			continue
		}
		yuan, eInc := strconv.ParseFloat(strings.TrimSpace(row[2]), 64)
		if eInc != nil || yuan < 0 {
			rowErrors = append(rowErrors, fmt.Sprintf("第%d行：收益金额不合法", lineNo))
			continue
		}
		statDate, ok := normalizeDate(strings.TrimSpace(row[3]))
		if !ok {
			rowErrors = append(rowErrors, fmt.Sprintf("第%d行：日期格式应为 YYYY-MM-DD", lineNo))
			continue
		}
		parsed = append(parsed, parsedRow{
			title:       title,
			channel:     channel,
			incomeCents: int64(math.Round(yuan * 100)),
			statDate:    statDate,
		})
	}

	imported := 0
	var totalDelta int64
	err = s.db.Transaction(func(tx *gorm.DB) error {
		for _, pr := range parsed {
			// 按名称匹配剧目：要求唯一。
			var dramas []model.Drama
			if err := tx.Select("id", "creator_id").Where("title = ?", pr.title).Find(&dramas).Error; err != nil {
				return err
			}
			if len(dramas) == 0 {
				rowErrors = append(rowErrors, fmt.Sprintf("短剧《%s》不存在，已跳过", pr.title))
				continue
			}
			if len(dramas) > 1 {
				rowErrors = append(rowErrors, fmt.Sprintf("短剧《%s》名称不唯一(%d 部)，已跳过", pr.title, len(dramas)))
				continue
			}
			drama := dramas[0]
			if drama.CreatorID == nil {
				rowErrors = append(rowErrors, fmt.Sprintf("短剧《%s》未绑定创作者，已跳过", pr.title))
				continue
			}
			creatorID := *drama.CreatorID

			// 渠道明细：覆盖语义，算差额。
			var existing model.ChannelIncomeDaily
			errFind := tx.Where("drama_id = ? AND channel = ? AND stat_date = ?",
				drama.ID, pr.channel, pr.statDate).First(&existing).Error
			var delta int64
			if errFind == nil {
				delta = pr.incomeCents - existing.IncomeCents
				if err := tx.Model(&model.ChannelIncomeDaily{}).Where("id = ?", existing.ID).
					Updates(map[string]interface{}{"income_cents": pr.incomeCents, "creator_id": creatorID}).Error; err != nil {
					return err
				}
			} else if isNotFound(errFind) {
				delta = pr.incomeCents
				if err := tx.Create(&model.ChannelIncomeDaily{
					DramaID: drama.ID, Channel: pr.channel, StatDate: pr.statDate,
					CreatorID: creatorID, IncomeCents: pr.incomeCents,
				}).Error; err != nil {
					return err
				}
			} else {
				return errFind
			}

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
			imported++
		}
		return nil
	})
	if err != nil {
		response.ServerError(c, "导入失败，已回滚")
		return
	}

	response.OK(c, gin.H{
		"imported_rows":      imported,
		"failed_rows":        len(rowErrors),
		"income_delta_cents": totalDelta,
		"errors":             rowErrors,
	})
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

// normalizeDate 把 YYYY-MM-DD / YYYY/MM/DD / YYYY.MM.DD 归一成 YYYY-MM-DD。
func normalizeDate(s string) (string, bool) {
	for _, layout := range []string{"2006-01-02", "2006/01/02", "2006.01.02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02"), true
		}
	}
	return "", false
}
