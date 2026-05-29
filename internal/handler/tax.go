package handler

import (
	"math"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

// computeWithdrawalTax 计算某次提现的代扣个税与实际到账。
//   - 机构创作者（开票）：不扣税，tax=0。
//   - 个人创作者：按 tax_brackets 阶梯（速算扣除法）计算；无配置档命中=不扣税。
//
// 返回 (税额, 到账, 命中的档ID 0=无)。金额单位均为分。
func (s *Server) computeWithdrawalTax(creator model.Creator, amountCents int64) (taxCents, netCents int64, bracketID uint64) {
	if creator.CreatorType == model.CreatorTypeOrganization {
		return 0, amountCents, 0
	}
	var b model.TaxBracket
	// 命中：min<=金额，且（max=0 无上限 或 金额<max），active。取符合的最高档（min 最大）。
	err := s.db.Where("status = ? AND min_cents <= ? AND (max_cents = 0 OR ? < max_cents)",
		model.StatusActive, amountCents, amountCents).
		Order("min_cents desc").First(&b).Error
	if err != nil {
		return 0, amountCents, 0
	}
	tax := int64(math.Round(float64(amountCents)*float64(b.RateBP)/float64(model.ShareRatioBPFull))) - b.QuickDeductCents
	if tax < 0 {
		tax = 0
	}
	if tax > amountCents {
		tax = amountCents
	}
	return tax, amountCents - tax, b.ID
}

// creatorWithdrawTaxPreview —— GET /v1/creator/withdrawals/tax-preview?amount_cents=...
// 提现前预览：给定金额返回总额/代扣个税/实际到账，便于前端展示。
func (s *Server) creatorWithdrawTaxPreview(c *gin.Context) {
	cid := middleware.CurrentID(c)
	amount := int64(parseUint(c.Query("amount_cents")))
	if amount <= 0 {
		response.InvalidParam(c, "amount_cents 必须为正整数")
		return
	}
	var creator model.Creator
	if err := s.db.First(&creator, cid).Error; err != nil {
		response.ServerError(c, "查询创作者失败")
		return
	}
	tax, net, bid := s.computeWithdrawalTax(creator, amount)
	response.OK(c, gin.H{
		"creator_type": creator.CreatorType,
		"amount_cents": amount,
		"tax_cents":    tax,
		"net_cents":    net,
		"bracket_id":   bid,
		"note":         "机构创作者开票不扣个税；个人创作者按阶梯代扣。阶梯未配置时不扣税。",
	})
}

// === 管理端：个税阶梯配置 ===

func taxBracketView(b model.TaxBracket) gin.H {
	return gin.H{
		"id":                 b.ID,
		"min_cents":          b.MinCents,
		"max_cents":          b.MaxCents,
		"rate_bp":            b.RateBP,
		"quick_deduct_cents": b.QuickDeductCents,
		"sort_order":         b.SortOrder,
		"status":             b.Status,
	}
}

func (s *Server) adminListTaxBrackets(c *gin.Context) {
	var items []model.TaxBracket
	if err := s.db.Order("min_cents asc, id asc").Find(&items).Error; err != nil {
		response.ServerError(c, "查询个税阶梯失败")
		return
	}
	views := make([]gin.H, 0, len(items))
	for _, b := range items {
		views = append(views, taxBracketView(b))
	}
	response.OK(c, gin.H{
		"list":  views,
		"total": int64(len(views)),
		"note":  "速算扣除法：tax = round(金额×rate_bp/10000) - quick_deduct_cents，最低 0。数字由财务落实，空配置=不扣税。",
	})
}

type taxBracketUpsertRequest struct {
	MinCents         *int64  `json:"min_cents"`
	MaxCents         *int64  `json:"max_cents"`
	RateBP           *int    `json:"rate_bp"`
	QuickDeductCents *int64  `json:"quick_deduct_cents"`
	SortOrder        *int    `json:"sort_order"`
	Status           *string `json:"status"`
}

func validTaxBracket(minCents, maxCents int64, rateBP int, quick int64) string {
	if minCents < 0 || maxCents < 0 || quick < 0 {
		return "金额字段不能为负"
	}
	if maxCents != 0 && maxCents <= minCents {
		return "max_cents 必须大于 min_cents（0 表示无上限）"
	}
	if rateBP < 0 || rateBP > model.ShareRatioBPFull {
		return "rate_bp 须在 0~10000 之间"
	}
	return ""
}

func (s *Server) adminCreateTaxBracket(c *gin.Context) {
	var req taxBracketUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}
	b := model.TaxBracket{Status: model.StatusActive}
	if req.MinCents != nil {
		b.MinCents = *req.MinCents
	}
	if req.MaxCents != nil {
		b.MaxCents = *req.MaxCents
	}
	if req.RateBP != nil {
		b.RateBP = *req.RateBP
	}
	if req.QuickDeductCents != nil {
		b.QuickDeductCents = *req.QuickDeductCents
	}
	if req.SortOrder != nil {
		b.SortOrder = *req.SortOrder
	}
	if req.Status != nil && *req.Status != "" {
		b.Status = *req.Status
	}
	if msg := validTaxBracket(b.MinCents, b.MaxCents, b.RateBP, b.QuickDeductCents); msg != "" {
		response.InvalidParam(c, msg)
		return
	}
	if err := s.db.Create(&b).Error; err != nil {
		response.ServerError(c, "创建个税阶梯失败")
		return
	}
	response.OK(c, taxBracketView(b))
}

func (s *Server) adminUpdateTaxBracket(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var b model.TaxBracket
	if err := s.db.First(&b, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "个税阶梯不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	var req taxBracketUpsertRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}
	if req.MinCents != nil {
		b.MinCents = *req.MinCents
	}
	if req.MaxCents != nil {
		b.MaxCents = *req.MaxCents
	}
	if req.RateBP != nil {
		b.RateBP = *req.RateBP
	}
	if req.QuickDeductCents != nil {
		b.QuickDeductCents = *req.QuickDeductCents
	}
	if req.SortOrder != nil {
		b.SortOrder = *req.SortOrder
	}
	if req.Status != nil && *req.Status != "" {
		b.Status = *req.Status
	}
	if msg := validTaxBracket(b.MinCents, b.MaxCents, b.RateBP, b.QuickDeductCents); msg != "" {
		response.InvalidParam(c, msg)
		return
	}
	if err := s.db.Save(&b).Error; err != nil {
		response.ServerError(c, "更新个税阶梯失败")
		return
	}
	response.OK(c, taxBracketView(b))
}

func (s *Server) adminDeleteTaxBracket(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	if err := s.db.Delete(&model.TaxBracket{}, id).Error; err != nil {
		response.ServerError(c, "删除个税阶梯失败")
		return
	}
	response.OK(c, gin.H{"id": id, "deleted": true})
}
