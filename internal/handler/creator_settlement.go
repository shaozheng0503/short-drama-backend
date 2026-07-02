package handler

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
)

// === 创作者侧：结算单 ===

// creatorSettlementSummary —— GET /v1/creator/settlement/summary
// 创作者侧账号收益 + 按剧提现页数据（按 OpenAPI CreatorSettlementSummaryPage schema 实现）。
//
// 2026-07-02 修（邱嘉诚 15:25 反馈 schema 对不上）：
// OpenAPI 上 schema 是 {summary, list[], page, page_size, total}，summary 含 5 字段，
// 但之前实现只返了 3 字段（total_income/balance/min_withdrawal）且没 list。
//
// 这次补完：
//   - summary: 新增 withdraw_hint + missing_fields（资料不全时给前端提示）
//   - list: 按剧聚合，每部剧一行（drama_id, drama_title, income_cents, withdrawable_cents, withdrawn_cents, action）
//   - action.enabled: balance >= min_withdrawal_cents && verify_status=verified
//   - contract_attribute / share_type 字段暂不返（contracts 表没这 2 字段，二期加）
//
// 数据源：
//   - total_income_cents / balance_cents: creators 表（导入收入时已累加）
//   - list[].income_cents: creator_stats_daily 聚合（按 creator_id+drama_id SUM）
//   - list[].withdrawn_cents: withdrawals 表（pending/approved/paid 状态，rejected 不算）
//   - list[].withdrawable_cents: income_cents - withdrawn_cents
func (s *Server) creatorSettlementSummary(c *gin.Context) {
	creatorID := middleware.CurrentID(c)

	// 1. 创作者主信息
	var creator model.Creator
	if err := s.db.First(&creator, creatorID).Error; err != nil {
		response.ServerError(c, "查询创作者收益失败")
		return
	}

	// 2. 最低提现门槛
	minWithdrawal := int64(10000)
	if s.cfg.MinWithdrawalCents > 0 {
		minWithdrawal = s.cfg.MinWithdrawalCents
	}

	// 3. 资料完整性检查（用于 summary.missing_fields + withdraw_hint）
	missingFields := s.collectMissingProfileFields(creator)
	withdrawHint := ""
	if len(missingFields) > 0 {
		withdrawHint = "提现前需先完善实名认证和银行卡信息"
	}

	// 4. 按剧聚合收入（stats_daily）
	type dramaAgg struct {
		DramaID     uint64
		IncomeCents int64
	}
	var dramaAggs []dramaAgg
	s.db.Table("creator_stats_daily").
		Select("drama_id, SUM(income_cents) AS income_cents").
		Where("creator_id = ?", creatorID).
		Group("drama_id").
		Order("income_cents DESC").
		Scan(&dramaAggs)

	// 5. 按 drama 维度查已提现占用（pending/approved/paid）
	type dramaWithdrawn struct {
		DramaID       uint64
		WithdrawnCents int64
	}
	var dramaWithdrawns []dramaWithdrawn
	s.db.Table("withdrawals").
		Select("drama_id, COALESCE(SUM(amount_cents),0) AS withdrawn_cents").
		Where("creator_id = ? AND status IN ?", creatorID, []string{"pending", "approved", "paid"}).
		Group("drama_id").
		Scan(&dramaWithdrawns)
	withdrawnMap := make(map[uint64]int64, len(dramaWithdrawns))
	for _, w := range dramaWithdrawns {
		withdrawnMap[w.DramaID] = w.WithdrawnCents
	}

	// 6. 拉 drama 标题（按 ID 一次拉完）
	dramaIDs := make([]uint64, 0, len(dramaAggs))
	for _, d := range dramaAggs {
		dramaIDs = append(dramaIDs, d.DramaID)
	}
	titleMap := make(map[uint64]string, len(dramaIDs))
	if len(dramaIDs) > 0 {
		var dramas []model.Drama
		s.db.Select("id, title").Where("id IN ?", dramaIDs).Find(&dramas)
		for _, d := range dramas {
			titleMap[d.ID] = d.Title
		}
	}

	// 7. 组装 list 数组
	verifiedCanWithdraw := creator.VerifyStatus == model.CreatorVerifyVerified && len(missingFields) == 0
	list := make([]gin.H, 0, len(dramaAggs))
	for _, d := range dramaAggs {
		withdrawn := withdrawnMap[d.DramaID]
		withdrawable := d.IncomeCents - withdrawn
		if withdrawable < 0 {
			withdrawable = 0
		}
		// 提现按钮 enabled：可提现金额 >= 最低门槛 + 实名 verified + 资料完整
		enabled := withdrawable >= minWithdrawal && verifiedCanWithdraw
		dramaID := d.DramaID
		list = append(list, gin.H{
			"drama_id":               dramaID,
			"drama_title":            titleMap[dramaID],
			"contract_attribute":     nil, // 暂不返（contracts 表没这字段）
			"contract_attribute_label": "对私",  // MVP 默认值
			"share_type":             nil, // 暂不返
			"share_type_label":       "纯分成", // MVP 默认值
			"income_cents":           d.IncomeCents,
			"withdrawable_cents":     withdrawable,
			"withdrawn_cents":        withdrawn,
			"action": gin.H{
				"type":         "withdraw",
				"label":        "立即提现",
				"enabled":      enabled,
				"drama_id":     dramaID,
				"amount_cents": withdrawable,
			},
		})
	}

	// 8. 响应
	response.OK(c, gin.H{
		"summary": gin.H{
			"total_income_cents":   creator.TotalIncomeCents,
			"balance_cents":        creator.BalanceCents,
			"min_withdrawal_cents": minWithdrawal,
			"withdraw_hint":        withdrawHint,
			"missing_fields":       missingFields,
		},
		"list":       list,
		"page":       1,
		"page_size":  len(list),
		"total":      len(list),
	})
}

