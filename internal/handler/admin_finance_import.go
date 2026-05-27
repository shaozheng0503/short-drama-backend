package handler

import (
	"fmt"
	"math"
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

// adminImportDailyIncome —— POST /v1/admin/finance/income/import
// 财务上传 xlsx，按「剧目 + 日期」导入每日收入，写入 creator_stats_daily 并同步创作者余额。
//
// 表格列（第 1 行表头，从第 2 行起读）：
//
//	A 列：剧目ID（drama_id）
//	B 列：日期（YYYY-MM-DD 或 YYYY/MM/DD）
//	C 列：收入金额（元，支持小数）
//
// 幂等：同一 (创作者, 剧目, 日期) 重复导入按「覆盖」处理——以本次值为准，按差额调整创作者账面。
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
		dramaID     uint64
		statDate    string
		incomeCents int64
	}
	var parsed []parsedRow
	rowErrors := make([]string, 0)

	for i := 1; i < len(rows); i++ { // 跳过表头
		row := rows[i]
		lineNo := i + 1
		if len(row) < 3 {
			rowErrors = append(rowErrors, fmt.Sprintf("第%d行：列数不足（需 剧目ID/日期/收入）", lineNo))
			continue
		}
		dramaID, e1 := strconv.ParseUint(strings.TrimSpace(row[0]), 10, 64)
		if e1 != nil || dramaID == 0 {
			rowErrors = append(rowErrors, fmt.Sprintf("第%d行：剧目ID 不合法", lineNo))
			continue
		}
		statDate, ok := normalizeDate(strings.TrimSpace(row[1]))
		if !ok {
			rowErrors = append(rowErrors, fmt.Sprintf("第%d行：日期格式应为 YYYY-MM-DD", lineNo))
			continue
		}
		yuan, e3 := strconv.ParseFloat(strings.TrimSpace(row[2]), 64)
		if e3 != nil || yuan < 0 {
			rowErrors = append(rowErrors, fmt.Sprintf("第%d行：收入金额不合法", lineNo))
			continue
		}
		parsed = append(parsed, parsedRow{
			dramaID:     dramaID,
			statDate:    statDate,
			incomeCents: int64(math.Round(yuan * 100)),
		})
	}

	imported := 0
	var totalDelta int64
	err = s.db.Transaction(func(tx *gorm.DB) error {
		for _, pr := range parsed {
			var drama model.Drama
			if err := tx.Select("id", "creator_id").First(&drama, pr.dramaID).Error; err != nil {
				if isNotFound(err) {
					rowErrors = append(rowErrors, fmt.Sprintf("剧目 %d 不存在，已跳过", pr.dramaID))
					continue
				}
				return err
			}
			if drama.CreatorID == nil {
				rowErrors = append(rowErrors, fmt.Sprintf("剧目 %d 未绑定创作者，已跳过", pr.dramaID))
				continue
			}
			creatorID := *drama.CreatorID

			// 读现有当日记录，算差额（覆盖语义，保证可重复导入）。
			var existing model.CreatorStatsDaily
			errFind := tx.Where("creator_id = ? AND drama_id = ? AND stat_date = ?",
				creatorID, pr.dramaID, pr.statDate).First(&existing).Error
			var delta int64
			if errFind == nil {
				delta = pr.incomeCents - existing.IncomeCents
				if err := tx.Model(&model.CreatorStatsDaily{}).
					Where("id = ?", existing.ID).
					Update("income_cents", pr.incomeCents).Error; err != nil {
					return err
				}
			} else if isNotFound(errFind) {
				delta = pr.incomeCents
				if err := tx.Create(&model.CreatorStatsDaily{
					CreatorID:   creatorID,
					DramaID:     pr.dramaID,
					StatDate:    pr.statDate,
					IncomeCents: pr.incomeCents,
				}).Error; err != nil {
					return err
				}
			} else {
				return errFind
			}

			if delta != 0 {
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

// normalizeDate 把 YYYY-MM-DD / YYYY/MM/DD 归一成 YYYY-MM-DD。
func normalizeDate(s string) (string, bool) {
	for _, layout := range []string{"2006-01-02", "2006/01/02", "2006.01.02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02"), true
		}
	}
	return "", false
}
