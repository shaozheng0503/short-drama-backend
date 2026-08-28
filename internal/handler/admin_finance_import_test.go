package handler

import (
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

// TestParseExcelDateCell 验证吴建棉 7/4 反馈的"日期差几天"问题修复
// 2026-07-06 改：不用 RawCellValue + 序列号（excelize.ExcelDateToTime 跟 WPS 算法差几天）
//       改用默认 GetRows + normalizeDate 接受所有常见日期格式
func TestParseExcelDateCell(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		// 财务手打字符串（最常见）
		{"string 2024/7/3", "2024/7/3", "2024-07-03", true},
		{"string 2024-07-03", "2024-07-03", "2024-07-03", true},
		{"string 2024.7.3", "2024.7.3", "2024-07-03", true},
		{"string 2024/07/03", "2024/07/03", "2024-07-03", true},
		{"string 2026/7/3", "2026/7/3", "2026-07-03", true},

		// WPS 格式化输出（excelize 默认 GetRows 对日期类型单元格的输出）
		// NumFmt=14 (m/d/yy) -> "07-15-24"
		{"excel default 2024-07-15", "07-15-24", "2024-07-15", true},
		{"excel default 2026-07-03", "7-3-26", "2026-07-03", true},
		// 自定义 yyyy/m/d
		{"excel custom 2024/7/15", "2024/7/15", "2024-07-15", true},
		// 美式 4 位年
		{"US 4-digit 7/3/2026", "7/3/2026", "2026-07-03", true},

		// 2 位年（Go 默认 0-69 归 2000+，70-99 归 1900+；与 Excel 1900 闰年 bug 不完全一致，
		// 但生产数据 2024-2026 范围都能正确归到 2000+，不实际触发问题）
		{"2-digit year 24", "7/3/24", "2024-07-03", true},
		{"2-digit year 26", "7/3/26", "2026-07-03", true},
		{"2-digit year 30", "7/3/30", "2030-07-03", true}, // Go 2 位年默认行为
		{"2-digit year 99", "7/3/99", "1999-07-03", true}, // Go 70-99 归 1900+

		// 中文
		{"中文 2026年7月3日", "2026年7月3日", "2026-07-03", true},

		// 异常
		{"empty", "", "", false},
		{"random string", "abc", "", false},
		{"out of range high", "100000", "", false},
		{"out of range low", "0", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseExcelDateCell(c.in)
			if ok != c.ok {
				t.Errorf("parseExcelDateCell(%q) ok = %v, want %v", c.in, ok, c.ok)
				return
			}
			if ok && got != c.want {
				t.Errorf("parseExcelDateCell(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestParseExcelDateCell_E2E 端到端：模拟 WPS 真实保存的 xlsx
// 验证默认 GetRows 拿到的字符串能被 parseExcelDateCell 正确解析
func TestParseExcelDateCell_E2E(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()

	// 表头
	for i, h := range []string{"A", "B", "C", "D", "E_date", "F"} {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue("Sheet1", cell, h)
	}

	// Row 2: 财务手打字符串 "2024/7/3"（即使设了日期 NumFmt，excelize 仍返回字符串）
	f.SetCellValue("Sheet1", "E2", "2024/7/3")
	style, _ := f.NewStyle(&excelize.Style{NumFmt: 14}) // m/d/yy
	f.SetCellStyle("Sheet1", "E2", "E2", style)

	// Row 3: 真实 time.Time（excelize 存为日期序列号 + NumFmt=m/d/yy）
	// excelize 默认 GetRows 会按 NumFmt 格式化为 "07-15-24"
	t3, _ := time.Parse("2006-01-02", "2024-07-15")
	f.SetCellValue("Sheet1", "E3", t3)
	f.SetCellStyle("Sheet1", "E3", "E3", style)

	tmp := t.TempDir()
	path := tmp + "/test.xlsx"
	if err := f.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	xl, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer xl.Close()

	// 默认 GetRows（不加 RawCellValue）
	rows, err := xl.GetRows("Sheet1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 3 {
		t.Fatalf("expected >=3 rows, got %d", len(rows))
	}

	// Row 2 (index 1): 字符串 "2024/7/3" → "2024/7/3"
	t.Logf("row2 E = %q (手打字符串)", rows[1][4])
	got, ok := parseExcelDateCell(rows[1][4])
	if !ok || got != "2024-07-03" {
		t.Errorf("row2 E (string) parseExcelDateCell = (%q, %v), want (2024-07-03, true)", got, ok)
	}

	// Row 3 (index 2): time.Time → excelize 按 NumFmt 格式化为 "07-15-24"
	t.Logf("row3 E = %q (excelize 格式化)", rows[2][4])
	got, ok = parseExcelDateCell(rows[2][4])
	if !ok || got != "2024-07-15" {
		t.Errorf("row3 E (serial) parseExcelDateCell = (%q, %v), want (2024-07-15, true)", got, ok)
	}
}

// TestShareRatioBpLabel 验证基点比例转可读文案
func TestShareRatioBpLabel(t *testing.T) {
	cases := []struct {
		bp   int64
		want string
	}{
		{0, "0%"},
		{5500, "55%"},
		{5000, "50%"},
		{10000, "100%"},
		{3333, "33.33%"},
	}
	for _, c := range cases {
		if got := shareRatioBpLabel(c.bp); got != c.want {
			t.Errorf("shareRatioBpLabel(%d) = %q, want %q", c.bp, got, c.want)
		}
	}
}

// TestParseShareRatioBPZeroAndDefault 验证 E 列发行商比例的关键场景：
// 填 0 / 0% / 0.0 都应解析为 0 基点且"已填"（发行商分成记 0）；空串为"未填"（回落 55%）
func TestParseShareRatioBPZeroAndDefault(t *testing.T) {
	cases := []struct {
		in   string
		bp   int
		has  bool
		err  string
	}{
		{"", 0, false, ""},
		{"0", 0, true, ""},
		{"0%", 0, true, ""},
		{"0.0", 0, true, ""},
		{"55", 5500, true, ""},
		{"55%", 5500, true, ""},
		{"0.55", 5500, true, ""},
		{"100%", 10000, true, ""},
		{"101%", 0, false, "分成比例须在 0~100% 之间"},
		{"abc", 0, false, "分成比例不合法（支持 50 / 50% / 0.5）"},
	}
	for _, c := range cases {
		bp, has, err := parseShareRatioBP(c.in)
		if bp != c.bp || has != c.has || err != c.err {
			t.Errorf("parseShareRatioBP(%q) = (%d, %v, %q), want (%d, %v, %q)", c.in, bp, has, err, c.bp, c.has, c.err)
		}
	}
}

// TestChannelToPlatformWechatVideo 视频号必须映射到 wechat_video 平台
// （导入时靠该映射触发"平台自发、发行商分成记 0"特判；映射丢失会导致特判失效）
func TestChannelToPlatformWechatVideo(t *testing.T) {
	if got := channelToPlatform("视频号"); got != "wechat_video" {
		t.Errorf("channelToPlatform(视频号) = %q, want wechat_video", got)
	}
	if got := channelToPlatform("微信视频号"); got != "wechat_video" {
		t.Errorf("channelToPlatform(微信视频号) = %q, want wechat_video", got)
	}
	if got := channelToPlatform("抖音"); got != "douyin" {
		t.Errorf("channelToPlatform(抖音) = %q, want douyin", got)
	}
	if got := channelToPlatform("腾讯"); got != "" {
		t.Errorf("channelToPlatform(腾讯) = %q, want empty", got)
	}
}
