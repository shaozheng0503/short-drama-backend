package handler

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
)

// ============================================================
// 通用 Excel 导入基础模块（2026-07-28）
// 供创作者/发行商批量导入、财务收益导入等场景复用。
// ============================================================

// 行状态常量（与现有收益导入五态对齐）
const (
	RowCreated   = "created"
	RowUpdated   = "updated"
	RowUnchanged = "unchanged"
	RowDuplicate = "duplicate"
	RowFailed    = "failed"
)

// OpenUploadedXLSX 从 gin form-data "file" 字段读取并打开 xlsx。
// 返回 (File, rows, ok)；ok=false 时已自动 response 错误，调用方直接 return。
func OpenUploadedXLSX(c *gin.Context) (*excelize.File, [][]string, bool) {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		response.InvalidParam(c, "请在 form-data 的 file 字段上传 xlsx 文件")
		return nil, nil, false
	}
	if !strings.HasSuffix(fileHeader.Filename, ".xlsx") {
		response.InvalidParam(c, "文件必须是 .xlsx 格式")
		return nil, nil, false
	}
	f, err := fileHeader.Open()
	if err != nil {
		response.ServerError(c, "读取上传文件失败")
		return nil, nil, false
	}
	defer f.Close()
	xl, err := excelize.OpenReader(f)
	if err != nil {
		response.InvalidParam(c, "文件不是有效的 xlsx")
		return nil, nil, false
	}
	sheet := xl.GetSheetName(0)
	if sheet == "" {
		xl.Close()
		response.InvalidParam(c, "xlsx 没有可读的工作表")
		return nil, nil, false
	}
	rows, err := xl.GetRows(sheet)
	if err != nil {
		xl.Close()
		response.ServerError(c, "解析表格失败")
		return nil, nil, false
	}
	return xl, rows, true
}

// WriteXLSXResponse 把内存中的 xlsx 以附件下载方式返回。
func WriteXLSXResponse(c *gin.Context, xl *excelize.File, filename string) {
	defer xl.Close()
	var buf bytes.Buffer
	if err := xl.Write(&buf); err != nil {
		response.ServerError(c, "生成 Excel 失败")
		return
	}
	escaped := url.QueryEscape(filename)
	ct := "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	c.Header("Content-Type", ct)
	c.Header("Content-Disposition",
		fmt.Sprintf("attachment; filename=\"%s\"; filename*=UTF-8''%s",
			asciiSafeName(filename), escaped))
	c.Data(http.StatusOK, ct, buf.Bytes())
}

// FillSheetHeaders 将 headers 写入第 1 行，samples 写入后续行。
func FillSheetHeaders(xl *excelize.File, sheet string, headers []string, samples [][]interface{}) {
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = xl.SetCellValue(sheet, cell, h)
	}
	for r, row := range samples {
		for col, v := range row {
			cell, _ := excelize.CoordinatesToCellName(col+1, r+2)
			_ = xl.SetCellValue(sheet, cell, v)
		}
	}
}

// GenerateBatchNo 生成 前缀+时间+随机hex 的批次号。
func GenerateBatchNo(prefix string) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s%s", prefix, time.Now().Format("20060102150405"))
	}
	return fmt.Sprintf("%s%s%s", prefix, time.Now().Format("20060102150405"),
		strings.ToUpper(hex.EncodeToString(b[:])))
}

// cellOr 安全读取一行中指定列的值，越界返回默认值。
func cellOr(row []string, idx int, def string) string {
	if idx < 0 || idx >= len(row) {
		return def
	}
	return strings.TrimSpace(row[idx])
}

// asciiSafeName 取文件名的 ASCII 安全部分作为 Content-Disposition 的 filename。
func asciiSafeName(filename string) string {
	// 简单实现：非 ASCII 字符替换为下划线
	var b strings.Builder
	for _, r := range filename {
		if r < 128 {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
