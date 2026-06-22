package handler

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
)

// maxOrderExportRows 单次导出最多行数，防止超大结果集撑爆内存。
const maxOrderExportRows = 50000

// adminExportOrders —— GET /v1/admin/finance/orders-export.xlsx（财务角色）
// 把 App 用户购买订单导出成 Excel 供财务汇总（订单中心导出）。
// 过滤与 /admin/orders 一致：status / payment_method / user_id / drama_id，另加 start_date/end_date（按 paid_at）。
// 默认只导「曾支付成功」的订单（paid/refunded/partial_refunded）；status=all 可导全部。
func (s *Server) adminExportOrders(c *gin.Context) {
	q := s.db.Model(&model.Order{})

	switch strings.TrimSpace(c.Query("status")) {
	case "", "paid":
		// 默认：财务关心的是真实成交，导曾支付成功的单
		q = q.Where("status IN ?", []string{model.OrderStatusPaid, model.OrderStatusRefunded, model.OrderStatusPartialRefunded})
	case "all":
		// 不加状态过滤
	default:
		q = q.Where("status = ?", strings.TrimSpace(c.Query("status")))
	}
	if v := strings.TrimSpace(c.Query("payment_method")); v != "" {
		q = q.Where("payment_method = ?", v)
	}
	if v := parseUint(c.Query("user_id")); v > 0 {
		q = q.Where("user_id = ?", v)
	}
	if v := parseUint(c.Query("drama_id")); v > 0 {
		q = q.Where("drama_id = ?", v)
	}
	if v := strings.TrimSpace(c.Query("start_date")); v != "" {
		t, err := time.ParseInLocation("2006-01-02", v, time.Local)
		if err != nil {
			response.InvalidParam(c, "start_date 格式应为 YYYY-MM-DD")
			return
		}
		q = q.Where("paid_at >= ?", t)
	}
	if v := strings.TrimSpace(c.Query("end_date")); v != "" {
		t, err := time.ParseInLocation("2006-01-02", v, time.Local)
		if err != nil {
			response.InvalidParam(c, "end_date 格式应为 YYYY-MM-DD")
			return
		}
		q = q.Where("paid_at < ?", t.AddDate(0, 0, 1))
	}

	var orders []model.Order
	if err := q.Order("created_at desc").Limit(maxOrderExportRows).Find(&orders).Error; err != nil {
		response.ServerError(c, "查询订单失败")
		return
	}

	// 批量补短剧标题，避免逐行查（N+1）。
	dramaIDs := make([]uint64, 0, len(orders))
	for _, o := range orders {
		dramaIDs = append(dramaIDs, o.DramaID)
	}
	titles, _ := s.dramaTitleCreatorMap(dramaIDs)

	xl := excelize.NewFile()
	defer xl.Close()
	sheet := "订单"
	xl.SetSheetName("Sheet1", sheet)
	headers := []string{"订单号", "用户ID", "短剧ID", "短剧名称", "集数", "实付金额(元)", "已退金额(元)", "净额(元)", "支付方式", "状态", "下单时间", "支付时间", "渠道流水号"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = xl.SetCellValue(sheet, cell, h)
	}
	for r, o := range orders {
		row := r + 2
		epCount := 1
		if len(o.EpisodeIDs) > 0 {
			epCount = len(o.EpisodeIDs)
		}
		paidAt := ""
		if o.PaidAt != nil {
			paidAt = o.PaidAt.In(time.Local).Format("2006-01-02 15:04:05")
		}
		vals := []interface{}{
			o.OrderNo,
			o.UserID,
			o.DramaID,
			titles[o.DramaID],
			epCount,
			yuanFloat(o.AmountCents),
			yuanFloat(o.RefundAmountCents),
			yuanFloat(o.AmountCents - o.RefundAmountCents),
			o.PaymentMethod,
			o.Status,
			o.CreatedAt.In(time.Local).Format("2006-01-02 15:04:05"),
			paidAt,
			o.PlatformTradeNo,
		}
		for col, v := range vals {
			cell, _ := excelize.CoordinatesToCellName(col+1, row)
			_ = xl.SetCellValue(sheet, cell, v)
		}
	}
	_ = xl.SetColWidth(sheet, "A", "A", 26)
	_ = xl.SetColWidth(sheet, "D", "D", 24)
	_ = xl.SetColWidth(sheet, "K", "L", 20)

	var buf bytes.Buffer
	if err := xl.Write(&buf); err != nil {
		response.ServerError(c, "生成订单导出文件失败")
		return
	}
	filename := fmt.Sprintf("订单导出_%s.xlsx", time.Now().In(time.Local).Format("20060102_1504"))
	escaped := url.QueryEscape(filename)
	contentType := "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"orders-export.xlsx\"; filename*=UTF-8''%s", escaped))
	if len(orders) >= maxOrderExportRows {
		c.Header("X-Export-Truncated", fmt.Sprintf("true; max=%d", maxOrderExportRows))
	}
	c.Data(http.StatusOK, contentType, buf.Bytes())
}

// yuanFloat 分 → 元（保留 2 位的浮点，供 Excel 数值列）。
func yuanFloat(cents int64) float64 {
	return float64(cents) / 100.0
}
