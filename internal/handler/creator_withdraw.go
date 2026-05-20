package handler

import (
	"errors"
	"fmt"
	"log"
	"math/rand"
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type withdrawalRequest struct {
	AmountCents int64 `json:"amount_cents" binding:"required"`
}

func (s *Server) creatorCreateWithdrawal(c *gin.Context) {
	cid := middleware.CurrentID(c)
	if idem := c.GetHeader("Idempotency-Key"); idem != "" {
		log.Printf("[withdrawal] creator=%d idem=%s", cid, idem)
	}
	var req withdrawalRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.AmountCents <= 0 {
		response.InvalidParam(c, "amount_cents 必须为正整数")
		return
	}
	if req.AmountCents < s.cfg.MinWithdrawalCents {
		response.InvalidParam(c, fmt.Sprintf("最低提现门槛 %d 分", s.cfg.MinWithdrawalCents))
		return
	}

	// 事务里做：行锁 → 校验 → 扣 balance / 加 frozen → 写 withdrawal
	var result model.Withdrawal
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var creator model.Creator
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&creator, cid).Error; err != nil {
			return err
		}
		if creator.VerifyStatus != model.CreatorVerifyVerified {
			return errNotVerified
		}
		if creator.Status != model.StatusActive {
			return errAccountBanned
		}
		if req.AmountCents > creator.BalanceCents {
			return errAmountExceedsBalance
		}
		if creator.BankName == "" || creator.BankCardLast4 == "" {
			return errBankMissing
		}

		// 同时只允许一笔 pending
		var existingPending int64
		if err := tx.Model(&model.Withdrawal{}).
			Where("creator_id = ? AND status = ?", cid, model.WithdrawalStatusPending).
			Count(&existingPending).Error; err != nil {
			return err
		}
		if existingPending > 0 {
			return errPendingExists
		}

		// 转 balance → frozen
		if err := tx.Model(&model.Creator{}).Where("id = ?", cid).
			Updates(map[string]interface{}{
				"balance_cents": gorm.Expr("balance_cents - ?", req.AmountCents),
				"frozen_cents":  gorm.Expr("frozen_cents + ?", req.AmountCents),
			}).Error; err != nil {
			return err
		}

		w := model.Withdrawal{
			WithdrawalNo:       generateWithdrawalNo(),
			CreatorID:          cid,
			AmountCents:        req.AmountCents,
			BankNameSnapshot:   creator.BankName,
			BankCardNoSnapshot: "***" + creator.BankCardLast4,
			Status:             model.WithdrawalStatusPending,
		}
		if err := tx.Create(&w).Error; err != nil {
			return err
		}
		result = w
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, errNotVerified):
			response.Forbidden(c, "创作者未完成实名认证，无法提现")
		case errors.Is(err, errAccountBanned):
			response.Forbidden(c, "账号已被封禁")
		case errors.Is(err, errAmountExceedsBalance):
			response.InvalidParam(c, "提现金额超过可用余额")
		case errors.Is(err, errBankMissing):
			response.InvalidParam(c, "请先在「我的资料」中完善银行卡信息")
		case errors.Is(err, errPendingExists):
			response.Conflict(c, "存在 pending 提现申请，请先等待审核完成")
		default:
			log.Printf("[withdrawal] create err=%v", err)
			response.ServerError(c, "申请失败")
		}
		return
	}

	response.OK(c, withdrawalView(result))
}

func (s *Server) creatorListWithdrawals(c *gin.Context) {
	cid := middleware.CurrentID(c)
	page, pageSize := paginate(c)

	q := s.db.Model(&model.Withdrawal{}).Where("creator_id = ?", cid)
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	var total int64
	q.Count(&total)
	var items []model.Withdrawal
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items)

	list := make([]gin.H, 0, len(items))
	for _, w := range items {
		list = append(list, withdrawalView(w))
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

func withdrawalView(w model.Withdrawal) gin.H {
	return gin.H{
		"id":                    w.ID,
		"withdrawal_no":         w.WithdrawalNo,
		"amount_cents":          w.AmountCents,
		"bank_name_snapshot":    w.BankNameSnapshot,
		"bank_card_no_snapshot": w.BankCardNoSnapshot,
		"status":                w.Status,
		"remark":                w.Remark,
		"transaction_no":        w.TransactionNo,
		"reviewed_at":           w.ReviewedAt,
		"paid_at":               w.PaidAt,
		"created_at":            w.CreatedAt,
	}
}

func generateWithdrawalNo() string {
	now := time.Now()
	return fmt.Sprintf("WD%s%05d", now.Format("20060102150405"), rand.Intn(100000))
}

var (
	errNotVerified          = errors.New("not verified")
	errAccountBanned        = errors.New("account banned")
	errAmountExceedsBalance = errors.New("amount > balance")
	errBankMissing          = errors.New("bank missing")
	errPendingExists        = errors.New("pending exists")
)