// collectMissingProfileFields 检查创作者资料完整性，返回缺失字段列表
// 用于 summary.missing_fields，前端根据这个决定是否显示「去完善」按钮
func (s *Server) collectMissingProfileFields(c model.Creator) []string {
	missing := []string{}
	if c.Name == "" {
		missing = append(missing, "name")
	}
	if c.IDCardNoEnc == "" {
		missing = append(missing, "id_card_no")
	}
	if c.BankName == "" {
		missing = append(missing, "bank_name")
	}
	if c.BankCardNoEnc == "" {
		missing = append(missing, "bank_card_no")
	}
	if c.Phone == "" {
		missing = append(missing, "phone")
	}
	return missing
}

// creatorListSettlements —— GET /v1/creator/settlements
// 创作者查看自己的结算单列表（按月倒序，附该结算单已审核通过的发票金额合计）。
func (s *Server) creatorListSettlements(c *gin.Context) {
	creatorID := middleware.CurrentID(c)
	page, pageSize := paginate(c)
	q := s.db.Model(&model.Settlement{}).Where("creator_id = ?", creatorID)
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("period"); v != "" {
		q = q.Where("period = ?", v)
	}
	if v := c.Query("contract_no"); v != "" {
		q = q.Where("contract_no = ?", v)
	}
	var total int64
	q.Count(&total)
	var rows []model.Settlement
	q.Order("period desc, id desc").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)

	// 一次性查全部结算单的「已审核通过发票金额合计」避免 N+1
	settleIDs := make([]uint64, 0, len(rows))
	for _, r := range rows {
		settleIDs = append(settleIDs, r.ID)
	}
	approvedSum := map[uint64]int64{}
	if len(settleIDs) > 0 {
		type pair struct {
			SettlementID uint64
			Sum          int64
		}
		var pairs []pair
		s.db.Model(&model.Invoice{}).
			Select("settlement_id, COALESCE(SUM(amount_cents),0) AS sum").
			Where("settlement_id IN ? AND status = ?", settleIDs, model.InvoiceStatusApproved).
			Group("settlement_id").Scan(&pairs)
		for _, p := range pairs {
			approvedSum[p.SettlementID] = p.Sum
		}
	}

	list := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		list = append(list, gin.H{
			"id":                  r.ID,
			"settlement_no":       r.SettlementNo,
			"creator_id":          r.CreatorID,
			"contract_no":         r.ContractNo,
			"period":              r.Period,
			"gross_cents":         r.GrossCents,
			"platform_cents":      r.PlatformCents,
			"net_cents":           r.NetCents,
			"status":              r.Status,
			"approved_invoice_cents": approvedSum[r.ID], // 已审核通过发票金额合计
			"created_at":          r.CreatedAt,
			"closed_at":           r.ClosedAt,
		})
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

