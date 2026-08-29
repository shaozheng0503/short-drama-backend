package handler

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
	Category        string `json:"category,omitempty"` // 收入 / 押金
	GrossCents      int64  `json:"gross_cents"`        // 总收益（押金行为押金金额）
	ShareRatioBP    int    `json:"share_ratio_bp"`     // 实际采用的分成比例（基点）
	IncomeCents     int64  `json:"income_cents"`       // 创作者实得 = 总收益×比例
	Status          string `json:"status"`             // created / updated / unchanged / duplicate / failed
	Message         string `json:"message"`
	ExistingCents   *int64 `json:"existing_cents,omitempty"` // 旧的创作者实得
	DeltaCents      int64  `json:"delta_cents"`
	DramaID         uint64 `json:"drama_id,omitempty"`
	CreatorID       uint64 `json:"creator_id,omitempty"`
	DuplicateOfRow  int    `json:"duplicate_of_row,omitempty"`
	ChannelIncomeID uint64 `json:"channel_income_id,omitempty"`
	// 发行商收益信息（自动生成）
	DistributorID      uint64 `json:"distributor_id,omitempty"`
	DistributorName    string `json:"distributor_name,omitempty"`
	DistributorIncome  int64  `json:"distributor_income_cents,omitempty"` // 发行商实得
	DistributorStatus  string `json:"distributor_status,omitempty"`       // created / skipped / failed
	DistributorMessage string `json:"distributor_message,omitempty"`
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
// 生成单 Sheet 收益导入模板，9 列（2026-08-29 会议确认：D/E 列改直接填金额，不再填比例）：
//   - 短剧名称 | 渠道 | 总收益 | 创作者分成金额 | 发行商分成金额 | 日期 | 短剧ID | 类目 | 发行商
//
// 创作者分成金额(D 列)：直接填元（如 37.04）；留空则按该渠道的全局配置比例折算，都没有时按 30% 入账。
// 发行商分成金额(E 列)：直接填元（如 67.9）；留空按总收益×55% 折算；视频号固定不生成分成。
// 口径参考（2026-08-29 会议确认）：发行商实得 = 总收益×55%，创作者 = 总收益×30%，平台 = 总收益×15%。
// 旧模板兼容：D/E 列表头含「比例」的旧文件仍按比例（50/50%/0.5）解析。
// 类目(H 列)：收入（默认，留空=收入）/ 押金。类目=押金时只读 C 列金额 + I 列发行商，不进任何收入表。
// 发行商(I 列)：类目=押金时必填（发行商名称或手机号，手机号唯一定位）。
// 短剧ID(G 列)用于解决名称重复时的歧义；不填则按名称匹配，名称唯一才能定位。
func (s *Server) adminDownloadIncomeTemplate(c *gin.Context) {
	xl := excelize.NewFile()
	defer xl.Close()

	sheet := "收益导入模板"
	xl.SetSheetName(xl.GetSheetName(0), sheet)
	headers := []string{
		"短剧名称", "渠道", "总收益",
		"创作者分成金额(元,直接填金额,留空按30%)",
		"发行商分成金额(元,直接填金额,留空按55%;视频号固定0)",
		"日期", "短剧ID(选填,名称重复时必填)",
		"类目(收入/押金,留空=收入)", "发行商(类目=押金时必填:名称或手机号)",
	}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = xl.SetCellValue(sheet, cell, h)
	}
	// 金额示例：123.45×30%≈37.04（创作者），123.45×55%≈67.9（发行商）
	samples := [][]interface{}{
		{"总裁的逆袭新娘", "抖音", 123.45, 37.04, 67.9, "2026-05-26", "", "", ""},
		{"总裁的逆袭新娘", "快手", 88.00, "", "", "2026-05-27", 42, "", ""},
		{"总裁的逆袭新娘", "视频号", 66.00, 19.8, "", "2026-05-28", "", "", ""},
		{"", "", 400.00, "", "", "", "", "押金", "泉州市连姑娘商贸有限责任公司"},
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
	_ = xl.SetColWidth(sheet, "E", "E", 34)
	_ = xl.SetColWidth(sheet, "F", "F", 16)
	_ = xl.SetColWidth(sheet, "G", "G", 30)
	_ = xl.SetColWidth(sheet, "H", "H", 24)
	_ = xl.SetColWidth(sheet, "I", "I", 34)

	var buf bytes.Buffer
	if err := xl.Write(&buf); err != nil {
		response.ServerError(c, "生成收益导入模板失败")
		return
	}
	filename := "收益导入模板.xlsx"
	escaped := url.QueryEscape(filename)
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"income-template.xlsx\"; filename*=UTF-8''%s", escaped))
	c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
	c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", buf.Bytes())
}

