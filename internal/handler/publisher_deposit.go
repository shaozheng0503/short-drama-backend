package handler

import (
	"fmt"
	"strings"
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/payment"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GET /v1/publisher/deposit/account —— 押金账户概览
func (s *Server) publisherDepositAccount(c *gin.Context) {
	id := middleware.CurrentID(c)
	var d model.Distributor
	if err := s.db.Select("deposit_available_cents, deposit_frozen_cents, deposit_deducted_cents").First(&d, id).Error; err != nil {
		response.NotFound(c, "发行商不存在")
		return
	}
	response.OK(c, gin.H{
		"total_balance_cents":     d.DepositAvailableCents + d.DepositFrozenCents + d.DepositDeductedCents,
		"available_balance_cents": d.DepositAvailableCents,
		"frozen_balance_cents":    d.DepositFrozenCents,
		"deducted_balance_cents":  d.DepositDeductedCents,
	})
}

// POST /v1/publisher/deposit/recharge —— 发起押金充值
func (s *Server) publisherRecharge(c *gin.Context) {
	id := middleware.CurrentID(c)
	var req struct {
		AmountCents   int64  `json:"amount_cents" binding:"required"`
		PaymentMethod string `json:"payment_method"`
		PayScene      string `json:"pay_scene"` // app / wap，默认 wap（网页端）
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "amount_cents 必填")
		return
	}
	if req.AmountCents <= 0 {
		response.InvalidParam(c, "充值金额必须大于 0")
		return
	}
	if req.PaymentMethod == "" {
		req.PaymentMethod = "alipay"
	}
	if req.PayScene == "" {
		req.PayScene = "wap" // 默认网页 H5 支付
	}

	// dev 模式：直接充值成功
	if s.cfg.PaymentDevMode {
		err := s.db.Transaction(func(tx *gorm.DB) error {
			var d model.Distributor
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&d, id).Error; err != nil {
				return err
			}
			d.DepositAvailableCents += req.AmountCents
			if err := tx.Save(&d).Error; err != nil {
				return err
			}
			// 充值单
			rc := model.DistributorRecharge{
				RechargeNo:    fmt.Sprintf("RC%06d", time.Now().UnixMilli()%1000000),
				DistributorID: id,
				AmountCents:   req.AmountCents,
				PaymentMethod: req.PaymentMethod,
				Status:        "paid",
				PaidAt:        &[]time.Time{time.Now()}[0],
			}
			if err := tx.Create(&rc).Error; err != nil {
				return err
			}
			// 流水
			if err := s.recordDepositTx(tx, id, model.DepositTxRecharge, req.AmountCents, d.DepositAvailableCents, "recharge", rc.RechargeNo, "押金充值"); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			response.ServerError(c, "充值失败")
			return
		}
		response.OK(c, gin.H{"status": "paid", "message": "充值成功（dev 模式）"})
		return
	}

	// 生产模式：创建充值单，调支付 provider 拿 prepay 参数
	rc := model.DistributorRecharge{
		RechargeNo:    fmt.Sprintf("RC%06d", time.Now().UnixMilli()%1000000),
		DistributorID: id,
		AmountCents:   req.AmountCents,
		PaymentMethod: req.PaymentMethod,
		Status:        "pending",
		ExpiredAt:     &[]time.Time{time.Now().Add(30 * time.Minute)}[0],
	}
	if err := s.db.Create(&rc).Error; err != nil {
		response.ServerError(c, "创建充值单失败")
		return
	}

	// 调支付 provider 拿 prepay 参数，按前端传入的 pay_scene 决定走 wap(H5) 还是 app
	provider, err := s.payments.Get(req.PaymentMethod)
	if err != nil {
		response.Fail(c, response.CodeThirdPartyError, "支付渠道不可用: "+req.PaymentMethod)
		return
	}
	prepayParams, err := provider.Prepay(payment.PrepayInput{
		OrderNo:     rc.RechargeNo,
		AmountCents: rc.AmountCents,
		Subject:     "发行商押金充值",
		Scene:       req.PayScene,
		ExpireAt:    time.Now().Add(s.cfg.PaymentExpire),
	})
	if err != nil {
		s.db.Delete(&rc) // prepay 失败删掉充值单
		errMsg := err.Error()
		// 支付宝 wap 未签约时给出明确提示
		if req.PayScene == "wap" && (strings.Contains(errMsg, "insufficient-isv-permissions") || strings.Contains(errMsg, "ISV")) {
			response.Fail(c, response.CodeThirdPartyError, "支付宝尚未签约「手机网站支付」产品，请先在支付宝开放平台签约，或前端传 pay_scene=app 使用 App 支付")
			return
		}
		response.ServerError(c, "调起支付失败: "+errMsg)
		return
	}

	response.OK(c, gin.H{
		"recharge_no":    rc.RechargeNo,
		"amount_cents":   rc.AmountCents,
		"payment_method": rc.PaymentMethod,
		"status":         "pending",
		"expired_at":     rc.ExpiredAt,
		"pay_params":     prepayParams,
	})
}

// GET /v1/publisher/deposit/transactions —— 押金流水列表
func (s *Server) publisherDepositTransactions(c *gin.Context) {
	id := middleware.CurrentID(c)
	page, pageSize := paginate(c)
	q := s.db.Model(&model.DistributorDepositTransaction{}).Where("distributor_id = ?", id)
	if v := c.Query("type"); v != "" {
		q = q.Where("type = ?", v)
	}
	var total int64
	q.Count(&total)
	var items []model.DistributorDepositTransaction
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items)

	list := make([]gin.H, 0, len(items))
	for _, r := range items {
		list = append(list, gin.H{
			"id":                  r.ID,
			"type":                r.Type,
			"amount_cents":        r.AmountCents,
			"balance_after_cents": r.BalanceAfterCents,
			"related_business_no": r.RelatedBusinessNo,
			"remark":              r.Remark,
			"created_at":          r.CreatedAt,
		})
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}