// creatorGetSettlement —— GET /v1/creator/settlements/:id
// 创作者查看结算单详情：基础信息 + 订单明细 + 关联发票列表。
func (s *Server) creatorGetSettlement(c *gin.Context) {
	creatorID := middleware.CurrentID(c)
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var st model.Settlement
	if err := s.db.Where("id = ? AND creator_id = ?", id, creatorID).First(&st).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "结算单不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	// 订单明细
	var items []model.SettlementItem
	s.db.Where("settlement_id = ?", st.ID).Order("paid_at asc, id asc").Find(&items)
	itemViews := make([]gin.H, 0, len(items))
	for _, it := range items {
		itemViews = append(itemViews, gin.H{
			"id":            it.ID,
			"order_id":      it.OrderID,
			"order_no":      it.OrderNo,
			"drama_id":      it.DramaID,
			"source":        it.Source,
			"amount_cents":  it.AmountCents,
			"paid_at":       it.PaidAt,
		})
	}
	// 发票列表（只返创作者自己看得到的字段）
	var invoices []model.Invoice
	s.db.Where("settlement_id = ?", st.ID).Order("created_at desc").Find(&invoices)
	invViews := make([]gin.H, 0, len(invoices))
	approvedSum := int64(0)
	for _, inv := range invoices {
		invView := gin.H{
			"id":            inv.ID,
			"invoice_no":    inv.InvoiceNo,
			"invoice_type":  inv.InvoiceType,
			"external_no":   inv.ExternalNo,
			"amount_cents":  inv.AmountCents,
			"file_url":      inv.FileURL,
			"file_size":     inv.FileSize,
			"status":        inv.Status,
			"reject_reason": inv.RejectReason,
			"reviewed_at":   inv.ReviewedAt,
			"created_at":    inv.CreatedAt,
		}
		// 私有的 file_hash / reviewed_by 不返
		invViews = append(invViews, invView)
		if inv.Status == model.InvoiceStatusApproved {
			approvedSum += inv.AmountCents
		}
	}
	// 公司抬头（用于前端展示"请开给：xxx"，来自 .env 平台配置）
	platformName := strings.TrimSpace(s.cfg.PlatformCompanyName)
	if platformName == "" {
		platformName = "海南琅智网络科技有限公司" // 兜底默认值
	}
	platformTaxNo := strings.TrimSpace(s.cfg.PlatformTaxNo)
	platformBankName := strings.TrimSpace(s.cfg.PlatformBankName)
	platformBankAccount := strings.TrimSpace(s.cfg.PlatformBankAccount)
	platformAddress := strings.TrimSpace(s.cfg.PlatformAddress)
	platformPhone := strings.TrimSpace(s.cfg.PlatformPhone)

	response.OK(c, gin.H{
		"id":             st.ID,
		"settlement_no":  st.SettlementNo,
		"creator_id":     st.CreatorID,
		"contract_no":    st.ContractNo,
		"period":         st.Period,
		"gross_cents":    st.GrossCents,
		"platform_cents": st.PlatformCents,
		"net_cents":      st.NetCents,
		"status":         st.Status,
		"approved_invoice_cents": approvedSum,
		"items":          itemViews,
		"invoices":       invViews,
		"remark":         st.Remark,
		"created_at":     st.CreatedAt,
		"closed_at":      st.ClosedAt,
		// 公司抬头（开票信息，源自 .env）
		"platform_company": gin.H{
			"name":          platformName,
			"tax_no":        platformTaxNo,
			"bank_name":     platformBankName,
			"bank_account":  platformBankAccount,
			"address":       platformAddress,
			"phone":         platformPhone,
		},
	})
}

