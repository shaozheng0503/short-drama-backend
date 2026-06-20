package handler

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"

	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
)

// creatorDownloadCostConfigTemplate —— GET /v1/creator/cost-config/template.xlsx
// 「成本配置为用户提供模板文件下载」（2026-06-18 会议）：给创作者下发一份「短剧成本配置清单」Excel 模板，
// 创作者按项目填写制作成本后导出/盖章上传，回填到建剧表单的 cost_config_url（备案制作金额 production_cost_cents 取合计）。
//
// 模板只下发结构与示例，不写库；与「收益导入模板」（财务侧）是两份独立模板，互不影响。
func (s *Server) creatorDownloadCostConfigTemplate(c *gin.Context) {
	xl := excelize.NewFile()
	defer xl.Close()

	sheet := "成本配置清单"
	// excelize 默认有个 Sheet1，重命名为业务表名，避免多出空表。
	xl.SetSheetName("Sheet1", sheet)

	// 顶部说明行（合并单元格），告知填写口径与盖章要求。
	_ = xl.MergeCell(sheet, "A1", "D1")
	_ = xl.SetCellValue(sheet, "A1",
		"短剧成本配置清单模板：请按制作环节据实填写各项金额（单位：元），合计金额即备案制作金额；填写完成后请加盖公章并上传。")

	headers := []string{"序号", "成本项目", "金额（元）", "说明 / 备注"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 2)
		_ = xl.SetCellValue(sheet, cell, h)
	}

	// 示例行：覆盖 AI 短剧常见成本科目，创作者据实增删改。
	samples := [][]interface{}{
		{1, "剧本 / 编剧", 5000.00, "剧本创作、改编授权等"},
		{2, "AI 生成（视频 / 图像）", 8000.00, "AIGC 工具生成、算力 / 套餐费用"},
		{3, "配音 / 音效 / 配乐", 2000.00, "配音演员、音效素材、BGM 授权"},
		{4, "后期剪辑 / 特效 / 字幕", 3000.00, "剪辑、调色、特效、字幕"},
		{5, "封面 / 物料设计", 800.00, "封面图、宣传海报等"},
		{6, "其他", 0.00, "如有其它成本请补充说明"},
	}
	for r, row := range samples {
		for col, v := range row {
			cell, _ := excelize.CoordinatesToCellName(col+1, r+3)
			_ = xl.SetCellValue(sheet, cell, v)
		}
	}

	// 合计行：金额列用 SUM 公式，财务/创作者改示例数字后自动汇总。
	totalRow := len(samples) + 3
	_ = xl.SetCellValue(sheet, fmt.Sprintf("B%d", totalRow), "合计")
	_ = xl.SetCellFormula(sheet, fmt.Sprintf("C%d", totalRow), fmt.Sprintf("SUM(C3:C%d)", totalRow-1))

	_ = xl.SetColWidth(sheet, "A", "A", 8)
	_ = xl.SetColWidth(sheet, "B", "B", 28)
	_ = xl.SetColWidth(sheet, "C", "C", 16)
	_ = xl.SetColWidth(sheet, "D", "D", 40)

	var buf bytes.Buffer
	if err := xl.Write(&buf); err != nil {
		response.ServerError(c, "生成成本配置模板失败")
		return
	}
	filename := "短剧成本配置清单模板.xlsx"
	escaped := url.QueryEscape(filename)
	contentType := "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"cost-config-template.xlsx\"; filename*=UTF-8''%s", escaped))
	c.Data(http.StatusOK, contentType, buf.Bytes())
}
