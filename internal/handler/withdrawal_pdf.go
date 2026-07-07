package handler

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"github.com/go-pdf/fpdf"
)

// invoiceTypeLabel 把 invoice_type 枚举转成中文标签。
func invoiceTypeLabel(t string) string {
	switch t {
	case model.InvoiceTypeVATSpecial:
		return "增值税专用发票"
	case model.InvoiceTypeVATGeneral:
		return "增值税普通发票"
	case model.InvoiceTypeEVATSpecial:
		return "增值税电子专用发票"
	case model.InvoiceTypeEVATGeneral:
		return "增值税电子普通发票"
	default:
		return t
	}
}

// withdrawalStatusLabel 把 withdrawal status 转成中文标签。
func withdrawalStatusLabel(s string) string {
	switch s {
	case model.WithdrawalStatusPending:
		return "待审核"
	case model.WithdrawalStatusApproved:
		return "审核通过待打款"
	case model.WithdrawalStatusPaid:
		return "已打款"
	case model.WithdrawalStatusRejected:
		return "已驳回"
	default:
		return s
	}
}

// transferTypeLabel 把 transfer_type 转中文。
func transferTypeLabel(t string) string {
	if t == model.TransferTypePublic {
		return "对公"
	}
	return "对私"
}

// renderWithdrawalPDF 写一张提现单 PDF 到 w。
// 创作者打款后下载报账用——含提现金额/个税/实到/关联发票/关联结算单/收款账户/平台抬头。
//
// 版式（A4 纵向）：
//   ┌──────────────────────────────────────────┐
//   │              创作者提现单                  │ ← 标题 18pt
//   │ 提现单号 / 申请日期 / 状态                 │
//   │ ──────────────────────────────────────── │
//   │ 金额汇总：提现金额 / 个税 / 实际到账       │
//   │ ──────────────────────────────────────── │
//   │ 关联发票信息（发票号 / 类型 / 金额 / 状态）│
//   │ ──────────────────────────────────────── │
//   │ 关联结算单（单号 / 期间 / 实收金额）       │
//   │ ──────────────────────────────────────── │
//   │ 收款账户（户名 / 开户行 / 卡号 / 类型）    │
//   │ ──────────────────────────────────────── │
//   │ 平台付款方信息（公司抬头 / 税号 / ...）    │
//   │ ──────────────────────────────────────── │
//   │ 审核记录（审核人 / 审核时间 / 打款时间）   │
//   │ 打印时间 + 自动生成说明                    │
//   └──────────────────────────────────────────┘
func (s *Server) renderWithdrawalPDF(
	wd model.Withdrawal,
	creator model.Creator,
	invoice *model.Invoice,
	settlement *model.Settlement,
	platformCompany *PlatformCompanyInfo,
	out io.Writer,
) error {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.AddPage()
	pdf.AddUTF8FontFromBytes("noto", "", notoSansSCBytes)
	pdf.SetFont("noto", "", 12)
	pdf.SetMargins(15, 15, 15)
	pdf.SetAutoPageBreak(true, 15)

	// === 标题 ===
	pdf.SetFont("noto", "", 18)
	pdf.CellFormat(0, 12, "创作者提现单", "", 1, "C", false, 0, "")
	pdf.Ln(2)

	// === 头部信息 ===
	pdf.SetFont("noto", "", 11)
	pdf.CellFormat(0, 6, "提现单号："+wd.WithdrawalNo, "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 6, "申请日期："+wd.CreatedAt.Format("2006-01-02 15:04"), "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 6, "状态："+withdrawalStatusLabel(wd.Status), "", 1, "L", false, 0, "")
	pdf.Ln(3)

	// === 金额汇总 ===
	pdf.SetFont("noto", "", 12)
	pdf.SetFillColor(240, 240, 240)
	pdf.CellFormat(60, 8, "提现金额（元）", "1", 0, "L", true, 0, "")
	pdf.CellFormat(0, 8, fmt.Sprintf("%.2f", float64(wd.AmountCents)/100), "1", 1, "R", false, 0, "")
	pdf.CellFormat(60, 8, "代扣个税（元）", "1", 0, "L", true, 0, "")
	pdf.CellFormat(0, 8, fmt.Sprintf("%.2f", float64(wd.TaxCents)/100), "1", 1, "R", false, 0, "")
	pdf.CellFormat(60, 8, "实际到账（元）", "1", 0, "L", true, 0, "")
	pdf.CellFormat(0, 8, fmt.Sprintf("%.2f", float64(wd.NetCents)/100), "1", 1, "R", false, 0, "")
	pdf.Ln(5)

	// === 关联发票 ===
	pdf.SetFont("noto", "", 11)
	pdf.CellFormat(0, 7, "关联发票", "", 1, "L", false, 0, "")
	pdf.SetFont("noto", "", 10)
	if invoice != nil {
		pdf.SetFillColor(220, 220, 220)
		colW := []float64{40, 55, 40, 55}
		headers := []string{"发票号码", "发票类型", "发票金额（元）", "审核状态"}
		for i, h := range headers {
			pdf.CellFormat(colW[i], 7, h, "1", 0, "C", true, 0, "")
		}
		pdf.Ln(-1)
		pdf.SetFillColor(255, 255, 255)
		pdf.CellFormat(colW[0], 6, invoice.InvoiceNo, "1", 0, "L", false, 0, "")
		pdf.CellFormat(colW[1], 6, invoiceTypeLabel(invoice.InvoiceType), "1", 0, "L", false, 0, "")
		pdf.CellFormat(colW[2], 6, fmt.Sprintf("%.2f", float64(invoice.AmountCents)/100), "1", 0, "R", false, 0, "")
		invStatus := "待审核"
		if invoice.Status == model.InvoiceStatusApproved {
			invStatus = "已通过"
		} else if invoice.Status == model.InvoiceStatusRejected {
			invStatus = "已驳回"
		}
		pdf.CellFormat(colW[3], 6, invStatus, "1", 0, "C", false, 0, "")
		pdf.Ln(-1)
	} else {
		pdf.CellFormat(0, 6, "（无关联发票）", "1", 1, "C", false, 0, "")
	}
	pdf.Ln(4)

	// === 关联结算单 ===
	pdf.SetFont("noto", "", 11)
	pdf.CellFormat(0, 7, "关联结算单", "", 1, "L", false, 0, "")
	pdf.SetFont("noto", "", 10)
	if settlement != nil {
		pdf.SetFillColor(220, 220, 220)
		colW2 := []float64{55, 55, 40, 40}
		headers2 := []string{"结算单号", "业务期间", "总流水（元）", "实收（元）"}
		for i, h := range headers2 {
			pdf.CellFormat(colW2[i], 7, h, "1", 0, "C", true, 0, "")
		}
		pdf.Ln(-1)
		pdf.SetFillColor(255, 255, 255)
		periodRange := settlement.PeriodRange
		if periodRange == "" {
			periodRange = settlement.Period + "（整月）"
		}
		pdf.CellFormat(colW2[0], 6, settlement.SettlementNo, "1", 0, "L", false, 0, "")
		pdf.CellFormat(colW2[1], 6, periodRange, "1", 0, "L", false, 0, "")
		pdf.CellFormat(colW2[2], 6, fmt.Sprintf("%.2f", float64(settlement.GrossCents)/100), "1", 0, "R", false, 0, "")
		pdf.CellFormat(colW2[3], 6, fmt.Sprintf("%.2f", float64(settlement.NetCents)/100), "1", 0, "R", false, 0, "")
		pdf.Ln(-1)
	} else {
		pdf.CellFormat(0, 6, "（无关联结算单）", "1", 1, "C", false, 0, "")
	}
	pdf.Ln(4)

	// === 收款账户 ===
	pdf.SetFont("noto", "", 11)
	pdf.CellFormat(0, 7, "收款账户", "", 1, "L", false, 0, "")
	pdf.SetFont("noto", "", 10)
	// 收款人姓名（机构用 OrgName，个人用 Name）
	payeeName := strings.TrimSpace(creator.Name)
	if creator.CreatorType == model.CreatorTypeOrganization && strings.TrimSpace(creator.OrgName) != "" {
		payeeName = strings.TrimSpace(creator.OrgName)
	}
	// 银行卡号：优先用 withdrawal 快照里的，兜底用 creator 脱敏值
	bankCardDisplay := wd.BankCardNoSnapshot
	if bankCardDisplay == "" {
		bankCardDisplay = creator.BankCardNoMasked
	}
	bankNameDisplay := wd.BankNameSnapshot
	if bankNameDisplay == "" {
		bankNameDisplay = creator.BankName
		if creator.BankBranch != "" {
			bankNameDisplay = bankNameDisplay + " " + creator.BankBranch
		}
	}
	kvAccount := [][2]string{
		{"收款人", payeeName},
		{"身份证号", creator.IDCardNoMasked},
		{"开户银行", bankNameDisplay},
		{"银行账号", bankCardDisplay},
		{"收款类型", transferTypeLabel(wd.TransferType)},
	}
	for _, kv := range kvAccount {
		val := kv[1]
		if strings.TrimSpace(val) == "" {
			val = "—"
		}
		pdf.CellFormat(40, 6.5, kv[0], "1", 0, "L", false, 0, "")
		pdf.CellFormat(0, 6.5, val, "1", 1, "L", false, 0, "")
	}
	pdf.Ln(4)

	// === 平台付款方信息 ===
	pdf.SetFont("noto", "", 11)
	pdf.SetFillColor(240, 248, 255)
	pdf.CellFormat(0, 8, "付款方信息（平台公司抬头）", "1", 1, "L", true, 0, "")
	pdf.SetFont("noto", "", 10)
	kvPlatform := [][2]string{
		{"公司名称", platformCompany.Name},
		{"纳税识别号", platformCompany.TaxNo},
		{"开户银行", platformCompany.BankName},
		{"银行账号", platformCompany.BankAccount},
		{"注册地址", platformCompany.Address},
		{"电话", platformCompany.Phone},
	}
	for _, kv := range kvPlatform {
		val := kv[1]
		if strings.TrimSpace(val) == "" {
			val = "（请向平台索取最新抬头）"
		}
		pdf.CellFormat(40, 6.5, kv[0], "1", 0, "L", false, 0, "")
		pdf.CellFormat(0, 6.5, val, "1", 1, "L", false, 0, "")
	}
	pdf.Ln(4)

	// === 审核记录 ===
	pdf.SetFont("noto", "", 11)
	pdf.CellFormat(0, 7, "审核记录", "", 1, "L", false, 0, "")
	pdf.SetFont("noto", "", 10)
	if wd.ReviewedAt != nil {
		pdf.CellFormat(60, 6.5, "审核时间", "1", 0, "L", false, 0, "")
		pdf.CellFormat(0, 6.5, wd.ReviewedAt.Format("2006-01-02 15:04:05"), "1", 1, "L", false, 0, "")
	}
	if wd.PaidAt != nil {
		pdf.CellFormat(60, 6.5, "打款时间", "1", 0, "L", false, 0, "")
		pdf.CellFormat(0, 6.5, wd.PaidAt.Format("2006-01-02 15:04:05"), "1", 1, "L", false, 0, "")
	}
	if wd.TransactionNo != "" {
		pdf.CellFormat(60, 6.5, "交易流水号", "1", 0, "L", false, 0, "")
		pdf.CellFormat(0, 6.5, wd.TransactionNo, "1", 1, "L", false, 0, "")
	}
	if wd.Remark != "" {
		pdf.CellFormat(60, 6.5, "备注", "1", 0, "L", false, 0, "")
		pdf.CellFormat(0, 6.5, wd.Remark, "1", 1, "L", false, 0, "")
	}
	pdf.Ln(4)

	// === 打印时间 + 自动生成说明 ===
	pdf.SetFont("noto", "", 9)
	pdf.SetTextColor(128, 128, 128)
	pdf.CellFormat(0, 5, "打印时间："+time.Now().Format("2006-01-02 15:04:05"), "", 1, "L", false, 0, "")
	pdf.CellFormat(0, 5, "本提现单由 DramaBackend 自动生成，具有同等法律效力。", "", 1, "L", false, 0, "")

	// 输出
	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return err
	}
	_, err := out.Write(buf.Bytes())
	return err
}