// creatorDownloadSettlementExcel —— GET /v1/creator/settlements/:id/download
// 创作者下载结算单 Excel 对账单（含订单明细）。PDF 二期再做。
func (s *Server) creatorDownloadSettlementExcel(c *gin.Context) {
	creatorID := middleware.CurrentID(c)
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var st model.Settlement
	if err := s.db.Where("id = ? AND creator_id = ?", id, creatorID).First(&st).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "结算单不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	var items []model.SettlementItem
	s.db.Where("settlement_id = ?", st.ID).Order("paid_at asc, id asc").Find(&items)

	// 平台抬头（.env）
	platformName := strings.TrimSpace(s.cfg.PlatformCompanyName)
	if platformName == "" {
		platformName = "海南琅智网络科技有限公司"
	}

	// 生成 xlsx
	f := excelize.NewFile()
	defer f.Close()
	sheet := "对账单"
	f.SetSheetName("Sheet1", sheet)

	// 头部：公司抬头 + 标题
	f.SetCellValue(sheet, "A1", "对账单")
	f.MergeCell(sheet, "A1", "F1")
	f.SetCellValue(sheet, "A2", "出具方："+platformName)
	f.MergeCell(sheet, "A2", "F2")
	f.SetCellValue(sheet, "A3", "结算单号："+st.SettlementNo)
	f.MergeCell(sheet, "A3", "F3")
	f.SetCellValue(sheet, "A4", "结算月份："+st.Period)
	f.MergeCell(sheet, "A4", "F4")
	f.SetCellValue(sheet, "A5", "合同编号："+st.ContractNo)
	f.MergeCell(sheet, "A5", "F5")
	f.SetCellValue(sheet, "A6", "结算方/创作者ID："+strconv.FormatUint(st.CreatorID, 10))
	f.MergeCell(sheet, "A6", "F6")

	// 合计
	f.SetCellValue(sheet, "A8", "结算总金额（元）")
	f.SetCellValue(sheet, "B8", fmt.Sprintf("%.2f", float64(st.NetCents)/100))
	f.SetCellValue(sheet, "A9", "订单总流水（元）")
	f.SetCellValue(sheet, "B9", fmt.Sprintf("%.2f", float64(st.GrossCents)/100))
	f.SetCellValue(sheet, "A10", "平台抽成（元）")
	f.SetCellValue(sheet, "B10", fmt.Sprintf("%.2f", float64(st.PlatformCents)/100))

	// 订单明细表
	headers := []string{"序号", "订单号", "剧ID", "来源", "订单金额(元)", "分成实得(元)", "支付时间"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 12)
		f.SetCellValue(sheet, cell, h)
	}
	for i, it := range items {
		row := 13 + i
		f.SetCellValue(sheet, fmt.Sprintf("A%d", row), i+1)
		f.SetCellValue(sheet, fmt.Sprintf("B%d", row), it.OrderNo)
		f.SetCellValue(sheet, fmt.Sprintf("C%d", row), it.DramaID)
		f.SetCellValue(sheet, fmt.Sprintf("D%d", row), it.Source)
		f.SetCellValue(sheet, fmt.Sprintf("E%d", row), fmt.Sprintf("%.2f", float64(it.AmountCents)/100))
		if it.PaidAt != nil {
			f.SetCellValue(sheet, fmt.Sprintf("G%d", row), it.PaidAt.Format("2006-01-02 15:04:05"))
		}
	}

	// 我方开票信息（创作者需要照此开票）
	taxRow := 13 + len(items) + 2
	f.SetCellValue(sheet, fmt.Sprintf("A%d", taxRow), "我方开票信息（请按此开票）")
	f.MergeCell(sheet, fmt.Sprintf("A%d", taxRow), fmt.Sprintf("F%d", taxRow))
	taxRow++
	f.SetCellValue(sheet, fmt.Sprintf("A%d", taxRow), "账户名称")
	f.SetCellValue(sheet, fmt.Sprintf("B%d", taxRow), platformName)
	taxRow++
	f.SetCellValue(sheet, fmt.Sprintf("A%d", taxRow), "开户行名称")
	f.SetCellValue(sheet, fmt.Sprintf("B%d", taxRow), s.cfg.PlatformBankName)
	taxRow++
	f.SetCellValue(sheet, fmt.Sprintf("A%d", taxRow), "开户行账号")
	f.SetCellValue(sheet, fmt.Sprintf("B%d", taxRow), s.cfg.PlatformBankAccount)
	taxRow++
	f.SetCellValue(sheet, fmt.Sprintf("A%d", taxRow), "纳税识别号")
	f.SetCellValue(sheet, fmt.Sprintf("B%d", taxRow), s.cfg.PlatformTaxNo)
	taxRow++
	f.SetCellValue(sheet, fmt.Sprintf("A%d", taxRow), "注册地址")
	f.SetCellValue(sheet, fmt.Sprintf("B%d", taxRow), s.cfg.PlatformAddress)
	taxRow++
	f.SetCellValue(sheet, fmt.Sprintf("A%d", taxRow), "电话")
	f.SetCellValue(sheet, fmt.Sprintf("B%d", taxRow), s.cfg.PlatformPhone)

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		response.ServerError(c, "生成 Excel 失败")
		return
	}
	filename := fmt.Sprintf("settlement_%s_%s.xlsx", st.SettlementNo, time.Now().Format("20060102"))
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", buf.Bytes())
}

