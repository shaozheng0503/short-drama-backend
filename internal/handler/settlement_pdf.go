package handler

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"strings"
	"time"

	"ai-drama-platform/internal/model"

	"github.com/go-pdf/fpdf"
)

// === 字体嵌入 ===
// PDF 必走中文字体，go-pdf/fpdf 的内置 Helvetica 渲染中文会变方块。
// 用 embed.FS 把 22.7KB 的 Noto Sans SC subset 字体（仅含对账单用到的字符）
// 嵌进 binary，部署无任何外部依赖。
// 若以后需加新字（如粗体/特殊符号），重新跑 fontTools subsetter 覆盖。
//
//go:embed assets/fonts/NotoSansSC-Regular.ttf
var notoSansSCBytes []byte

// renderSettlementPDF 写一张结算单对账单 PDF 到 w。
// 复用 creator + admin 两个接口，输出统一版式。
//
// 版式（A4 纵向，单位 mm）：
//   ┌──────────────────────────────────────────┐
//   │              对账单                       │ ← 标题 18pt 加粗
//   │ 出具方：海南琅智网络科技有限公司            │
//   │ 结算单号 / 结算月份 / 合同编号 / 创作者 ID  │
//   │ ──────────────────────────────────────── │
//   │ 结算总金额（元）            xxx.xx        │
//   │ 订单总流水（元）            xxx.xx        │
//   │ 税率  （元）                xxx.xx        │
//   │ ──────────────────────────────────────── │
//   │ 订单明细表头（序号|订单号|剧ID|来源|...）  │
//   │ ┌──┬──┬──┬──┬──┬──┐                      │
//   │ │  │  │  │  │  │  │                      │
//   │ └──┴──┴──┴──┴──┴──┘                      │
//   │ ──────────────────────────────────────── │
//   │ 我方开票信息（请按此开票）                 │
//   │   账户名称 / 开户行 / 账号 / 税号 / ...    │
//   │ 打印时间：YYYY-MM-DD HH:mm:ss             │
//   └──────────────────────────────────────────┘
func (s *Server) renderSettlementPDF(
	st model.Settlement,
	items []model.SettlementItem,
	platformCompany *PlatformCompanyInfo,
	w io.Writer,
) error {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.AddPage()

	// 注册中文字体（从 embed.FS 读取）
	pdf.AddUTF8FontFromBytes("noto", "", notoSansSCBytes)
	pdf.SetFont("noto", "", 12)

	// === 标题 ===
	pdf.SetFont("noto", "", 18)
	pdf.CellFormat(0, 12, "对账单", "", 1, "C", false, 0, "")
	pdf.Ln(2)

	// === 头部信息 ===
	pdf.SetFont("noto", "", 11)
	pdf.CellFormat(0, 7, "出具方："+platformCompany.Name, "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 6, "结算单号："+st.SettlementNo, "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 6, "结算月份："+st.Period, "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 6, "合同编号："+st.ContractNo, "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 6, "创作者 ID："+fmt.Sprintf("%d", st.CreatorID), "", 1, "L", false, 0, "")
	if st.Status == model.SettlementStatusSettled {
		pdf.CellFormat(0, 6, "状态：已结算", "", 1, "L", false, 0, "")
	} else if st.Status == model.SettlementStatusVoid {
		pdf.CellFormat(0, 6, "状态：已作废", "", 1, "L", false, 0, "")
	} else {
		pdf.CellFormat(0, 6, "状态：未结算", "", 1, "L", false, 0, "")
	}
	pdf.Ln(3)

	// === 金额汇总（3 行）===
	pdf.SetFont("noto", "", 12)
	pdf.SetFillColor(240, 240, 240)
	pdf.CellFormat(60, 8, "结算总金额（元）", "1", 0, "L", true, 0, "")
	pdf.CellFormat(0, 8, fmt.Sprintf("%.2f", float64(st.NetCents)/100), "1", 1, "L", false, 0, "")
	pdf.CellFormat(60, 8, "订单总流水（元）", "1", 0, "L", true, 0, "")
	pdf.CellFormat(0, 8, fmt.Sprintf("%.2f", float64(st.GrossCents)/100), "1", 1, "L", false, 0, "")
	pdf.CellFormat(60, 8, "税率（元）", "1", 0, "L", true, 0, "")
	pdf.CellFormat(0, 8, fmt.Sprintf("%.2f", float64(st.PlatformCents)/100), "1", 1, "L", false, 0, "")
	pdf.Ln(5)

	// === 订单明细表头 ===
	pdf.SetFont("noto", "", 11)
	pdf.CellFormat(0, 7, fmt.Sprintf("订单明细（共 %d 笔）", len(items)), "", 1, "L", false, 0, "")
	pdf.SetFillColor(220, 220, 220)
	pdf.SetFont("noto", "", 10)
	// 列宽：A4 减去左右边距 10*2=20mm，可用 190mm
	// 6 列：序号 12 / 订单号 50 / 剧ID 18 / 来源 18 / 金额 32 / 支付时间 60
	colW := []float64{12, 50, 18, 18, 32, 60}
	headers := []string{"序号", "订单号", "剧ID", "来源", "金额(元)", "支付时间"}
	for i, h := range headers {
		pdf.CellFormat(colW[i], 8, h, "1", 0, "C", true, 0, "")
	}
	pdf.Ln(-1)

	// === 订单明细数据行 ===
	pdf.SetFont("noto", "", 9)
	pdf.SetFillColor(255, 255, 255)
	for i, it := range items {
		pdf.CellFormat(colW[0], 7, fmt.Sprintf("%d", i+1), "1", 0, "C", false, 0, "")
		pdf.CellFormat(colW[1], 7, it.OrderNo, "1", 0, "L", false, 0, "")
		pdf.CellFormat(colW[2], 7, fmt.Sprintf("%d", it.DramaID), "1", 0, "C", false, 0, "")
		pdf.CellFormat(colW[3], 7, it.Source, "1", 0, "C", false, 0, "")
		pdf.CellFormat(colW[4], 7, fmt.Sprintf("%.2f", float64(it.AmountCents)/100), "1", 0, "R", false, 0, "")
		paidAt := ""
		if it.PaidAt != nil {
			paidAt = it.PaidAt.Format("2006-01-02 15:04:05")
		}
		pdf.CellFormat(colW[5], 7, paidAt, "1", 0, "L", false, 0, "")
		pdf.Ln(-1)
	}
	if len(items) == 0 {
		pdf.CellFormat(0, 7, "（无订单明细）", "1", 1, "C", false, 0, "")
	}
	pdf.Ln(5)

	// === 我方开票信息块 ===
	pdf.SetFont("noto", "", 12)
	pdf.SetFillColor(240, 248, 255)
	pdf.CellFormat(0, 8, "我方开票信息（请按此开票）", "1", 1, "L", true, 0, "")
	pdf.SetFont("noto", "", 10)
	kvRows := [][2]string{
		{"账户名称", platformCompany.Name},
		{"开户行名称", platformCompany.BankName},
		{"开户行账号", platformCompany.BankAccount},
		{"纳税识别号", platformCompany.TaxNo},
		{"注册地址", platformCompany.Address},
		{"电话", platformCompany.Phone},
	}
	for _, kv := range kvRows {
		// 空值仍展示标签，便于创作者发现"还没配置"——但带括号提示
		val := kv[1]
		if strings.TrimSpace(val) == "" {
			val = "（请向平台索取最新抬头）"
		}
		pdf.CellFormat(40, 7, kv[0], "1", 0, "L", false, 0, "")
		pdf.CellFormat(0, 7, val, "1", 1, "L", false, 0, "")
	}
	pdf.Ln(3)

	// === 打印时间 + 自动生成说明 ===
	pdf.SetFont("noto", "", 9)
	pdf.SetTextColor(128, 128, 128)
	pdf.CellFormat(0, 5, "打印时间："+time.Now().Format("2006-01-02 15:04:05"), "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 5, "本对账单由 DramaBackend 自动生成，具有同等法律效力。", "", 1, "L", false, 0, "")

	// 输出到 writer
	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// PlatformCompanyInfo 平台开票信息块，PDF 渲染时从 cfg / db 拼出来
type PlatformCompanyInfo struct {
	Name        string
	TaxNo       string
	BankName    string
	BankAccount string
	Address     string
	Phone       string
}

// platformCompanyFromConfig 从 s.cfg 拼一个 PlatformCompanyInfo，Name 兜底默认值
func (s *Server) platformCompanyFromConfig() *PlatformCompanyInfo {
	name := strings.TrimSpace(s.cfg.PlatformCompanyName)
	if name == "" {
		name = "海南琅智网络科技有限公司" // 兜底
	}
	return &PlatformCompanyInfo{
		Name:        name,
		TaxNo:       strings.TrimSpace(s.cfg.PlatformTaxNo),
		BankName:    strings.TrimSpace(s.cfg.PlatformBankName),
		BankAccount: strings.TrimSpace(s.cfg.PlatformBankAccount),
		Address:     strings.TrimSpace(s.cfg.PlatformAddress),
		Phone:       strings.TrimSpace(s.cfg.PlatformPhone),
	}
}
