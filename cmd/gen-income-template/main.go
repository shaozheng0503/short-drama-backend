// gen-income-template 生成「财务每日收入导入」Excel 模板。
//
// 用法：
//
//	go run ./cmd/gen-income-template            # 在当前目录生成 收益导入模板.xlsx
//	go run ./cmd/gen-income-template out.xlsx   # 指定输出路径
//
// 模板列与 POST /v1/admin/finance/income/import 解析逻辑一致：
//
//	A 列：短剧名称   B 列：渠道   C 列：总收益   D 列：分成比例(50/50%/0.5,留空按配置)
//	E 列：日期   F 列：短剧ID(选填,名称重复时必填)
//
// 说明：这里导入的是**第三方渠道**的收益数据；本平台自有付费收入走支付分账自动入账，无需人工导入。
// 创作者实得 = round(总收益 × 比例)，比例留空时按渠道全局配置回落。
package main

import (
	"fmt"
	"os"

	"github.com/xuri/excelize/v2"
)

func main() {
	out := "收益导入模板.xlsx"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}

	f := excelize.NewFile()
	defer f.Close()
	sheet := "Sheet1"

	headers := []string{"短剧名称", "渠道", "总收益", "分成比例(如50或50%或0.5,留空按配置)", "日期", "短剧ID(选填,名称重复时必填)"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = f.SetCellValue(sheet, cell, h)
	}
	// 两行示例，方便财务照着填（正式导入前请删除示例行或替换为真实数据）。
	// 第二行示例展示当名称重复时通过 F 列「短剧ID」精确定位，且 D 列留空走全局配置比例。
	samples := [][]interface{}{
		{"总裁的逆袭新娘", "抖音", 123.45, "50%", "2026-05-26", ""},
		{"总裁的逆袭新娘", "快手", 88.00, "", "2026-05-27", 42},
	}
	for r, row := range samples {
		for col, v := range row {
			cell, _ := excelize.CoordinatesToCellName(col+1, r+2)
			_ = f.SetCellValue(sheet, cell, v)
		}
	}
	_ = f.SetColWidth(sheet, "A", "A", 28)
	_ = f.SetColWidth(sheet, "B", "C", 16)
	_ = f.SetColWidth(sheet, "D", "D", 30)
	_ = f.SetColWidth(sheet, "E", "E", 16)
	_ = f.SetColWidth(sheet, "F", "F", 30)

	if err := f.SaveAs(out); err != nil {
		fmt.Fprintln(os.Stderr, "生成失败:", err)
		os.Exit(1)
	}
	fmt.Println("已生成:", out)
}
