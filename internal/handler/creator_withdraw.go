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
	// 2026-07-03 改：drama_id 改为可空指针。
	// 流程图步骤 2 说"根据结算单，自行制作发票，上传后发起提现申请"——只有结算单维度。
	// 走法 A（传 invoice_id）时 drama_id 可空（结算单已固化 drama 范围）；
	// 走法 B（不传 invoice_id）时 drama_id 可空（一张结算单对多剧合并提现）。
	// 兼容：老接口 drama_id 必填行为由 `omitempty` + 事务内判断兼容。
	DramaID     *uint64 `json:"drama_id"`
	AmountCents int64   `json:"amount_cents" binding:"required"`
	// 2026-07-03 改：invoice_id 改为必填
	// 同事反馈：发票和提现是一体的，不能不传发票，不能单独审核发票
	// 财务审 withdrawal 时一并审发票（approve withdrawal → invoice.approved；reject → invoice.rejected 可重用）
	// 必填原因：
	//   · 财务需要看到「具体这张发票」才能对账
	//   · 一笔 withdrawal 必绑定一张 invoice，避免「空头提现」
	//   · 创作者先在 /v1/creator/invoices 上传发票，财务审核通过后再来提现
	InvoiceID *uint64 `json:"invoice_id" binding:"required"`
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
	if err := c.ShouldBindJSON(&req); err != nil || req.AmountCents <= 0 {
		response.InvalidParam(c, "amount_cents 必填且为正整数；drama_id 可空（按结算单维度提现时省略）")
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

		// 2026-07-03 改：drama_id 改为可空。流程图只有"结算单"维度，不强制"按剧提现"。
		// 走法 A：drama_id 为空时跳过 drama 校验（结算单已固化 drama 范围）
		// 走法 B：drama_id 为空时跳过 drama 校验（按结算单合并提现，drama 在结算单 items 里）
		if req.DramaID != nil {
			var drama model.Drama
			if err := tx.First(&drama, *req.DramaID).Error; err != nil {
				if isNotFound(err) {
					return errDramaNotFound
				}
				return err
			}
			if drama.CreatorID == nil || *drama.CreatorID != cid {
				return errDramaNotOwned
			}
		}

		// === 2026-07-03 改：invoice_id 必填，发票和提现一体 ===
		// 校验：
		//   1) 发票存在
		//   2) 发票属于本创作者
		//   3) 发票关联的结算单属于本创作者
		//   4) 结算单未作废
		//   5) settlement 包含本剧（drama_id 传了时校验）
		//   6) 提现金额不超发票剩余可提现余额（status≠rejected 的 withdrawal 不重复计）
		// 不再要求 invoice.status=approved —— 财务在审 withdrawal 时一并审发票
		// （approve withdrawal → invoice.approved；reject → invoice.rejected 可重用）
		if req.InvoiceID == nil {
			return errInvoiceIDRequired
		}
		var inv model.Invoice
		if err := tx.First(&inv, *req.InvoiceID).Error; err != nil {
			if isNotFound(err) {
				return errInvoiceNotFound
			}
			return err
		}
		if inv.CreatorID != cid {
			return errInvoiceNotOwned
		}
		var st model.Settlement
		if err := tx.First(&st, inv.SettlementID).Error; err != nil {
			return err
		}
		if st.CreatorID != cid {
			return errInvoiceSettlementMismatch
		}
		if st.Status != model.SettlementStatusInvoiced && st.Status != model.SettlementStatusPaid {
			return errInvoiceSettlementVoid
		}
		if req.DramaID != nil {
			var stItemCount int64
			if err := tx.Model(&model.SettlementItem{}).
				Where("settlement_id = ? AND drama_id = ?", st.ID, *req.DramaID).
				Count(&stItemCount).Error; err != nil {
				return err
			}
			if stItemCount == 0 {
				return errInvoiceSettlementMismatch
			}
		}
		var invoiceWithdrawn int64
		tx.Model(&model.Withdrawal{}).
			Where("invoice_id = ? AND status <> ?", *req.InvoiceID, model.WithdrawalStatusRejected).
			Select("COALESCE(SUM(amount_cents),0)").Scan(&invoiceWithdrawn)
		if req.AmountCents > inv.AmountCents-invoiceWithdrawn {
			return errAmountExceedsInvoiceBalance
		}

		dramaAvail := s.dramaWithdrawableCentsTx(tx, cid, req.DramaID, creator.BalanceCents)
		if req.AmountCents > dramaAvail {
			return errAmountExceedsDramaBalance
		}
		if req.AmountCents > creator.BalanceCents {
			return errAmountExceedsBalance
		}

		// 同结算单存在审核中的提现申请（drama_id 为空时不卡——一张结算单可有多笔不同金额提现）
		var existingPending int64
		pendingQ := tx.Model(&model.Withdrawal{}).
			Where("creator_id = ? AND status = ?", cid, model.WithdrawalStatusPending)
		if req.DramaID != nil {
			pendingQ = pendingQ.Where("drama_id = ?", *req.DramaID)
		}
		if err := pendingQ.Count(&existingPending).Error; err != nil {
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

		taxCents, netCents, _ := s.computeWithdrawalTax(creator, req.AmountCents)
		// 2026-07-03 改：drama_id 可空（按结算单维度合并提现），Withdrawal.DramaID 接受 *uint64
		var dramaID *uint64
		if req.DramaID != nil {
			d := *req.DramaID
			dramaID = &d
		}
		// 2026-07-03 改：invoice_id 必填。linkedInvoiceID 直接用 *req.InvoiceID
		// 财务审 withdrawal 时一并审发票
		w := model.Withdrawal{
			WithdrawalNo:        generateWithdrawalNo(),
			CreatorID:           cid,
			DramaID:             dramaID,
			AmountCents:         req.AmountCents,
			CreatorTypeSnapshot: creator.CreatorType,
			TransferType:        model.TransferTypeOf(creator.CreatorType),
			TaxCents:            taxCents,
			NetCents:            netCents,
			BankNameSnapshot:    creator.BankName,
			BankCardNoSnapshot:  "***" + creator.BankCardLast4,
			Status:              model.WithdrawalStatusPending,
			InvoiceID:           req.InvoiceID, // 必填，与发票一一对应
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
		case errors.Is(err, errInvoiceNotFound):
			response.NotFound(c, "发票不存在")
		case errors.Is(err, errInvoiceNotOwned):
			response.Forbidden(c, "无权使用该发票发起提现")
		case errors.Is(err, errInvoiceSettlementMismatch):
			response.InvalidParam(c, "发票与结算单/短剧不匹配")
		case errors.Is(err, errInvoiceSettlementVoid):
			response.InvalidParam(c, "该发票关联的结算单已作废，无法提现")
		case errors.Is(err, errAmountExceedsInvoiceBalance):
			response.InvalidParam(c, "提现金额超过该发票剩余可提现余额（同一张发票不能被多次提现到超额）")
		case errors.Is(err, errInvoiceIDRequired):
			response.InvalidParam(c, "invoice_id 必填（创作者必须先上传发票并通过财务审核）")
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
		"tax_cents":             w.TaxCents,
		"net_cents":             w.NetCents,
		"creator_type_snapshot": w.CreatorTypeSnapshot,
		"transfer_type":         w.TransferType,
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
	// === 2026-07-02 加：展示关联的发票和结算单号（财务/创作者看一笔提现对应哪张发票、哪张结算单）===
	if w.InvoiceID != nil {
		view["invoice_id"] = *w.InvoiceID
		var inv model.Invoice
		// 2026-07-03 改：发票和提现一体，发票状态不独立展示
		// 创作者/财务看提现时，发票信息只作为附件信息展示（no/amount/settlement_no）
		// 「发票审了没」看 withdrawal.status 即可（联动）
		if err := s.db.Select("invoice_no, settlement_id, amount_cents").First(&inv, *w.InvoiceID).Error; err == nil {
			view["invoice_no"] = inv.InvoiceNo
			view["invoice_amount_cents"] = inv.AmountCents
			view["settlement_id"] = inv.SettlementID
			var stNo string
			s.db.Model(&model.Settlement{}).Select("settlement_no").Where("id = ?", inv.SettlementID).Scan(&stNo)
			view["settlement_no"] = stNo
		}
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

func (s *Server) dramaWithdrawableCents(creatorID uint64, dramaID *uint64, accountBalance int64) int64 {
	return s.dramaWithdrawableCentsTx(s.db, creatorID, dramaID, accountBalance)
}

// dramaWithdrawableCentsTx 计算"可提现余额"。
// dramaID == nil 表示"全 creator 维度"（按结算单合并提现，对齐流程图步骤 2）。
// dramaID != nil 表示"按剧维度"（兼容老接口）。
func (s *Server) dramaWithdrawableCentsTx(tx *gorm.DB, creatorID uint64, dramaID *uint64, accountBalance int64) int64 {
	var income int64
	q := tx.Table("creator_stats_daily").Select("COALESCE(SUM(income_cents),0)").Where("creator_id = ?", creatorID)
	if dramaID != nil {
		q = q.Where("drama_id = ?", *dramaID)
	}
	q.Scan(&income)
	var withdrawn int64
	q2 := tx.Model(&model.Withdrawal{}).
		Select("COALESCE(SUM(amount_cents),0)").
		Where("creator_id = ? AND status IN ?", creatorID, activeWithdrawalStatuses)
	if dramaID != nil {
		q2 = q2.Where("drama_id = ?", *dramaID)
	}
	q2.Scan(&withdrawn)
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
	errAmountExceedsInvoiceBalance = errors.New("amount > invoice balance")
	errPendingExists           = errors.New("pending exists")
	errDramaNotFound           = errors.New("drama not found")
	errDramaNotOwned           = errors.New("drama not owned")
	errInvoiceNotFound         = errors.New("invoice not found")
	errInvoiceNotOwned         = errors.New("invoice not owned")
	// 2026-07-03 改：删 errInvoiceNotApproved / errInvoiceMetaMissing / errInvoiceTypeInvalid
	// （发票和提现一体，invoice_id 必填，不再有"未传 invoice_id 走 meta 分支"）
	// 加 errInvoiceIDRequired —— 必填校验
	errInvoiceIDRequired      = errors.New("invoice_id required")
	errInvoiceSettlementMismatch = errors.New("invoice settlement mismatch")
	errInvoiceSettlementVoid   = errors.New("invoice settlement void")
)
