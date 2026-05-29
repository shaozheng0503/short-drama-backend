package handler

import (
	"fmt"
	"strings"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	shareTypePureShare      = "pure_share"
	shareTypePureShareLabel = "纯分成合同"
	withdrawActionLabel     = "立即提现"
)

type settlementDramaRow struct {
	model.Drama
	IncomeCents int64 `gorm:"column:income_cents"`
}

// creatorSettlementSummary —— GET /v1/creator/settlement/summary
func (s *Server) creatorSettlementSummary(c *gin.Context) {
	cid := middleware.CurrentID(c)
	page, pageSize := paginate(c)

	var creator model.Creator
	if err := s.db.First(&creator, cid).Error; err != nil {
		response.ServerError(c, "查询创作者失败")
		return
	}

	attr, attrLabel := contractAttribute(creator.CreatorType)
	profileCheck := checkCreatorWithdrawProfile(creator)

	base := s.settlementDramaBaseQuery(cid)
	if kw := strings.TrimSpace(c.Query("title")); kw != "" {
		base = base.Where("d.title ILIKE ?", "%"+kw+"%")
	}

	var total int64
	if err := base.Count(&total).Error; err != nil {
		response.ServerError(c, "查询短剧失败")
		return
	}

	var rows []settlementDramaRow
	if err := base.
		Select(`d.*, COALESCE(inc.income_cents, 0) AS income_cents`).
		Order("income_cents desc, d.updated_at desc").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Scan(&rows).Error; err != nil {
		response.ServerError(c, "查询短剧失败")
		return
	}

	dramaIDs := make([]uint64, 0, len(rows))
	for _, row := range rows {
		dramaIDs = append(dramaIDs, row.ID)
	}
	withdrawnByDrama := s.batchDramaWithdrawnCents(cid, dramaIDs)
	pendingByDrama := s.batchDramaPending(cid, dramaIDs)

	list := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		withdrawn := withdrawnByDrama[row.ID]
		withdrawable := row.IncomeCents - withdrawn
		if withdrawable < 0 {
			withdrawable = 0
		}
		if withdrawable > creator.BalanceCents {
			withdrawable = creator.BalanceCents
		}

		enabled, hint, missing := evaluateDramaWithdrawAction(
			profileCheck, pendingByDrama[row.ID], withdrawable, s.cfg.MinWithdrawalCents,
		)
		list = append(list, gin.H{
			"drama_id":                 row.ID,
			"drama_title":              row.Title,
			"contract_attribute":       attr,
			"contract_attribute_label": attrLabel,
			"share_type":               shareTypePureShare,
			"share_type_label":         shareTypePureShareLabel,
			"income_cents":             row.IncomeCents,
			"withdrawable_cents":       withdrawable,
			"withdrawn_cents":          withdrawn,
			"action":                   dramaWithdrawAction(row.ID, enabled, hint, withdrawable, missing),
		})
	}

	summary := gin.H{
		"total_income_cents":   creator.TotalIncomeCents,
		"balance_cents":        creator.BalanceCents,
		"min_withdrawal_cents": s.cfg.MinWithdrawalCents,
	}
	if !profileCheck.OK {
		summary["withdraw_hint"] = profileCheck.Hint
		if len(profileCheck.MissingFields) > 0 {
			summary["missing_fields"] = profileCheck.MissingFields
		}
	}
	resp := pageResp(list, page, pageSize, total)
	resp["summary"] = summary
	response.OK(c, resp)
}

func (s *Server) settlementDramaBaseQuery(cid uint64) *gorm.DB {
	return s.db.Table("dramas AS d").
		Joins(`LEFT JOIN (
			SELECT drama_id, SUM(income_cents) AS income_cents
			FROM creator_stats_daily
			WHERE creator_id = ?
			GROUP BY drama_id
		) AS inc ON inc.drama_id = d.id`, cid).
		Where("d.creator_id = ?", cid).
		Where(`(
			COALESCE(inc.income_cents, 0) > 0
			OR EXISTS (
				SELECT 1 FROM contracts c
				WHERE c.creator_id = ? AND c.drama_id = d.id
			)
		)`, cid)
}

func dramaWithdrawAction(dramaID uint64, enabled bool, hint string, amountCents int64, missing []string) gin.H {
	amount := int64(0)
	if enabled {
		amount = amountCents
	}
	action := gin.H{
		"type":         "withdraw",
		"label":        withdrawActionLabel,
		"enabled":      enabled,
		"drama_id":     dramaID,
		"amount_cents": amount,
		"hint":         hint,
	}
	if len(missing) > 0 {
		action["missing_fields"] = missing
	}
	return action
}

func contractAttribute(creatorType string) (code, label string) {
	if creatorType == model.CreatorTypeOrganization {
		return "public", "对公"
	}
	return "private", "对私"
}

func evaluateDramaWithdrawAction(profile withdrawProfileCheck, hasPending bool, withdrawable, minCents int64) (bool, string, []string) {
	if !profile.OK {
		return false, profile.Hint, profile.MissingFields
	}
	if hasPending {
		return false, "该剧存在审核中的提现申请，请等待处理", nil
	}
	if withdrawable < minCents {
		return false, fmt.Sprintf(
			"该剧可提现 ¥%.2f，未达到最低提现门槛 ¥%.2f",
			float64(withdrawable)/100, float64(minCents)/100,
		), nil
	}
	return true, "", nil
}
