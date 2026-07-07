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
	// 2026-07-07 改（邱嘉诚 7/7 反馈）：发票和提现合并，每次提现上传一张发票，全额提现。
	// 不再单独调 /invoices 接口，发票在提现事务内自动创建。
	// 不再支持结算单多次提现——一张结算单只允许一笔 pending/approved 的提现。
	SettlementID      uint64 `json:"settlement_id" binding:"required"`
	AmountCents       int64  `json:"amount_cents"` // 可选，不传则用 settlement.NetCents（全额提现）
	InvoiceFileURL    string `json:"invoice_file_url" binding:"required"`
	InvoiceType       string `json:"invoice_type" binding:"required"`
	InvoiceExternalNo string `json:"invoice_external_no"`
	InvoiceFileHash   string `json:"invoice_file_hash"`
	InvoiceFileSize   int64  `json:"invoice_file_size"`
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
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法：settlement_id / invoice_file_url / invoice_type 必填，amount_cents 可选（不传则全额提现）")
		return
	}
	// 校验发票类型
	switch req.InvoiceType {
	case model.InvoiceTypeVATSpecial, model.InvoiceTypeVATGeneral,
		model.InvoiceTypeEVATSpecial, model.InvoiceTypeEVATGeneral:
	default:
		response.InvalidParam(c, "invoice_type 不合法（可选值：evat_special / evat_general / vat_special / vat_general）")
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
	var newInvoice model.Invoice
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var creator model.Creator
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&creator, cid).Error; err != nil {
			return err
		}
		if profile := checkCreatorWithdrawProfile(creator); !profile.OK {
			return errWithdrawProfileBlocked
		}

		// === 2026-07-07 改：按结算单维度，提现时自动创建发票 ===
		// 校验结算单
		var st model.Settlement
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&st, req.SettlementID).Error; err != nil {
			if isNotFound(err) {
				return errSettlementNotFound
			}
			return err
		}
		if st.CreatorID != cid {
			return errSettlementNotOwned
		}
		if st.Status == model.SettlementStatusPaid || st.Status == model.SettlementStatusVoid {
			return errSettlementClosed
		}

		// 校验该结算单没有 pending/approved 的提现（暂不支持多次提现）
		var existingActive int64
		tx.Model(&model.Withdrawal{}).
			Where("invoice_id IN (?) AND status IN ?",
				tx.Model(&model.Invoice{}).Select("id").Where("settlement_id = ?", req.SettlementID),
				[]string{model.WithdrawalStatusPending, model.WithdrawalStatusApproved},
			).Count(&existingActive)
		if existingActive > 0 {
			return errSettlementHasWithdrawal
		}

		// 提现金额 = settlement.NetCents（全额提现）
		amount := st.NetCents
		if req.AmountCents > 0 && req.AmountCents != amount {
			return errAmountMustFull
		}
		if amount < s.cfg.MinWithdrawalCents {
			return errAmountTooSmall
		}
		if amount > creator.BalanceCents {
			return errAmountExceedsBalance
		}
		dramaAvail := s.dramaWithdrawableCentsTx(tx, cid, nil, creator.BalanceCents)
		if amount > dramaAvail {
			return errAmountExceedsDramaBalance
		}

		// 创建发票（金额 = 提现金额 = 结算单净额）
		inv := model.Invoice{
			InvoiceNo:    generateInvoiceBizNo(),
			SettlementID: req.SettlementID,
			CreatorID:    cid,
			InvoiceType:  req.InvoiceType,
			ExternalNo:   req.InvoiceExternalNo,
			AmountCents:  amount,
			FileURL:      req.InvoiceFileURL,
			FileHash:     req.InvoiceFileHash,
			FileSize:     req.InvoiceFileSize,
			Status:       model.InvoiceStatusPending,
		}
		if err := tx.Create(&inv).Error; err != nil {
			return err
		}
		newInvoice = inv

		// settlement open → invoiced
		if st.Status == model.SettlementStatusOpen {
			if err := tx.Model(&st).Update("status", model.SettlementStatusInvoiced).Error; err != nil {
				return err
			}
		}

		// 扣 balance，加 frozen
		if err := tx.Model(&model.Creator{}).Where("id = ?", cid).
			Updates(map[string]interface{}{
				"balance_cents": gorm.Expr("balance_cents - ?", amount),
				"frozen_cents":  gorm.Expr("frozen_cents + ?", amount),
			}).Error; err != nil {
			return err
		}

		// 个税
		taxCents, netCents, _ := s.computeWithdrawalTax(creator, amount)

		// 创建提现单（drama_id = nil，按结算单维度）
		w := model.Withdrawal{
			WithdrawalNo:        generateWithdrawalNo(),
			CreatorID:           cid,
			DramaID:             nil,
			AmountCents:         amount,
			CreatorTypeSnapshot: creator.CreatorType,
			TransferType:        model.TransferTypeOf(creator.CreatorType),
			TaxCents:            taxCents,
			NetCents:            netCents,
			BankNameSnapshot:    creator.BankName,
			BankCardNoSnapshot:  "***" + creator.BankCardLast4,
			Status:              model.WithdrawalStatusPending,
			InvoiceID:           &inv.ID,
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
		case errors.Is(err, errSettlementNotFound):
			response.NotFound(c, "结算单不存在")
		case errors.Is(err, errSettlementNotOwned):
			response.Forbidden(c, "无权对该结算单发起提现")
		case errors.Is(err, errSettlementClosed):
			response.Conflict(c, "该结算单已结清或已作废，无法提现")
		case errors.Is(err, errSettlementHasWithdrawal):
			response.Conflict(c, "该结算单已有审核中或已通过的提现申请，请等待处理")
		case errors.Is(err, errAmountMustFull):
			response.InvalidParam(c, "当前只支持全额提现（amount_cents 必须等于结算单实收金额，或不传由系统自动填）")
		case errors.Is(err, errAmountTooSmall):
			response.InvalidParam(c, fmt.Sprintf("结算单实收金额低于最低提现额 ¥%.2f，无法提现", float64(s.cfg.MinWithdrawalCents)/100))
		case errors.Is(err, errAmountExceedsDramaBalance):
			response.InvalidParam(c, fmt.Sprintf("提现金额超过可提现余额（¥%.2f）", float64(s.dramaWithdrawableCents(cid, nil, creator.BalanceCents))/100))
		case errors.Is(err, errAmountExceedsBalance):
			response.InvalidParam(c, fmt.Sprintf("提现金额超过账户可用余额（¥%.2f）", float64(creator.BalanceCents)/100))
		default:
			log.Printf("[withdrawal] create err=%v", err)
			response.ServerError(c, "申请失败")
		}
		return
	}

	response.OK(c, s.withdrawalView(result))

	// 时间线
	actorID := cid
	s.recordTransition("invoice", newInvoice.ID, "", model.InvoiceStatusPending, "creator", &actorID, "创作者提现时上传发票", map[string]interface{}{
		"invoice_no":    newInvoice.InvoiceNo,
		"amount_cents":  newInvoice.AmountCents,
		"settlement_id": newInvoice.SettlementID,
	})
	s.recordTransition("withdrawal", result.ID, "", model.WithdrawalStatusPending, "creator", &actorID, "创作者发起提现申请", map[string]interface{}{
		"amount_cents":  result.AmountCents,
		"withdrawal_no": result.WithdrawalNo,
		"invoice_id":    newInvoice.ID,
		"settlement_id": req.SettlementID,
	})
	if newInvoice.SettlementID > 0 {
		var stNow model.Settlement
		if err := s.db.First(&stNow, newInvoice.SettlementID).Error; err == nil {
			s.recordTransition("settlement", newInvoice.SettlementID, stNow.Status, stNow.Status, "creator", &actorID, "创作者基于该结算单发起提现", map[string]interface{}{
				"withdrawal_id": result.ID,
				"invoice_id":    newInvoice.ID,
			})
		}
	}
}

