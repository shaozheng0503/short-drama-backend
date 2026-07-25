package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ============================================================
// 发行商 /publisher 端接口（0.15.0）
// ============================================================

// GET /v1/publisher/dashboard —— 工作台首页概览
func (s *Server) publisherDashboard(c *gin.Context) {
	id := middleware.CurrentID(c)
	var d model.Distributor
	if err := s.db.First(&d, id).Error; err != nil {
		response.NotFound(c, "发行商不存在")
		return
	}

	// 已认领剧集数（status=completed 的 application 关联的 distributor_drama）
	var claimedCount int64
	s.db.Model(&model.DistributorDrama{}).Where("distributor_id = ? AND status IN ?", id, []string{"authorized", "active"}).Count(&claimedCount)

	// 待审核认领数
	var pendingClaims int64
	s.db.Model(&model.DistributorApplication{}).Where("distributor_id = ? AND status IN ?", id, []string{model.ClaimReviewPending, model.ClaimContractPending}).Count(&pendingClaims)

	response.OK(c, gin.H{
		"verify_status":           d.VerifyStatus,
		"claimed_drama_count":     claimedCount,
		"deposit_available_cents": d.DepositAvailableCents,
		"deposit_frozen_cents":    d.DepositFrozenCents,
		"deposit_deducted_cents":  d.DepositDeductedCents,
		"withdrawable_cents":      d.BalanceCents,
		"pending_claim_count":     pendingClaims,
	})
}

// POST /v1/publisher/upload —— 上传认证材料/发票/附件（图片签名）
func (s *Server) publisherUpload(c *gin.Context) {
	if !s.cos.Configured() {
		response.Fail(c, response.CodeThirdPartyError, "COS 未配置")
		return
	}
	var req struct {
		Suffix string `json:"suffix" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "suffix 必填")
		return
	}
	id := middleware.CurrentID(c)
	key := fmt.Sprintf("publisher/%d/%d%s", id, time.Now().UnixMilli(), req.Suffix)
	url, expiresAt, requiredHeaders, err := s.cos.PresignedPUT(key)
	if err != nil {
		response.ServerError(c, "生成上传签名失败")
		return
	}
	hdrs := gin.H{}
	for k, v := range requiredHeaders {
		hdrs[k] = v
	}
	cdnDomain := s.cfg.COSCDNDomain
	fileURL := "https://" + cdnDomain + "/" + key
	if cdnDomain == "" {
		fileURL = url
	}
	response.OK(c, gin.H{
		"upload_url": url,
		"file_url":   fileURL,
		"key":        key,
		"headers":    hdrs,
		"expires":    expiresAt,
	})
}

// POST /v1/publisher/uploads/remittance —— 打款凭证上传签名
// 私有 ACL，下载走 presigned GET
func (s *Server) publisherRemittanceUploadSign(c *gin.Context) {
	if !s.cos.Configured() {
		response.Fail(c, response.CodeThirdPartyError, "COS 未配置")
		return
	}
	var req struct {
		Ext string `json:"ext"`
	}
	_ = c.ShouldBindJSON(&req)

	ext := strings.ToLower(strings.TrimPrefix(req.Ext, "."))
	if ext == "" {
		ext = "png"
	}
	allowedExts := map[string]bool{"png": true, "jpg": true, "jpeg": true, "pdf": true}
	if !allowedExts[ext] {
		response.InvalidParam(c, "ext 不允许，仅支持 png/jpg/jpeg/pdf")
		return
	}

	id := middleware.CurrentID(c)
	prefix := "settlement-remittance/" + time.Now().Format("2006/01/02")
	prefix += "/dist_" + itoa(id)
	key := prefix + "/" + randomToken(12) + "." + ext

	url, expiresAt, requiredHeaders, err := s.cos.PresignedPUTWithACL(key, "private")
	if err != nil {
		response.ServerError(c, "签名失败")
		return
	}
	hdrs := gin.H{}
	for k, v := range requiredHeaders {
		hdrs[k] = v
	}
	response.OK(c, gin.H{
		"upload_url": url,
		"key":        key,
		"headers":    hdrs,
		"expires":    expiresAt,
	})
}

// ============================================================
// 共用辅助函数
// ============================================================

// calcDepositAmount 计算保证金金额（首单：基础押金 + 平台加价）
func (s *Server) calcDepositAmount(drama model.Drama, platforms []string) int64 {
	base := s.depositBaseCents(drama)
	rateBP := 1500 // 15%
	n := len(platforms)
	return base * (10000 + int64(rateBP*(n-1))) / 10000
}

// calcAppendDepositAmount 计算追加平台的增量押金（不重新收基础押金，只按新增平台数 × 加价比例）
func (s *Server) calcAppendDepositAmount(drama model.Drama, newPlatformCount int) int64 {
	base := s.depositBaseCents(drama)
	rateBP := 1500 // 15%
	return base * int64(rateBP) * int64(newPlatformCount) / 10000
}

// depositBaseCents 返回基础押金（分），按剧集总时长分档：
// ≤25 分钟：400 元；26-35 分钟：500 元；>35 分钟：500 元
func (s *Server) depositBaseCents(drama model.Drama) int64 {
	minutes := s.dramaTotalMinutes(drama.ID)
	if minutes <= 25 {
		return 40000 // 400 元
	}
	return 50000 // 500 元（26 分钟及以上）
}

// dramaTotalMinutes 查剧集所有集数时长总和（分钟），用于保证金分档
func (s *Server) dramaTotalMinutes(dramaID uint64) int {
	var totalSeconds sql.NullInt64
	s.db.Model(&model.Episode{}).
		Where("drama_id = ? AND status = ?", dramaID, "published").
		Select("COALESCE(SUM(duration_seconds), 0)").
		Scan(&totalSeconds)
	if !totalSeconds.Valid {
		return 0
	}
	return int(totalSeconds.Int64 / 60)
}

// recordDepositTx 记录押金流水。返回错误以便事务回滚。
func (s *Server) recordDepositTx(tx *gorm.DB, distributorID uint64, txType string, amount int64, balanceAfter int64, relatedType string, relatedNo string, remark string) error {
	dt := model.DistributorDepositTransaction{
		DistributorID:     distributorID,
		Type:              txType,
		AmountCents:       amount,
		BalanceAfterCents: balanceAfter,
		RelatedType:       relatedType,
		RelatedBusinessNo: relatedNo,
		Remark:            remark,
	}
	return tx.Create(&dt).Error
}

// parsePlatforms 解析 JSON 平台数组
func parsePlatforms(jsonStr string) []string {
	var platforms []string
	json.Unmarshal([]byte(jsonStr), &platforms)
	return platforms
}

// platformsToJSON 平台数组转 JSON
func platformsToJSON(platforms []string) string {
	b, _ := json.Marshal(platforms)
	return string(b)
}

// distributorName 获取发行商显示名
func distributorName(d *model.Distributor) string {
	if d.OrgName != "" {
		return d.OrgName
	}
	if d.Nickname != "" {
		return d.Nickname
	}
	return d.Name
}