// creatorDownloadSettlementPDF —— GET /v1/creator/settlements/:id/download.pdf
// 创作者下载结算单 PDF 对账单（存档/发邮件用，固定版式）。
// 中文显示：embed.FS 嵌入的 Noto Sans SC subset 字体（22.7KB），渲染中文字符不乱码。
func (s *Server) creatorDownloadSettlementPDF(c *gin.Context) {
	creatorID := middleware.CurrentID(c)
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var st model.Settlement
	if err := s.db.Where("id = ? AND creator_id = ?", id, creatorID).First(&st).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "结算单不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	var items []model.SettlementItem
	s.db.Where("settlement_id = ?", st.ID).Order("paid_at asc, id asc").Find(&items)

	// 渲染 PDF
	var buf bytes.Buffer
	if err := s.renderSettlementPDF(st, items, s.platformCompanyFromConfig(), &buf); err != nil {
		log.Printf("[settlement-pdf] render err id=%d err=%v", id, err)
		response.ServerError(c, "生成 PDF 失败")
		return
	}
	filename := fmt.Sprintf("settlement_%s_%s.pdf", st.SettlementNo, time.Now().Format("20060102"))
	c.Header("Content-Type", "application/pdf")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Data(http.StatusOK, "application/pdf", buf.Bytes())
}

// === 创作者侧：发票 ===