// creatorGetWithdrawal —— GET /v1/creator/withdrawals/:id
// 提现记录详情：提现单 + 关联发票 + 关联结算单
func (s *Server) creatorGetWithdrawal(c *gin.Context) {
	cid := middleware.CurrentID(c)
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var w model.Withdrawal
	if err := s.db.Where("id = ? AND creator_id = ?", id, cid).First(&w).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "提现记录不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	response.OK(c, s.withdrawalDetailView(w))
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

// withdrawalDetailView —— 提现详情视图（提现单 + 关联发票完整信息 + 关联结算单信息）
func (s *Server) withdrawalDetailView(w model.Withdrawal) gin.H {
	view := s.withdrawalView(w)

	// 关联发票完整信息
	if w.InvoiceID != nil {
		var inv model.Invoice
		if err := s.db.First(&inv, *w.InvoiceID).Error; err == nil {
			view["invoice"] = gin.H{
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
		}
	}

	// 关联结算单信息
	if w.InvoiceID != nil {
		var inv model.Invoice
		if err := s.db.Select("settlement_id").First(&inv, *w.InvoiceID).Error; err == nil && inv.SettlementID > 0 {
			var st model.Settlement
			if err := s.db.First(&st, inv.SettlementID).Error; err == nil {
				view["settlement"] = gin.H{
					"id":             st.ID,
					"settlement_no":  st.SettlementNo,
					"period":         st.Period,
					"cycle_key":      st.CycleKey,
					"period_range":   st.PeriodRange,
					"gross_cents":    st.GrossCents,
					"platform_cents": st.PlatformCents,
					"net_cents":      st.NetCents,
					"status":         st.Status,
					"created_at":     st.CreatedAt,
				}
			}
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
	errInvoiceIDRequired      = errors.New("invoice_id required")
	errInvoiceSettlementMismatch = errors.New("invoice settlement mismatch")
	errInvoiceSettlementVoid   = errors.New("invoice settlement void")
	// 2026-07-07 改：发票提现合并后新增的错误
	errSettlementNotFound     = errors.New("settlement not found")
	errSettlementNotOwned     = errors.New("settlement not owned")
	errSettlementClosed       = errors.New("settlement closed")
	errSettlementHasWithdrawal = errors.New("settlement has active withdrawal")
	errAmountMustFull         = errors.New("amount must be full settlement")
	errAmountTooSmall         = errors.New("amount too small")
)
