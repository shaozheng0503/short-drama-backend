package handler

import (
	"bytes"
	"net/http/httptest"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
)

// TestWriteXLSXResponse 验证通用 XLSX 下载响应能正确生成文件
func TestWriteXLSXResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	xl := excelize.NewFile()
	sheet := "Sheet1"
	FillSheetHeaders(xl, sheet, []string{"手机号", "姓名"}, [][]interface{}{
		{"13800138001", "张三"},
	})
	_ = xl.SetColWidth(sheet, "A", "A", 20)

	WriteXLSXResponse(c, xl, "测试模板.xlsx")

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if ct != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Fatalf("unexpected content-type: %s", ct)
	}
	cd := w.Header().Get("Content-Disposition")
	if cd == "" {
		t.Fatal("missing Content-Disposition")
	}
	// 验证返回的 body 是有效的 xlsx
	xl2, err := excelize.OpenReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("response body is not valid xlsx: %v", err)
	}
	defer xl2.Close()
	rows, err := xl2.GetRows("Sheet1")
	if err != nil {
		t.Fatalf("GetRows failed: %v", err)
	}
	if len(rows) < 2 {
		t.Fatalf("expected at least 2 rows, got %d", len(rows))
	}
	if rows[0][0] != "手机号" || rows[0][1] != "姓名" {
		t.Fatalf("unexpected headers: %v", rows[0])
	}
	if rows[1][0] != "13800138001" || rows[1][1] != "张三" {
		t.Fatalf("unexpected sample row: %v", rows[1])
	}
}

// TestGenerateBatchNo 验证批次号生成
func TestGenerateBatchNo(t *testing.T) {
	bn := GenerateBatchNo("CRT")
	if len(bn) < 10 {
		t.Fatalf("batch no too short: %s", bn)
	}
	if bn[:3] != "CRT" {
		t.Fatalf("expected prefix CRT, got %s", bn[:3])
	}
	// 两次调用应不同
	bn2 := GenerateBatchNo("CRT")
	if bn == bn2 {
		t.Fatal("two calls should produce different batch numbers")
	}
}

// TestCellOr 验证安全读取单元格
func TestCellOr(t *testing.T) {
	row := []string{"a", "b", "c"}
	if cellOr(row, 0, "") != "a" {
		t.Fatal("expected 'a'")
	}
	if cellOr(row, 5, "default") != "default" {
		t.Fatal("expected default for out-of-bounds")
	}
	if cellOr(row, -1, "neg") != "neg" {
		t.Fatal("expected neg for negative index")
	}
}

// TestRowIsBlank 验证空行检测
func TestRowIsBlank(t *testing.T) {
	if !rowIsBlank([]string{"", " ", "  "}) {
		t.Fatal("expected blank for whitespace-only row")
	}
	if rowIsBlank([]string{"", "a"}) {
		t.Fatal("expected non-blank for row with content")
	}
	if !rowIsBlank([]string{}) {
		t.Fatal("expected blank for empty row")
	}
}