// loadWithdrawalPDFContext 加载提现单 PDF 所需的关联数据（创作者 + 发票 + 结算单）。
// creatorOnly 为 true 时校验提现单属于当前创作者。
func (s *Server) loadWithdrawalPDFContext(id, creatorID uint64, creatorOnly bool) (
	wd model.Withdrawal,
	creator model.Creator,
	invoice *model.Invoice,
	settlement *model.Settlement,
	err error,
) {
	q := s.db.Model(&model.Withdrawal{})
	if creatorOnly {
		q = q.Where("id = ? AND creator_id = ?", id, creatorID)
	} else {
		q = q.Where("id = ?", id)
	}
	if err = q.First(&wd).Error; err != nil {
		return
	}
	if err = s.db.First(&creator, wd.CreatorID).Error; err != nil {
		return
	}
	if wd.InvoiceID != nil {
		var inv model.Invoice
		if e := s.db.First(&inv, *wd.InvoiceID).Error; e == nil {
			invoice = &inv
			var st model.Settlement
			if e := s.db.First(&st, inv.SettlementID).Error; e == nil {
				settlement = &st
			}
		}
	}
	return
}

// creatorDownloadWithdrawalPDF —— GET /v1/creator/withdrawals/:id/download.pdf
// 创作者下载自己的提现单 PDF（报账/存档用，固定版式）。
func (s *Server) creatorDownloadWithdrawalPDF(c *gin.Context) {
	creatorID := middleware.CurrentID(c)
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	wd, creator, invoice, settlement, err := s.loadWithdrawalPDFContext(id, creatorID, true)
	if err != nil {
		if isNotFound(err) {
			response.NotFound(c, "提现单不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	var buf bytes.Buffer
	if err := s.renderWithdrawalPDF(wd, creator, invoice, settlement, s.platformCompanyFromConfig(), &buf); err != nil {
		log.Printf("[withdrawal-pdf] render err id=%d err=%v", id, err)
		response.ServerError(c, "生成 PDF 失败")
		return
	}
	filename := fmt.Sprintf("withdrawal_%s_%s.pdf", wd.WithdrawalNo, time.Now().Format("20060102"))
	c.Header("Content-Type", "application/pdf")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Data(http.StatusOK, "application/pdf", buf.Bytes())
}

// adminDownloadWithdrawalPDF —— GET /v1/admin/withdrawals/:id/download.pdf
// 财务下载任意创作者的提现单 PDF（存档/对账用）。
func (s *Server) adminDownloadWithdrawalPDF(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	wd, creator, invoice, settlement, err := s.loadWithdrawalPDFContext(id, 0, false)
	if err != nil {
		if isNotFound(err) {
			response.NotFound(c, "提现单不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	var buf bytes.Buffer
	if err := s.renderWithdrawalPDF(wd, creator, invoice, settlement, s.platformCompanyFromConfig(), &buf); err != nil {
		log.Printf("[withdrawal-pdf admin] render err id=%d err=%v", id, err)
		response.ServerError(c, "生成 PDF 失败")
		return
	}
	filename := fmt.Sprintf("withdrawal_%s_%s.pdf", wd.WithdrawalNo, time.Now().Format("20060102"))
	c.Header("Content-Type", "application/pdf")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Data(http.StatusOK, "application/pdf", buf.Bytes())
}
