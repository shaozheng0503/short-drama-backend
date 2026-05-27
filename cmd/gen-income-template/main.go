// gen-income-template 生成「财务每日收入导入」Excel 模板。
//
// 用法：
//
//	go run ./cmd/gen-income-template            # 在当前目录生成 收益导入模板.xlsx
//	go run ./cmd/gen-income-template out.xlsx   # 指定输出路径
//
// 模板列与 POST /v1/admin/finance/income/import 解析逻辑一致：
//
//	A 列：剧目ID      B 列：日期(YYYY-MM-DD)   C 列：收入金额(元)
//
// 说明：这里导入的是**第三方平台**的收益数据；本平台自有付费收入走支付分账自动入账，无需人工导入。
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

	headers := []string{"剧目ID", "日期(YYYY-MM-DD)", "收入金额(元)"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = f.SetCellValue(sheet, cell, h)
	}
	// 两行示例，方便财务照着填（正式导入前请删除示例行或替换为真实数据）。
	samples := [][]interface{}{
		{1, "2026-05-26", 123.45},
		{1, "2026-05-27", 88.00},
	}
	for r, row := range samples {
		for col, v := range row {
			cell, _ := excelize.CoordinatesToCellName(col+1, r+2)
			_ = f.SetCellValue(sheet, cell, v)
		}
	}
	_ = f.SetColWidth(sheet, "A", "C", 20)

	if err := f.SaveAs(out); err != nil {
		fmt.Fprintln(os.Stderr, "生成失败:", err)
		os.Exit(1)
	}
	fmt.Println("已生成:", out)
}