// creatorCreateInvoice —— POST /v1/creator/invoices
// 创作者提交一张发票（file_url 已通过 image-sign 拿到，PUT 上传完）。
// 同一结算单支持多张发票累加；状态默认 pending。
func (s *Server) creatorCreateInvoice(c *gin.Context) {
	creatorID := middleware.CurrentID(c)
	var req struct {
		SettlementID uint64 `json:"settlement_id" binding:"required"`
		InvoiceType  string `json:"invoice_type"   binding:"required"`
		ExternalNo   string `json:"external_no"`
		AmountCents  int64  `json:"amount_cents"   binding:"required,gt=0"`
		FileURL      string `json:"file_url"       binding:"required"`
		FileHash     string `json:"file_hash"`
		FileSize     int64  `json:"file_size"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法："+err.Error())
		return
	}
	// 校验发票类型
	switch req.InvoiceType {
	case model.InvoiceTypeVATSpecial, model.InvoiceTypeVATGeneral,
		model.InvoiceTypeEVATSpecial, model.InvoiceTypeEVATGeneral:
	default:
		response.InvalidParam(c, "invoice_type 不合法")
		return
	}
	// 校验结算单属于自己
	var st model.Settlement
	if err := s.db.Where("id = ? AND creator_id = ?", req.SettlementID, creatorID).First(&st).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "结算单不存在")
		} else {
			response.ServerError(c, "查询失败")
		}
		return
	}
	if st.Status == model.SettlementStatusPaid || st.Status == model.SettlementStatusVoid {
		response.Conflict(c, "结算单已 "+st.Status+"，不能再上传发票")
		return
	}
	// 业务编号（INV + yyyymm + 序号）
	bizNo := generateInvoiceBizNo()
	inv := model.Invoice{
		InvoiceNo:    bizNo,
		SettlementID: req.SettlementID,
		CreatorID:    creatorID,
		InvoiceType:  req.InvoiceType,
		ExternalNo:   req.ExternalNo,
		AmountCents:  req.AmountCents,
		FileURL:      req.FileURL,
		FileHash:     req.FileHash,
		FileSize:     req.FileSize,
		Status:       model.InvoiceStatusPending,
	}
	if err := s.db.Create(&inv).Error; err != nil {
		response.ServerError(c, "提交发票失败")
		return
	}
	// 顺带把结算单 status 推到 invoiced（仅当之前是 open）
	if st.Status == model.SettlementStatusOpen {
		s.db.Model(&st).Update("status", model.SettlementStatusInvoiced)
	}
	response.OK(c, gin.H{
		"id":            inv.ID,
		"invoice_no":    inv.InvoiceNo,
		"settlement_id": inv.SettlementID,
		"invoice_type":  inv.InvoiceType,
		"external_no":   inv.ExternalNo,
		"amount_cents":  inv.AmountCents,
		"file_url":      inv.FileURL,
		"file_size":     inv.FileSize,
		"status":        inv.Status,
		"created_at":    inv.CreatedAt,
	})
}

// creatorListInvoices —— GET /v1/creator/invoices
// 创作者查看自己的发票列表（按结算单 + 状态筛选）。
func (s *Server) creatorListInvoices(c *gin.Context) {
	creatorID := middleware.CurrentID(c)
	page, pageSize := paginate(c)
	q := s.db.Model(&model.Invoice{}).Where("creator_id = ?", creatorID)
	if v := c.Query("settlement_id"); v != "" {
		if id := parseUint(v); id > 0 {
			q = q.Where("settlement_id = ?", id)
		}
	}
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	var total int64
	q.Count(&total)
	var rows []model.Invoice
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows)
	list := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		list = append(list, gin.H{
			"id":            r.ID,
			"invoice_no":    r.InvoiceNo,
			"settlement_id": r.SettlementID,
			"invoice_type":  r.InvoiceType,
			"external_no":   r.ExternalNo,
			"amount_cents":  r.AmountCents,
			"file_url":      r.FileURL,
			"file_size":     r.FileSize,
			"status":        r.Status,
			"reject_reason": r.RejectReason,
			"reviewed_at":   r.ReviewedAt,
			"created_at":    r.CreatedAt,
		})
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

// creatorGetInvoice —— GET /v1/creator/invoices/:id
func (s *Server) creatorGetInvoice(c *gin.Context) {
	creatorID := middleware.CurrentID(c)
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var inv model.Invoice
	if err := s.db.Where("id = ? AND creator_id = ?", id, creatorID).First(&inv).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "发票不存在")
		} else {
			response.ServerError(c, "查询失败")
		}
		return
	}
	response.OK(c, gin.H{
		"id":            inv.ID,
		"invoice_no":    inv.InvoiceNo,
		"settlement_id": inv.SettlementID,
		"invoice_type":  inv.InvoiceType,
		"external_no":   inv.ExternalNo,
		"amount_cents":  inv.AmountCents,
		"file_url":      inv.FileURL,
		"file_size":     inv.FileSize,
		"status":        inv.Status,
		"reject_reason": inv.RejectReason,
		"reviewed_at":   inv.ReviewedAt,
		"created_at":    inv.CreatedAt,
	})
}

// creatorCancelInvoice —— DELETE /v1/creator/invoices/:id
// 创作者撤销一张未审核的发票（仅 pending 状态可撤销；已 approved 不允许）。
func (s *Server) creatorCancelInvoice(c *gin.Context) {
	creatorID := middleware.CurrentID(c)
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var inv model.Invoice
	if err := s.db.Where("id = ? AND creator_id = ?", id, creatorID).First(&inv).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "发票不存在")
		} else {
			response.ServerError(c, "查询失败")
		}
		return
	}
	if inv.Status != model.InvoiceStatusPending {
		response.Conflict(c, "仅待审核的发票可撤销，当前状态："+inv.Status)
		return
	}
	if err := s.db.Delete(&inv).Error; err != nil {
		response.ServerError(c, "撤销失败")
		return
	}
	response.OK(c, gin.H{"id": id, "deleted": true})
}

// generateInvoiceBizNo 生成发票业务编号（INVyyyyMM-XXXX）。
// 简化实现：yyyyMM + 当前秒的 hex 末 4 位；并发场景下不保证唯一，
// 真实生产应改成 MAX(invoice_no) 自增或在 DB 加 unique index 兜底。
func generateInvoiceBizNo() string {
	now := time.Now()
	prefix := "INV" + now.Format("200601") + "-"
	suffix := strconv.FormatInt(now.UnixNano()%10000, 10)
	for len(suffix) < 4 {
		suffix = "0" + suffix
	}
	return prefix + suffix
}
