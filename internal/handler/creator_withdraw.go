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
	DramaID     uint64 `json:"drama_id" binding:"required"`
	AmountCents int64  `json:"amount_cents" binding:"required"`
}

var activeWithdrawalStatuses = []string{
	model.WithdrawalStatusPending,
	model.WithdrawalStatusApproved,
	model.WithdrawalStatusPaid,
}

func (s *Server) creatorCreateWithdrawal(c *gin.Context) {
	cid := middleware.CurrentID(c)
	if idem := c.GetHeader("Idempotency-Key"); idem != "" {
		log.Printf("[withdrawal] creator=%d idem=%s", cid, idem)
	}
	var req withdrawalRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.AmountCents <= 0 || req.DramaID == 0 {
		response.InvalidParam(c, "drama_id 与 amount_cents 必填，且 amount_cents 必须为正整数")
		return
	}
	if req.AmountCents < s.cfg.MinWithdrawalCents {
		response.InvalidParam(c, fmt.Sprintf("提现金额不能低于 ¥%.2f", float64(s.cfg.MinWithdrawalCents)/100))
		return
	}

	var creator model.Creator
	if err := s.db.First(&creator, cid).Error; err != nil {
		response.ServerError(c, "查询创作者失败")
		return
	}
	if profile := checkCreatorWithdrawProfile(creator); !profile.OK {
		useForbidden := creator.VerifyStatus != model.CreatorVerifyVerified ||
			creator.Status != model.StatusActive
		respondWithdrawProfileBlock(c, profile, useForbidden)
		return
	}

	var result model.Withdrawal
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var creator model.Creator
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&creator, cid).Error; err != nil {
			return err
		}
		if profile := checkCreatorWithdrawProfile(creator); !profile.OK {
			return errWithdrawProfileBlocked
		}

		var drama model.Drama
		if err := tx.First(&drama, req.DramaID).Error; err != nil {
			if isNotFound(err) {
				return errDramaNotFound
			}
			return err
		}
		if drama.CreatorID == nil || *drama.CreatorID != cid {
			return errDramaNotOwned
		}

		dramaAvail := s.dramaWithdrawableCentsTx(tx, cid, req.DramaID, creator.BalanceCents)
		if req.AmountCents > dramaAvail {
			return errAmountExceedsDramaBalance
		}
		if req.AmountCents > creator.BalanceCents {
			return errAmountExceedsBalance
		}

		var existingPending int64
		if err := tx.Model(&model.Withdrawal{}).
			Where("creator_id = ? AND drama_id = ? AND status = ?", cid, req.DramaID, model.WithdrawalStatusPending).
			Count(&existingPending).Error; err != nil {
			return err
		}
		if existingPending > 0 {
			return errPendingExists
		}

		if err := tx.Model(&model.Creator{}).Where("id = ?", cid).
			Updates(map[string]interface{}{
				"balance_cents": gorm.Expr("balance_cents - ?", req.AmountCents),
				"frozen_cents":  gorm.Expr("frozen_cents + ?", req.AmountCents),
			}).Error; err != nil {
			return err
		}

		dramaID := req.DramaID
		w := model.Withdrawal{
			WithdrawalNo:       generateWithdrawalNo(),
			CreatorID:          cid,
			DramaID:            &dramaID,
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
		case errors.Is(err, errWithdrawProfileBlocked):
			if profile := checkCreatorWithdrawProfile(creator); !profile.OK {
				useForbidden := creator.VerifyStatus != model.CreatorVerifyVerified ||
					creator.Status != model.StatusActive
				respondWithdrawProfileBlock(c, profile, useForbidden)
				return
			}
			response.InvalidParam(c, "提现资料不完整，请前往「实名认证」补充信息")
		case errors.Is(err, errDramaNotFound):
			response.NotFound(c, "短剧不存在")
		case errors.Is(err, errDramaNotOwned):
			response.Forbidden(c, "无权对该短剧发起提现")
		case errors.Is(err, errAmountExceedsDramaBalance):
			response.InvalidParam(c, fmt.Sprintf("提现金额超过该剧可提现余额（¥%.2f）", float64(s.dramaWithdrawableCents(cid, req.DramaID, creator.BalanceCents))/100))
		case errors.Is(err, errAmountExceedsBalance):
			response.InvalidParam(c, fmt.Sprintf("提现金额超过账户可用余额（¥%.2f）", float64(creator.BalanceCents)/100))
		case errors.Is(err, errPendingExists):
			response.Conflict(c, "该剧存在审核中的提现申请，请等待处理")
		default:
			log.Printf("[withdrawal] create err=%v", err)
			response.ServerError(c, "申请失败")
		}
		return
	}

	response.OK(c, s.withdrawalView(result))
}

