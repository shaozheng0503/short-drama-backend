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
	// 2026-07-02 改：invoice_id 改为可选，对齐流程图步骤 2「用户根据结算单自开发票 → 上传 → 发起提现申请」。
	//   - 传 invoice_id：走"已审发票快通道"，校验 invoice.status=approved 且金额未超额
	//   - 不传 invoice_id：进入"pending invoice 队列"，财务在 review 提现时一并审发票
	InvoiceID uint64 `json:"invoice_id"`
	// InvoiceMeta 仅在不传 invoice_id 时使用，财务审 withdrawal 时凭这俩字段"开"一张 invoice 记录。
	InvoiceType string `json:"invoice_type"` // vat_special / vat_general / evat_special / evat_general
	ExternalNo  string `json:"external_no"`  // 发票号（创作者自开发票上的号码）
	// 创作者表示"我已上传发票文件，这里是 file_url/file_hash/file_size"——跟 /v1/creator/invoices 的入参一致
	FileURL  string `json:"file_url"`
	FileHash string `json:"file_hash"`
	FileSize int64  `json:"file_size"`
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

		// === 2026-07-02 改：发票从「强校验」改成「有就校验」 ===
		// 流程图步骤 2：用户先上传发票再发起提现，财务在审 withdrawal 时一并审发票
		// 兼容老用法：传 invoice_id 且 status=approved → 走快通道（防一张发票被多次提现到超额）
		if req.InvoiceID > 0 {
			var inv model.Invoice
			if err := tx.First(&inv, req.InvoiceID).Error; err != nil {
				if isNotFound(err) {
					return errInvoiceNotFound
				}
				return err
			}
			if inv.CreatorID != cid {
				return errInvoiceNotOwned
			}
			if inv.Status != model.InvoiceStatusApproved {
				return errInvoiceNotApproved
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
			// settlement 是按合同切的，可能跨多剧；要校验该结算单下确实有本剧的收入条目。
			var stItemCount int64
			if err := tx.Model(&model.SettlementItem{}).
				Where("settlement_id = ? AND drama_id = ?", st.ID, req.DramaID).
				Count(&stItemCount).Error; err != nil {
				return err
			}
			if stItemCount == 0 {
				return errInvoiceSettlementMismatch
			}
			// 累加：所有 status≠rejected 的提现申请对本发票的金额合计
			var invoiceWithdrawn int64
			tx.Model(&model.Withdrawal{}).
				Where("invoice_id = ? AND status <> ?", req.InvoiceID, model.WithdrawalStatusRejected).
				Select("COALESCE(SUM(amount_cents),0)").Scan(&invoiceWithdrawn)
			if req.AmountCents > inv.AmountCents-invoiceWithdrawn {
				return errAmountExceedsInvoiceBalance
			}
		} else {
			// 没传 invoice_id：流程图步骤 2 的另一种走法
			// ——创作者在发起提现时一并提交发票元数据，财务审 withdrawal 时一并审发票
			if req.InvoiceType == "" || req.FileURL == "" {
				return errInvoiceMetaMissing
			}
			switch req.InvoiceType {
			case model.InvoiceTypeVATSpecial, model.InvoiceTypeVATGeneral,
				model.InvoiceTypeEVATSpecial, model.InvoiceTypeEVATGeneral:
			default:
				return errInvoiceTypeInvalid
			}
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

		taxCents, netCents, _ := s.computeWithdrawalTax(creator, req.AmountCents)
		dramaID := req.DramaID
		// 2026-07-02 改：invoice_id 可选。如果创作者没传 invoice_id，
		// 自动创建一张 pending invoice（金额 = withdrawal amount）并关联上 —— 对齐流程图步骤 2
		var linkedInvoiceID *uint64
		if req.InvoiceID > 0 {
			id := req.InvoiceID
			linkedInvoiceID = &id
		} else {
			// 创建一个"待审"发票，等财务 review withdrawal 时一并审
			autoInvoice := model.Invoice{
				InvoiceNo:    generateInvoiceBizNo(),
				CreatorID:    cid,
				InvoiceType:  req.InvoiceType,
				ExternalNo:   req.ExternalNo,
				AmountCents:  req.AmountCents,
				FileURL:      req.FileURL,
				FileHash:     req.FileHash,
				FileSize:     req.FileSize,
				Status:       model.InvoiceStatusPending,
			}
			if err := tx.Create(&autoInvoice).Error; err != nil {
				return err
			}
			linkedInvoiceID = &autoInvoice.ID
		}
		w := model.Withdrawal{
			WithdrawalNo:        generateWithdrawalNo(),
			CreatorID:           cid,
			DramaID:             &dramaID,
			AmountCents:         req.AmountCents,
			CreatorTypeSnapshot: creator.CreatorType,
			TransferType:        model.TransferTypeOf(creator.CreatorType),
			TaxCents:            taxCents,
			NetCents:            netCents,
			BankNameSnapshot:    creator.BankName,
			BankCardNoSnapshot:  "***" + creator.BankCardLast4,
			Status:              model.WithdrawalStatusPending,
			InvoiceID:           linkedInvoiceID, // 2026-07-02 改：可能指向自动建的 pending 发票
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
		case errors.Is(err, errInvoiceNotApproved):
			response.InvalidParam(c, "该发票尚未通过审核，请等待财务审核通过后再提现")
		case errors.Is(err, errInvoiceSettlementMismatch):
			response.InvalidParam(c, "发票与结算单/短剧不匹配")
		case errors.Is(err, errInvoiceSettlementVoid):
			response.InvalidParam(c, "该发票关联的结算单已作废，无法提现")
		case errors.Is(err, errAmountExceedsInvoiceBalance):
			response.InvalidParam(c, "提现金额超过该发票剩余可提现余额（同一张发票不能被多次提现到超额）")
		case errors.Is(err, errInvoiceMetaMissing):
			response.InvalidParam(c, "未传 invoice_id 时需附带发票元数据：invoice_type、file_url 必填")
		case errors.Is(err, errInvoiceTypeInvalid):
			response.InvalidParam(c, "invoice_type 不合法，可选：vat_special / vat_general / evat_special / evat_general")
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
		if err := s.db.Select("invoice_no, settlement_id, amount_cents, status").First(&inv, *w.InvoiceID).Error; err == nil {
			view["invoice_no"] = inv.InvoiceNo
			view["invoice_amount_cents"] = inv.AmountCents
			view["invoice_status"] = inv.Status
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
	errAmountExceedsInvoiceBalance = errors.New("amount > invoice balance")
	errPendingExists           = errors.New("pending exists")
	errDramaNotFound           = errors.New("drama not found")
	errDramaNotOwned           = errors.New("drama not owned")
	errInvoiceNotFound         = errors.New("invoice not found")
	errInvoiceNotOwned         = errors.New("invoice not owned")
	errInvoiceNotApproved      = errors.New("invoice not approved")
	errInvoiceSettlementMismatch = errors.New("invoice settlement mismatch")
	errInvoiceSettlementVoid   = errors.New("invoice settlement void")
	// 2026-07-02 改：新增"未传 invoice_id 但 meta 不全"和"invoice_type 不合法"
	errInvoiceMetaMissing  = errors.New("invoice meta missing")
	errInvoiceTypeInvalid  = errors.New("invoice type invalid")
)
