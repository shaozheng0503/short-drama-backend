package handler

import (
	"fmt"
	"math/rand"
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

// generateContractNo 生成合同编号，与订单 / 提现编号同风格。
func generateContractNo() string {
	now := time.Now()
	return fmt.Sprintf("CT%s%05d", now.Format("20060102150405"), rand.Intn(100000))
}

func contractView(ct model.Contract, dramaTitle string) gin.H {
	return gin.H{
		"id":            ct.ID,
		"creator_id":    ct.CreatorID,
		"drama_id":      ct.DramaID,
		"drama_title":   dramaTitle,
		"contract_no":   ct.ContractNo,
		"esign_flow_id": ct.EsignFlowID,
		"file_url":      ct.FileURL,
		"has_scan_file": ct.ScanFileKey != "", // 是否已上传签署后扫描件；下载走 GET .../contracts/:id/scan
		"status":        ct.Status,
		"created_at":    ct.CreatedAt,
		"updated_at":    ct.UpdatedAt,
	}
}

func (s *Server) attachDramaTitles(contracts []model.Contract) map[uint64]string {
	dramaIDs := make([]uint64, 0)
	seen := map[uint64]bool{}
	for _, ct := range contracts {
		if ct.DramaID != nil && !seen[*ct.DramaID] {
			dramaIDs = append(dramaIDs, *ct.DramaID)
			seen[*ct.DramaID] = true
		}
	}
	titles := map[uint64]string{}
	if len(dramaIDs) > 0 {
		var rows []struct {
			ID    uint64
			Title string
		}
		s.db.Table("dramas").Select("id, title").Where("id IN ?", dramaIDs).Scan(&rows)
		for _, r := range rows {
			titles[r.ID] = r.Title
		}
	}
	return titles
}

// === 创作者端 ===

func (s *Server) creatorListContracts(c *gin.Context) {
	cid := middleware.CurrentID(c)
	page, pageSize := paginate(c)
	q := s.db.Model(&model.Contract{}).Where("creator_id = ?", cid)
	var total int64
	q.Count(&total)
	var items []model.Contract
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items)
	titles := s.attachDramaTitles(items)
	list := make([]gin.H, 0, len(items))
	for _, ct := range items {
		title := ""
		if ct.DramaID != nil {
			title = titles[*ct.DramaID]
		}
		list = append(list, contractView(ct, title))
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

func (s *Server) creatorGetContract(c *gin.Context) {
	cid := middleware.CurrentID(c)
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var ct model.Contract
	if err := s.db.First(&ct, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "合同不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	if ct.CreatorID != cid {
		response.Forbidden(c, "合同不属于当前创作者")
		return
	}
	title := ""
	if ct.DramaID != nil {
		var d model.Drama
		if err := s.db.Select("title").First(&d, *ct.DramaID).Error; err == nil {
			title = d.Title
		}
	}
	response.OK(c, contractView(ct, title))
}

// === 管理中台 ===

func (s *Server) adminListContracts(c *gin.Context) {
	page, pageSize := paginate(c)
	q := s.db.Model(&model.Contract{})
	if v := parseUint(c.Query("creator_id")); v > 0 {
		q = q.Where("creator_id = ?", v)
	}
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	var total int64
	q.Count(&total)
	var items []model.Contract
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items)
	titles := s.attachDramaTitles(items)
	list := make([]gin.H, 0, len(items))
	for _, ct := range items {
		title := ""
		if ct.DramaID != nil {
			title = titles[*ct.DramaID]
		}
		list = append(list, contractView(ct, title))
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

type adminCreateContractRequest struct {
	CreatorID  uint64  `json:"creator_id" binding:"required"`
	DramaID    *uint64 `json:"drama_id"`
	ContractNo string  `json:"contract_no" binding:"required"`
	FileURL    string  `json:"file_url"`
}

func (s *Server) adminCreateContract(c *gin.Context) {
	var req adminCreateContractRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "creator_id 与 contract_no 必填")
		return
	}
	// 校验 creator / drama 存在
	var cnt int64
	if err := s.db.Model(&model.Creator{}).Where("id = ?", req.CreatorID).Count(&cnt).Error; err != nil || cnt == 0 {
		response.NotFound(c, "创作者不存在")
		return
	}
	if req.DramaID != nil && *req.DramaID > 0 {
		var drama model.Drama
		if err := s.db.First(&drama, *req.DramaID).Error; err != nil {
			if isNotFound(err) {
				response.NotFound(c, "短剧不存在")
				return
			}
			response.ServerError(c, "查询短剧失败")
			return
		}
		if drama.CreatorID == nil || *drama.CreatorID != req.CreatorID {
			response.InvalidParam(c, "短剧不属于该创作者")
			return
		}
	}

	ct := model.Contract{
		CreatorID:  req.CreatorID,
		DramaID:    req.DramaID,
		ContractNo: req.ContractNo,
		FileURL:    req.FileURL,
		Status:     model.ContractStatusPending,
	}
	if err := s.db.Create(&ct).Error; err != nil {
		response.ServerError(c, "创建合同失败")
		return
	}
	title := ""
	if ct.DramaID != nil {
		var d model.Drama
		if err := s.db.Select("title").First(&d, *ct.DramaID).Error; err == nil {
			title = d.Title
		}
	}
	response.OK(c, contractView(ct, title))
}

func (s *Server) adminGetContract(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var ct model.Contract
	if err := s.db.First(&ct, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "合同不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	title := ""
	if ct.DramaID != nil {
		var d model.Drama
		if err := s.db.Select("title").First(&d, *ct.DramaID).Error; err == nil {
			title = d.Title
		}
	}
	response.OK(c, contractView(ct, title))
}

type adminUpdateContractRequest struct {
	FileURL     *string `json:"file_url"`
	ScanFileKey *string `json:"scan_file_key"` // 签署后合同扫描件 PDF 的私有 COS key（先走 POST /admin/uploads/contract-sign 拿 key）
	Status      *string `json:"status"`
	EsignFlowID *string `json:"esign_flow_id"`
}

func (s *Server) adminUpdateContract(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var ct model.Contract
	if err := s.db.First(&ct, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "合同不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	var req adminUpdateContractRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}
	updates := map[string]interface{}{}
	if req.FileURL != nil {
		updates["file_url"] = *req.FileURL
	}
	if req.ScanFileKey != nil {
		if len(*req.ScanFileKey) > 512 {
			response.InvalidParam(c, "scan_file_key 过长")
			return
		}
		updates["scan_file_key"] = *req.ScanFileKey
	}
	if req.EsignFlowID != nil {
		updates["esign_flow_id"] = *req.EsignFlowID
	}
	if req.Status != nil && *req.Status != "" {
		switch *req.Status {
		case model.ContractStatusPending, model.ContractStatusSigning,
			model.ContractStatusSigned, model.ContractStatusCancelled:
			updates["status"] = *req.Status
		default:
			response.InvalidParam(c, "status 非法")
			return
		}
	} else if req.ScanFileKey != nil && *req.ScanFileKey != "" && ct.Status != model.ContractStatusCancelled {
		// 未显式传 status 且这次上传了扫描件：签署版合同到手，视为已签（已作废的不动）。
		updates["status"] = model.ContractStatusSigned
	}
	if len(updates) == 0 {
		title := ""
		if ct.DramaID != nil {
			var d model.Drama
			if err := s.db.Select("title").First(&d, *ct.DramaID).Error; err == nil {
				title = d.Title
			}
		}
		response.OK(c, contractView(ct, title))
		return
	}
	if err := s.db.Model(&ct).Updates(updates).Error; err != nil {
		response.ServerError(c, "更新合同失败")
		return
	}
	s.db.First(&ct, id)
	title := ""
	if ct.DramaID != nil {
		var d model.Drama
		if err := s.db.Select("title").First(&d, *ct.DramaID).Error; err == nil {
			title = d.Title
		}
	}
	response.OK(c, contractView(ct, title))
}

func (s *Server) adminCancelContract(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var ct model.Contract
	if err := s.db.First(&ct, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "合同不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	if ct.Status == model.ContractStatusSigned {
		response.Conflict(c, "已签署合同不能作废")
		return
	}
	if err := s.db.Model(&ct).Update("status", model.ContractStatusCancelled).Error; err != nil {
		response.ServerError(c, "作废合同失败")
		return
	}
	ct.Status = model.ContractStatusCancelled
	title := ""
	if ct.DramaID != nil {
		var d model.Drama
		if err := s.db.Select("title").First(&d, *ct.DramaID).Error; err == nil {
			title = d.Title
		}
	}
	response.OK(c, contractView(ct, title))
}

// contractScanDownload 校验通过后，对私有合同扫描件下发短时 presigned GET 下载链接。
func (s *Server) contractScanDownload(c *gin.Context, ct model.Contract) {
	if ct.ScanFileKey == "" {
		response.NotFound(c, "合同扫描件未上传")
		return
	}
	if !s.cos.Configured() {
		response.Fail(c, response.CodeThirdPartyError, "COS 未配置，无法生成下载链接")
		return
	}
	url, expiresAt, err := s.cos.PresignedGET(ct.ScanFileKey)
	if err != nil {
		response.ServerError(c, "生成下载链接失败")
		return
	}
	response.OK(c, gin.H{
		"download_url": url, // 短时有效，前端拿到直接打开即下载/预览 PDF
		"expires_at":   expiresAt,
	})
}

// adminDownloadContractScan 管理员下载签署后合同扫描件（PDF）。
// 路径：GET /v1/admin/contracts/:id/scan
func (s *Server) adminDownloadContractScan(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var ct model.Contract
	if err := s.db.First(&ct, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "合同不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	s.contractScanDownload(c, ct)
}

// creatorDownloadContractScan 创作者下载自己合同的签署扫描件（PDF）。
// 路径：GET /v1/creator/contracts/:id/scan
func (s *Server) creatorDownloadContractScan(c *gin.Context) {
	cid := middleware.CurrentID(c)
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var ct model.Contract
	if err := s.db.First(&ct, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "合同不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	if ct.CreatorID != cid {
		response.Forbidden(c, "合同不属于当前创作者")
		return
	}
	s.contractScanDownload(c, ct)
}

// adminEsignContract 腾讯电子签接入位（stub）。
// 真接入路径：用 github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/ess 创建签署流程，
// 把 ess_flow_id 写回合同；webhook 端在 /v1/webhooks/esign 接收 signed 事件再切 status。
func (s *Server) adminEsignContract(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var ct model.Contract
	if err := s.db.First(&ct, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "合同不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	if ct.Status != model.ContractStatusPending {
		response.Conflict(c, "合同当前状态不允许发起电子签")
		return
	}
	// TODO: 调用腾讯电子签 SDK
	response.FailWithData(c, response.CodeThirdPartyError, "电子签 SDK 尚未接入，请走线下签署或填 file_url", gin.H{
		"contract_id": ct.ID,
	})
}