func (s *Server) creatorListWithdrawals(c *gin.Context) {
	cid := middleware.CurrentID(c)
	page, pageSize := paginate(c)

	q := s.db.Model(&model.Withdrawal{}).Where("creator_id = ?", cid)
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := parseUint(c.Query("drama_id")); v > 0 {
		q = q.Where("drama_id = ?", v)
	}
	var total int64
	q.Count(&total)
	var items []model.Withdrawal
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items)

	list := make([]gin.H, 0, len(items))
	for _, w := range items {
		list = append(list, s.withdrawalView(w))
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

func (s *Server) withdrawalView(w model.Withdrawal) gin.H {
	view := gin.H{
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
	if w.DramaID != nil {
		view["drama_id"] = *w.DramaID
		var title string
		s.db.Table("dramas").Select("title").Where("id = ?", *w.DramaID).Scan(&title)
		view["drama_title"] = title
	}
	return view
}

func (s *Server) dramaIncomeCents(creatorID, dramaID uint64) int64 {
	var income int64
	s.db.Table("creator_stats_daily").
		Select("COALESCE(SUM(income_cents),0)").
		Where("creator_id = ? AND drama_id = ?", creatorID, dramaID).
		Scan(&income)
	return income
}

func (s *Server) dramaWithdrawnCents(creatorID, dramaID uint64) int64 {
	var withdrawn int64
	s.db.Model(&model.Withdrawal{}).
		Select("COALESCE(SUM(amount_cents),0)").
		Where("creator_id = ? AND drama_id = ? AND status IN ?", creatorID, dramaID, activeWithdrawalStatuses).
		Scan(&withdrawn)
	return withdrawn
}

func (s *Server) dramaWithdrawableCents(creatorID, dramaID uint64, accountBalance int64) int64 {
	return s.dramaWithdrawableCentsTx(s.db, creatorID, dramaID, accountBalance)
}

func (s *Server) dramaWithdrawableCentsTx(tx *gorm.DB, creatorID, dramaID uint64, accountBalance int64) int64 {
	var income int64
	tx.Table("creator_stats_daily").
		Select("COALESCE(SUM(income_cents),0)").
		Where("creator_id = ? AND drama_id = ?", creatorID, dramaID).
		Scan(&income)
	var withdrawn int64
	tx.Model(&model.Withdrawal{}).
		Select("COALESCE(SUM(amount_cents),0)").
		Where("creator_id = ? AND drama_id = ? AND status IN ?", creatorID, dramaID, activeWithdrawalStatuses).
		Scan(&withdrawn)
	avail := income - withdrawn
	if avail < 0 {
		avail = 0
	}
	if avail > accountBalance {
		avail = accountBalance
	}
	return avail
}

func (s *Server) batchDramaWithdrawnCents(creatorID uint64, dramaIDs []uint64) map[uint64]int64 {
	out := map[uint64]int64{}
	if len(dramaIDs) == 0 {
		return out
	}
	var rows []struct {
		DramaID uint64
		Amt     int64 `gorm:"column:amt"`
	}
	s.db.Model(&model.Withdrawal{}).
		Select("drama_id, COALESCE(SUM(amount_cents),0) as amt").
		Where("creator_id = ? AND drama_id IN ? AND status IN ?", creatorID, dramaIDs, activeWithdrawalStatuses).
		Group("drama_id").Scan(&rows)
	for _, r := range rows {
		out[r.DramaID] = r.Amt
	}
	return out
}

func (s *Server) batchDramaPending(creatorID uint64, dramaIDs []uint64) map[uint64]bool {
	out := map[uint64]bool{}
	if len(dramaIDs) == 0 {
		return out
	}
	var rows []struct {
		DramaID uint64
	}
	s.db.Model(&model.Withdrawal{}).
		Select("drama_id").
		Where("creator_id = ? AND drama_id IN ? AND status = ?", creatorID, dramaIDs, model.WithdrawalStatusPending).
		Scan(&rows)
	for _, r := range rows {
		out[r.DramaID] = true
	}
	return out
}

func generateWithdrawalNo() string {
	now := time.Now()
	return fmt.Sprintf("WD%s%05d", now.Format("20060102150405"), rand.Intn(100000))
}

var (
	errWithdrawProfileBlocked  = errors.New("withdraw profile blocked")
	errAmountExceedsBalance    = errors.New("amount > balance")
	errAmountExceedsDramaBalance = errors.New("amount > drama balance")
	errPendingExists           = errors.New("pending exists")
	errDramaNotFound           = errors.New("drama not found")
	errDramaNotOwned           = errors.New("drama not owned")
)