// adminImportDailyIncome —— POST /v1/admin/finance/income/import
// 财务上传 xlsx，导入**第三方渠道**每日收益。本平台自有付费收入走支付分账，无需导入。
//
// 表格列（第 1 行表头，从第 2 行起读）：
//
//	A 列：短剧名称   B 列：渠道(抖音/快手/腾讯/B站/视频号…)   C 列：总收益
//	D 列：创作者分成比例(50 / 50% / 0.5，留空按渠道全局配置，都没有时按30%)
//	E 列：发行商分成比例(55 / 55% / 0.5，填了含0%按该值，留空按55%；视频号固定不生成分成)
//	F 列日期  G 列短剧ID(选填)  H 列类目(收入/押金,留空=收入)  I 列发行商(类目=押金时必填)
//	旧模板兼容：9列(H类目) / 7列(E发行商比例) / 6列(E日期) 均可读。
//
// 类目=押金的行：只读 C 列金额 + I 列发行商（名称或手机号），
// 给发行商可用押金充值并写押金流水；不进 channel_income_daily / creator_stats_daily /
// distributor_income_daily，不进创作者/发行商收益余额，不触发结算补录，不产生未结清。
//
// 分成计算：创作者实得 = round(总收益 × 比例)。比例优先取行内 D 列；D 列留空则取
//
//	该渠道的全局配置(income.share_ratio.<渠道> → income.share_ratio.default)；
//	都没配置则回落 30% 并在该行给出 warning（2026-08-29 会议确认）。
//
// 剧目匹配：短剧ID 列优先 —— 填了就按 ID 直接定位（仍校验名称一致性，不一致只给 warning）；
// 没填则按 A 列名称匹配，名称在库内不唯一时该行 fail 并提示填短剧ID。
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

	// 2026-08-29 幂等改造：计算上传文件内容 SHA-256，作为跨批次幂等键。
	// 背景：batchNo 每次上传随机生成，同一份报表重传会拿到不同 batchNo，
	// 押金类目行会重复入账。文件内容相同 → 同一 fileHash → 第二次上传押金行全部判 duplicate。
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		response.ServerError(c, "读取上传文件失败")
		return
	}
	fileHash := hex.EncodeToString(hasher.Sum(nil))
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		response.ServerError(c, "读取上传文件失败")
		return
	}

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
		rowNo             int
		title             string
		channel           string
		category          string // 收入 / 押金（默认收入）
		distHint          string // 类目=押金时的发行商定位串（名称或手机号）
		grossCents        int64
		ratioBP           int   // 实际采用的创作者分成比例（基点）；行内填金额时由金额反推
		rowHasRatio       bool  // D 列是否填了值（金额或比例）
		dColIsAmount      bool  // D 列填的是金额（新模板）还是比例（旧模板）
		incomeAmountCents int64 // D 列填的金额（分）；dColIsAmount=true 时有效
		distRatioBP       int   // E 列发行商比例（基点）；hasDistRatio=false 时按 5500 默认
		hasDistRatio      bool  // E 列是否填了值
		statDate          string
		explicitDramaID   uint64 // 短剧ID 列填了才非 0
	}
	var parsed []parsedRow
	rowReports := make([]incomeImportRowReport, 0)
	seenRows := map[string]int{}

	// 检测模板格式：
	//   9 列（H=类目，I=发行商，2026-08-28 版） / 7 列（E=发行商分成） / 6 列旧模板（E=日期）
	// dateCol/idCol：日期列与短剧ID列的位置；catCol/distCol：类目列与发行商列，-1 表示无该列
	// hasDistCol：E 列是否为发行商分成列（金额或比例，决定是否读取行内值）
	// distColIsAmount：E 列是金额（2026-08-29 新模板）还是比例（旧模板）
	dateCol, idCol := 4, 5 // 默认旧模板
	hasDistCol := false
	distColIsAmount := false
	if len(rows) > 0 && len(rows[0]) >= 5 && strings.Contains(rows[0][4], "发行商") {
		dateCol, idCol = 5, 6 // 7 列模板
		hasDistCol = true
		// 2026-08-29 会议确认：表头含「金额」→ 新模板直接填元；含「比例」→ 旧模板按比例解析
		distColIsAmount = strings.Contains(rows[0][4], "金额")
	}
	catCol, distCol := -1, -1
	if len(rows) > 0 && len(rows[0]) >= 8 && strings.Contains(rows[0][7], "类目") {
		catCol, distCol = 7, 8 // 9 列模板
	}

	for i := 1; i < len(rows); i++ { // 跳过表头
		row := rows[i]
		lineNo := i + 1
		// 完全空行（包括所有 cell 为空白）silently skip，避免财务夹空行报一堆 failed
		if rowIsBlank(row) {
			continue
		}
		if len(row) < dateCol+1 {
			rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Status: "failed", Message: "列数不足（需 短剧名称/渠道/总收益/创作者分成比例/发行商分成比例/日期，G 列短剧ID 选填；比例可留空）"})
			continue
		}
		// H 列类目：留空 / "收入" / 其他 → 收入行；"押金" → 押金行
		category := "收入"
		if catCol >= 0 && len(row) > catCol {
			if v := strings.TrimSpace(row[catCol]); v != "" {
				category = v
			}
		}
		isDeposit := category == "押金" || strings.EqualFold(category, "deposit")

		// C 列金额（收入行=总收益，押金行=押金金额）
		yuanRaw := strings.ReplaceAll(strings.TrimSpace(row[2]), ",", "") // 容忍千分位
		yuan, eInc := strconv.ParseFloat(yuanRaw, 64)
		if eInc != nil || yuan < 0 {
			rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Category: category, Status: "failed", Message: "金额不合法"})
			continue
		}
		grossCents := int64(math.Round(yuan * 100))

		// ---- 押金行：只读 C 列金额 + I 列发行商，其余列全部忽略 ----
		if isDeposit {
			if distCol < 0 {
				rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Category: category, GrossCents: grossCents, Status: "failed", Message: "模板无「类目」列（请下载最新模板）"})
				continue
			}
			distHint := ""
			if len(row) > distCol {
				distHint = strings.TrimSpace(row[distCol])
			}
			if distHint == "" {
				rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Category: category, GrossCents: grossCents, Status: "failed", Message: "类目=押金时「发行商」列必填（名称或手机号）"})
				continue
			}
			if grossCents <= 0 {
				rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Category: category, GrossCents: grossCents, Status: "failed", Message: "押金金额必须大于 0"})
				continue
			}
			key := "押金\x00" + distHint + "\x00" + yuanRaw
			if firstRow, exists := seenRows[key]; exists {
				rowReports = append(rowReports, incomeImportRowReport{
					RowNo: lineNo, Category: category, GrossCents: grossCents,
					Status: "duplicate", Message: fmt.Sprintf("同一文件内重复押金行，已跳过；首次出现在第%d行", firstRow),
					DuplicateOfRow: firstRow,
				})
				continue
			}
			seenRows[key] = lineNo
			parsed = append(parsed, parsedRow{
				rowNo: lineNo, category: category, distHint: distHint, grossCents: grossCents,
			})
			continue
		}
		if category != "收入" && category != "income" && category != "Income" {
			rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Category: category, Status: "failed", Message: fmt.Sprintf("类目不合法（%q，仅支持 收入/押金）", category)})
			continue
		}

		title := strings.TrimSpace(row[0])
		channel := strings.TrimSpace(row[1])
		if title == "" || channel == "" {
			rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Title: title, Channel: channel, Category: category, Status: "failed", Message: "短剧名称/渠道不能为空"})
			continue
		}
		if runeLen(channel) > 32 {
			rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Title: title, Channel: channel, Category: category, Status: "failed", Message: "渠道字段过长（最多 32 个字符）"})
			continue
		}
		// D 列：新模板（表头含「金额」）直接填创作者分成金额（元）；旧模板按比例解析。
		// 表头格式检测：首个非空数据行的 E 列已判定模板版本；D/E 列同版本。
		dColIsAmount := hasDistCol && distColIsAmount
		ratioBP, rowHasRatio := 0, false
		incomeAmountCents := int64(0)
		dCell := ""
		if len(row) > 3 {
			dCell = strings.TrimSpace(row[3])
		}
		if dColIsAmount {
			// 金额模式：允许留空（回落渠道配置）；填了必须是合法金额
			if dCell != "" {
				v, e := strconv.ParseFloat(strings.ReplaceAll(dCell, ",", ""), 64)
				if e != nil || v < 0 {
					rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Title: title, Channel: channel, Category: category, Status: "failed", Message: "创作者分成金额不合法（填元，如 37.04；留空按配置）"})
					continue
				}
				incomeAmountCents = int64(math.Round(v * 100))
				rowHasRatio = true
			}
		} else {
			var ratioErr string
			ratioBP, rowHasRatio, ratioErr = parseShareRatioBP(dCell)
			if ratioErr != "" {
				rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Title: title, Channel: channel, Category: category, Status: "failed", Message: ratioErr})
				continue
			}
		}
		// E 列发行商分成：新模板填金额（元）→ 反推 BP；旧模板填比例；留空按 55%。仅 7/9 列模板有此列。
	distRatioBP, hasDistRatio := model.DefaultDistributorShareBP, false
		if hasDistCol && len(row) > 4 {
			eCell := strings.TrimSpace(row[4])
			if distColIsAmount {
				if eCell != "" {
					v, e := strconv.ParseFloat(strings.ReplaceAll(eCell, ",", ""), 64)
					if e != nil || v < 0 {
						rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Title: title, Channel: channel, Category: category, Status: "failed", Message: "发行商分成金额不合法（填元，如 67.9；留空按 55%）"})
						continue
					}
					amountCts := int64(math.Round(v * 100))
					// 金额反推 BP（向上取整保底）：bp = round(amount/gross*10000)；
					// gross=0 或异常时直接 failed，避免除零
					if grossCents <= 0 {
						rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Title: title, Channel: channel, Category: category, Status: "failed", Message: "总收益为 0，无法按发行商分成金额折算比例"})
						continue
					}
					distRatioBP = int(math.Round(float64(amountCts) / float64(grossCents) * float64(model.ShareRatioBPFull)))
					if distRatioBP > model.ShareRatioBPFull {
						distRatioBP = model.ShareRatioBPFull
					}
					hasDistRatio = true
				}
			} else {
				if bp, has, e := parseShareRatioBP(eCell); e != "" {
					rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Title: title, Channel: channel, Category: category, Status: "failed", Message: e})
					continue
				} else if has {
					distRatioBP, hasDistRatio = bp, true
				}
			}
		}
		// 2026-07-06 改：先按 Excel 序列号解析（RawCellValue 模式下日期类型是 float 字符串），
		// 解析失败再走字符串日期解析（兼容财务手打 "2024/7/3" 的情况）
		statDate, ok := parseExcelDateCell(strings.TrimSpace(row[dateCol]))
		if !ok {
			rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Title: title, Channel: channel, Category: category, Status: "failed", Message: "日期格式应为 YYYY-MM-DD 或 Excel 日期单元格"})
			continue
		}
		var explicitDramaID uint64
		if len(row) >= idCol+1 {
			if idStr := strings.TrimSpace(row[idCol]); idStr != "" {
				// Excel 中数字单元格读出来可能是 "42" 或 "42.0"，TrimRight 一下小数点尾巴
				if dot := strings.IndexByte(idStr, '.'); dot >= 0 {
					tail := strings.TrimRight(idStr[dot+1:], "0")
					if tail == "" {
						idStr = idStr[:dot]
					}
				}
				v, eID := strconv.ParseUint(idStr, 10, 64)
				if eID != nil || v == 0 {
					rowReports = append(rowReports, incomeImportRowReport{RowNo: lineNo, Title: title, Channel: channel, Category: category, StatDate: statDate, Status: "failed", Message: "短剧ID 不合法"})
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
				Category:       category,
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
			rowNo:             lineNo,
			title:             title,
			channel:           channel,
			category:          category,
			grossCents:        grossCents,
			ratioBP:           ratioBP,
			rowHasRatio:       rowHasRatio,
			dColIsAmount:      dColIsAmount,
			incomeAmountCents: incomeAmountCents,
			distRatioBP:       distRatioBP,
			hasDistRatio:      hasDistRatio,
			statDate:          statDate,
			explicitDramaID:   explicitDramaID,
		})
	}

	batchNo := generateIncomeImportBatchNo()

	// 2026-08-29 幂等护栏：同一文件（内容哈希相同）之前成功导入过 → 整批拒绝，防止财务误重传
	// 导致收入/押金重复入账。dry_run 试算不受限。极端情况两份内容完全相同的报表确需再导时，
	// 管理员改动文件（如调整日期列）即可绕开，不会卡死正常业务。
	if !dryRun {
		var prev model.ChannelIncomeImportBatch
		if err := s.db.Where("file_hash = ?", fileHash).Order("id DESC").First(&prev).Error; err == nil {
			var prevReports []incomeImportRowReport
			_ = json.Unmarshal([]byte(prev.RowReportsJSON), &prevReports)
			response.OK(c, gin.H{
				"batch_no":           prev.BatchNo,
				"duplicate_of_batch": prev.BatchNo,
				"note":               fmt.Sprintf("该文件已于 %s 导入过（批次 %s），本次拒绝重复导入，结果为上批数据；如确需重新导入请修改文件内容后上传", prev.CreatedAt.Format("2006-01-02 15:04"), prev.BatchNo),
				"processed_rows":     prev.ProcessedRows,
				"created_rows":       prev.CreatedRows,
				"updated_rows":       prev.UpdatedRows,
				"unchanged_rows":     prev.UnchangedRows,
				"duplicate_rows":     prev.DuplicateRows,
				"failed_rows":        prev.FailedRows,
				"deposit_rows":       prev.DepositRows,
				"deposit_cents":      prev.DepositCents,
				"income_delta_cents": prev.IncomeDeltaCents,
				"row_reports":        prevReports,
			})
			return
		}
	}

	createdRows := 0
	updatedRows := 0
	unchangedRows := 0
	duplicateRows := 0
	failedRows := 0
	depositRows := 0
	var depositCents int64
	var totalDelta int64
	var supplementedSettlements, blockedSettlements int
	err = s.db.Transaction(func(tx *gorm.DB) error {
		for _, pr := range parsed {
			// ---- 押金行：给发行商可用押金充值，不进任何收入表 ----
			if pr.category == "押金" {
				distID, amountCents, status, msg := s.importDepositRow(tx, pr.distHint, pr.grossCents, fileHash, batchNo, pr.rowNo, dryRun)
				report := incomeImportRowReport{
					RowNo: pr.rowNo, Category: "押金", GrossCents: pr.grossCents,
					Status: status, Message: msg,
					DistributorID: distID,
				}
				if distID > 0 {
					var dist model.Distributor
					tx.Select("id, name, org_name, nickname").Where("id = ?", distID).First(&dist)
					report.DistributorName = distributorName(&dist)
				}
				if status == "created" {
					depositRows++
					depositCents += amountCents
				}
				rowReports = append(rowReports, report)
				continue
			}

			// 解析创作者分成：金额模式（新模板）直接用 D 列金额、BP 由金额/gross 反推（仅供报表展示）；
			// 比例模式（旧模板）：行内优先；行内留空则查渠道配置；都没有回落 100% 并 warning。
			ratioBP := pr.ratioBP
			incomeCents := int64(0)
			var ratioWarn string
			if pr.dColIsAmount {
				if pr.rowHasRatio {
					incomeCents = pr.incomeAmountCents
					if pr.grossCents > 0 {
						// 反推 BP 仅用于报表展示，clamp 到 100%（金额>总收益时不虚标比例）
						ratioBP = int(math.Round(float64(pr.incomeAmountCents) / float64(pr.grossCents) * float64(model.ShareRatioBPFull)))
						if ratioBP > model.ShareRatioBPFull {
							ratioBP = model.ShareRatioBPFull
						}
					}
				} else {
					// D 列留空：回落渠道配置；都没有时按 30% 入账（2026-08-29 会议：创作者=总收益×30%）
					if cfgBP, ok := s.channelShareRatioBP(pr.channel); ok {
						ratioBP = cfgBP
						incomeCents = pr.grossCents * int64(ratioBP) / int64(model.ShareRatioBPFull)
					} else {
						ratioBP = model.DefaultCreatorShareBP
						incomeCents = pr.grossCents * int64(ratioBP) / int64(model.ShareRatioBPFull)
						ratioWarn = "未填金额且渠道未配置分成比例，按 30% 入账"
					}
				}
			} else {
				if !pr.rowHasRatio {
					if cfgBP, ok := s.channelShareRatioBP(pr.channel); ok {
						ratioBP = cfgBP
					} else {
						ratioBP = model.DefaultCreatorShareBP
						ratioWarn = "未填比例且渠道未配置分成比例，按 30% 入账"
					}
				}
				// 2026-08-29 修复（中-1）：统一走整数 BP 运算（与 model.IncomeFromGrossBP 同式）
				incomeCents = pr.grossCents * int64(ratioBP) / int64(model.ShareRatioBPFull)
			}
			report := incomeImportRowReport{
				RowNo:        pr.rowNo,
				Title:        pr.title,
				Channel:      pr.channel,
				Category:     pr.category,
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

			// 剧目匹配：dramaID 优先 → 否则按名称且要求唯一。
			var drama model.Drama
			if pr.explicitDramaID != 0 {
				if err := tx.Select("id", "title", "creator_id").
					Where("id = ?", pr.explicitDramaID).First(&drama).Error; err != nil {
					if isNotFound(err) {
						report.Status = "failed"
						report.Message = fmt.Sprintf("短剧ID=%d 不存在，已跳过", pr.explicitDramaID)
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
					report.Message = fmt.Sprintf("短剧名称不唯一（匹配到 %d 部，ID=[%s]），请填写「短剧ID」列定位", len(dramas), strings.Join(ids, ","))
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
			// 2026-08-28 会议确认：视频号 = 平台自发渠道，发行商一律不参与分成（数值记 0）
			platformKey := channelToPlatform(pr.channel)
			if platformKey == model.PlatformWechatVideo && report.Status != "failed" {
				report.DistributorStatus = "skipped"
				report.DistributorMessage = "视频号为平台自发渠道，发行商分成记为 0"
			} else if platformKey != "" && report.Status != "failed" {
				// E 列发行商分成比例：填了（含 0%）按该值；留空按默认 55%（2026-08-29 会议确认）
			distShareBP := model.DefaultDistributorShareBP
			if pr.hasDistRatio {
				distShareBP = pr.distRatioBP
			}
				distID, distInc, distStatus, distMsg := s.generateDistributorIncome(tx, drama.ID, platformKey, pr.statDate, pr.grossCents, int64(distShareBP), batchNo, pr.rowNo, dryRun)
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

		// ---- 2026-08-12 收入补录：重算受影响的 open 状态结算单 ----
		if !dryRun {
			statDateSet := map[string]struct{}{}
			for _, pr := range parsed {
				if pr.statDate != "" {
					statDateSet[pr.statDate] = struct{}{}
				}
			}
			statDates := make([]string, 0, len(statDateSet))
			for ds := range statDateSet {
				statDates = append(statDates, ds)
			}
			sup, blk, err := s.recalcOpenSettlementsForDateRange(tx, statDates)
			if err != nil {
				return err
			}
			supplementedSettlements = sup
			blockedSettlements = blk
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
		"batch_no":                 batchNo,
		"dry_run":                  dryRun,
		"processed_rows":           len(rowReports),
		"imported_rows":            createdRows + updatedRows,
		"created_rows":             createdRows,
		"updated_rows":             updatedRows,
		"unchanged_rows":           unchangedRows,
		"duplicate_rows":           duplicateRows,
		"failed_rows":              failedRows,
		"deposit_rows":             depositRows,
		"deposit_cents":            depositCents,
		"income_delta_cents":       totalDelta,
		"row_reports":              rowReports,
		"errors":                   incomeImportErrors(rowReports),
		"supplemented_settlements": supplementedSettlements,
		"blocked_settlements":      blockedSettlements,
	}
	// dry_run 只试算不落库，也不记录批次。
	if dryRun {
		result["batch_no"] = ""
		result["note"] = "试算结果（dry_run），未写库、未入账、未记录批次。去掉 dry_run 参数即可正式导入。"
		response.OK(c, result)
		return
	}
	if err := s.saveIncomeImportBatch(c, batchNo, fileHash, fileHeader.Filename, result, rowReports); err != nil {
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
// shareBP 为该行采用的发行商分成比例（基点，E 列填了按 E 列，留空默认 5500=55%）。
// 返回 (发行商ID, 发行商实得分, 状态, 消息)。状态：created=新建 / skipped=无人认领或已存在 / failed=出错。
func (s *Server) generateDistributorIncome(tx *gorm.DB, dramaID uint64, platform, statDate string, grossCents int64, shareBP int64, batchNo string, rowNo int, dryRun bool) (uint64, int64, string, string) {
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

	if shareBP < 0 {
		shareBP = 0
	}
	if shareBP > model.ShareRatioBPFull {
		shareBP = model.ShareRatioBPFull
	}
	incomeCents := grossCents * shareBP / 10000

	if dryRun {
		return matchedDD.DistributorID, incomeCents, "created", fmt.Sprintf("试算：发行商实得 %.2f 元（%s）", float64(incomeCents)/100, shareRatioBpLabel(shareBP))
	}

	inc := model.DistributorIncomeDaily{
		DistributorID: matchedDD.DistributorID,
		DramaID:       dramaID,
		Platform:      platform,
		StatDate:      statDate,
		GrossCents:    grossCents,
		ShareRatioBP:  int(shareBP),
		IncomeCents:   incomeCents,
		BatchNo:       batchNo,
		ImportRowNo:   rowNo,
	}
	if err := tx.Create(&inc).Error; err != nil {
		return matchedDD.DistributorID, 0, "failed", fmt.Sprintf("创建发行商收益失败: %v", err)
	}

	// 累加发行商收益（0% 时 incomeCents=0，余额不动）
	if incomeCents != 0 {
		if err := tx.Model(&model.Distributor{}).
			Where("id = ?", matchedDD.DistributorID).
			UpdateColumns(map[string]interface{}{
				"total_income_cents": gorm.Expr("total_income_cents + ?", incomeCents),
				"balance_cents":      gorm.Expr("balance_cents + ?", incomeCents),
			}).Error; err != nil {
			return matchedDD.DistributorID, 0, "failed", fmt.Sprintf("更新发行商余额失败: %v", err)
		}
	}

	return matchedDD.DistributorID, incomeCents, "created", fmt.Sprintf("发行商实得 %.2f 元（%s）", float64(incomeCents)/100, shareRatioBpLabel(shareBP))
}

// shareRatioBpLabel 把基点比例转成可读文案，如 5500 → "55%"，0 → "0%"。
func shareRatioBpLabel(bp int64) string {
	return fmt.Sprintf("%g%%", float64(bp)/100)
}

// importDepositRow 处理类目=押金 的导入行：给发行商「可用押金」充值并写押金流水。
// 不进任何收入表、不进收益余额、不触发结算补录、不产生未结清。
// distHint 支持发行商手机号（精确）或名称（name/nickname/org_name 精确匹配）。
// 2026-08-29 幂等改造：RelatedBusinessNo 从随机 batchNo 改为 "IMPDEP:<fileHash>:<rowNo>"，
// 同文件重传时行级幂等键命中 → 状态 duplicate 跳过；配合批次级 file_hash 拦截，双保险。
// 返回 (发行商ID, 实际入账分, 状态, 消息)。
func (s *Server) importDepositRow(tx *gorm.DB, distHint string, amountCents int64, fileHash, batchNo string, rowNo int, dryRun bool) (uint64, int64, string, string) {
	var dists []model.Distributor
	if err := tx.Where("phone = ? OR name = ? OR nickname = ?", distHint, distHint, distHint).Find(&dists).Error; err != nil {
		return 0, 0, "failed", fmt.Sprintf("查询发行商失败: %v", err)
	}
	if len(dists) == 0 {
		if err := tx.Where("org_name = ?", distHint).Find(&dists).Error; err != nil {
			return 0, 0, "failed", fmt.Sprintf("查询发行商失败: %v", err)
		}
	}
	if len(dists) == 0 {
		return 0, 0, "failed", fmt.Sprintf("发行商不存在（%q），请确认名称或手机号", distHint)
	}
	if len(dists) > 1 {
		return 0, 0, "failed", fmt.Sprintf("「%s」匹配到 %d 个发行商，请填写手机号唯一定位", distHint, len(dists))
	}
	dist := dists[0]
	if dist.Status != model.StatusActive {
		return dist.ID, 0, "failed", fmt.Sprintf("发行商状态为 %s，非活跃无法入账", dist.Status)
	}

	if dryRun {
		return dist.ID, amountCents, "created", fmt.Sprintf("试算：押金入账 %.2f 元（不进收入、不产生分成、不产生未结清）", float64(amountCents)/100)
	}

	// 行级幂等：同一文件的同一行号此前已入账过 → 判 duplicate 跳过（防同文件重传/并发双击）。
	depositBizNo := fmt.Sprintf("IMPDEP:%s:%d", fileHash, rowNo)
	var existedTx model.DistributorDepositTransaction
	if err := tx.Where("related_type = ? AND related_business_no = ?", "import", depositBizNo).
		First(&existedTx).Error; err == nil {
		return dist.ID, 0, "duplicate", fmt.Sprintf("该押金行此前已入账（流水 #%d，%.2f 元），本次跳过", existedTx.ID, float64(existedTx.AmountCents)/100)
	}

	// 行锁发行商，计算变动后可用余额
	var locked model.Distributor
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, dist.ID).Error; err != nil {
		return dist.ID, 0, "failed", fmt.Sprintf("锁定发行商失败: %v", err)
	}
	balanceAfter := locked.DepositAvailableCents + amountCents
	if err := tx.Model(&locked).
		Update("deposit_available_cents", balanceAfter).Error; err != nil {
		return dist.ID, 0, "failed", fmt.Sprintf("更新可用押金失败: %v", err)
	}
	txn := model.DistributorDepositTransaction{
		DistributorID:     dist.ID,
		Type:              "recharge",
		AmountCents:       amountCents,
		BalanceAfterCents: balanceAfter,
		RelatedType:       "import",
		RelatedBusinessNo: depositBizNo,
		Remark:            fmt.Sprintf("收益导入-押金类目（第%d行，批次 %s）", rowNo, batchNo),
	}
	if err := tx.Create(&txn).Error; err != nil {
		return dist.ID, 0, "failed", fmt.Sprintf("写入押金流水失败: %v", err)
	}
	return dist.ID, amountCents, "created", fmt.Sprintf("押金入账 %.2f 元（不进收入、不产生分成、不产生未结清）", float64(amountCents)/100)
}

// fileHashOfBatchNo 已废弃并删除：幂等键统一走 fileHash 参数。

// normalizeDate 把常见日期写法归一成 YYYY-MM-DD。
// Excel 把日期格式化输出时会用本地短格式（如 2026/5/26 无前导零），全部兼容。
// parseExcelDateCell 解析"日期类型"或"日期字符串"单元格
// 2026-07-06 改：吴建棉反馈"收益报表上传，日期对不上"
//
// 背景：WPS 写日期按"Excel 算法"（假装 1900 闰年），
//
//	excelize v2.10.1 的 ExcelDateToTime 按"真实日历"算（1899-12-30 + N 天），
//	两者差几天。WPS 真实序列号映射：
//	  "2026/7/3" → sn=46204（Excel 算法）→ ExcelDateToTime 给 2026-07-01（差 2 天）
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
