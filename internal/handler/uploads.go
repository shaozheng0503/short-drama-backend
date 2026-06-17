package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"strings"
	"time"

	"ai-drama-platform/internal/alert"
	"ai-drama-platform/internal/cos"
	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"
	"ai-drama-platform/internal/vod"

	"github.com/gin-gonic/gin"
)

// imageSignRequest：业务方可以提示用途（avatar / cover / banner），决定上传到哪个子目录。
type imageSignRequest struct {
	Scene    string `json:"scene"`    // avatar / cover / banner / generic
	Ext      string `json:"ext"`      // 文件后缀，不带点：jpg / png / webp …
	Filename string `json:"filename"` // 仅作日志参考，不参与签名
}

// allowedImageExt：限制后缀，避免被当上传通道传任意文件。
// bmp 为漫剧封面 7:10 规格所需（详见 GET /creator/config/cover-specs）。
var allowedImageExt = map[string]bool{
	"jpg": true, "jpeg": true, "png": true, "webp": true, "gif": true, "bmp": true,
}

// imageContentType 按 ext 推 MIME；image-sign 把这个回给客户端，让 PUT 时按这个
// header 传。客户端漏传 Content-Type 的话，COS 会落成 application/x-www-form-urlencoded，
// 后续浏览器加载图片可能不渲染。
var imageContentType = map[string]string{
	"jpg":  "image/jpeg",
	"jpeg": "image/jpeg",
	"png":  "image/png",
	"webp": "image/webp",
	"gif":  "image/gif",
	"bmp":  "image/bmp",
}

// commonImageUploadSign 返回 COS PUT 预签名。前端拿到 url 后 PUT 文件原文即可。
// 路径：POST /v1/common/uploads/image-sign
// 公开接口（共用：APP 头像 / 后台封面）；为了防滥用，匿名走也允许，但靠速率限制 + key 路径前缀做 audit。
func (s *Server) commonImageUploadSign(c *gin.Context) {
	if !s.cos.Configured() {
		response.Fail(c, response.CodeThirdPartyError, "COS 未配置，无法生成上传签名")
		return
	}
	var req imageSignRequest
	_ = c.ShouldBindJSON(&req) // 全部可选

	ext := strings.ToLower(strings.TrimPrefix(req.Ext, "."))
	if ext == "" {
		ext = "jpg"
	}
	if !allowedImageExt[ext] {
		response.InvalidParam(c, "ext 不允许，仅支持 jpg/jpeg/png/bmp/webp/gif")
		return
	}
	scene := req.Scene
	if scene == "" {
		scene = "generic"
	}
	scene = sanitizeSegment(scene)

	subject := middleware.CurrentSubject(c)
	uid := middleware.CurrentID(c)
	prefix := "images/" + time.Now().Format("2006/01/02") + "/" + scene
	if subject != "" && uid > 0 {
		prefix += "/" + subject + "_" + itoa(uid)
	}
	key := prefix + "/" + randomToken(12) + "." + ext

	url, expiresAt, requiredHeaders, err := s.cos.PresignedPUT(key)
	if err != nil {
		if errors.Is(err, cos.ErrNotConfigured) {
			response.Fail(c, response.CodeThirdPartyError, "COS 未配置")
			return
		}
		response.ServerError(c, "签名失败")
		return
	}

	// 把签了名要求的 header 转给前端，告诉它 PUT 时必须带这些 header。
	hdrs := gin.H{}
	for k, v := range requiredHeaders {
		hdrs[k] = v
	}

	// 顺手把 MIME 也回给客户端：COS 默认 Content-Type 会被写成 form-urlencoded，
	// 浏览器加载就废。把 image/png 这种放进 headers 让客户端 PUT 时一并带上。
	if mime, ok := imageContentType[ext]; ok {
		hdrs["Content-Type"] = mime
	}

	response.OK(c, gin.H{
		"method":     "PUT",
		"upload_url": url,
		"public_url": s.cos.PublicURL(key),
		"key":        key,
		"expires_at": expiresAt,
		"headers":    hdrs, // 前端 PUT 时务必把这些 header 原样带上，否则 COS 验签失败 / MIME 不对
	})
}

// creatorImageUploadSign —— 创作者拿 COS 图片上传签名（需 creator JWT）。
// 与 common 版同源；路径前缀会写入 creator_{id}，便于审计。
// 路径：POST /v1/creator/uploads/image-sign
func (s *Server) creatorImageUploadSign(c *gin.Context) {
	s.commonImageUploadSign(c)
}

// allowedContractDocExt：合同扫描件仅允许 PDF（电子签平台下载的签署版是 PDF）。
var allowedContractDocExt = map[string]bool{"pdf": true}

// contractDocSignRequest：合同扫描件上传签名入参。ext 默认 pdf。
type contractDocSignRequest struct {
	Ext      string `json:"ext"`      // 文件后缀，不带点，仅支持 pdf
	Filename string `json:"filename"` // 仅作日志参考，不参与签名
}

// adminContractUploadSign 返回签署后合同扫描件（PDF）的 COS PUT 预签名；仅 admin。
// 路径：POST /v1/admin/uploads/contract-sign
//
// 前端流程：拿到 upload_url → 按 headers 里的 Content-Type PUT 上 PDF 原文 → 拿 public_url
// 回填到 PUT /v1/admin/contracts/:id 的 scan_file_url。与图片签名同源，仅放行 pdf 后缀、独立目录便于审计。
func (s *Server) adminContractUploadSign(c *gin.Context) {
	if !s.cos.Configured() {
		response.Fail(c, response.CodeThirdPartyError, "COS 未配置，无法生成上传签名")
		return
	}
	var req contractDocSignRequest
	_ = c.ShouldBindJSON(&req) // 全部可选

	ext := strings.ToLower(strings.TrimPrefix(req.Ext, "."))
	if ext == "" {
		ext = "pdf"
	}
	if !allowedContractDocExt[ext] {
		response.InvalidParam(c, "ext 不允许，合同扫描件仅支持 pdf")
		return
	}

	uid := middleware.CurrentID(c)
	prefix := "contracts/" + time.Now().Format("2006/01/02")
	if uid > 0 {
		prefix += "/admin_" + itoa(uid)
	}
	key := prefix + "/" + randomToken(12) + "." + ext

	// 合同扫描件含 PII，以 private ACL 上传，不可公开访问；下载走鉴权后的 presigned GET。
	url, expiresAt, requiredHeaders, err := s.cos.PresignedPUTWithACL(key, "private")
	if err != nil {
		if errors.Is(err, cos.ErrNotConfigured) {
			response.Fail(c, response.CodeThirdPartyError, "COS 未配置")
			return
		}
		response.ServerError(c, "签名失败")
		return
	}

	hdrs := gin.H{}
	for k, v := range requiredHeaders {
		hdrs[k] = v
	}
	// PDF 必须带 application/pdf，否则 COS 落成 form-urlencoded，浏览器点开会变下载乱码而非预览。
	hdrs["Content-Type"] = "application/pdf"

	// 不回 public_url：private 对象公网不可访问。前端 PUT 完把 key 回填到 PUT /admin/contracts/:id 的 scan_file_key，
	// 下载统一走 GET /admin/contracts/:id/scan（短时 presigned）。
	response.OK(c, gin.H{
		"method":     "PUT",
		"upload_url": url,
		"key":        key,
		"expires_at": expiresAt,
		"headers":    hdrs, // 前端 PUT 时务必把这些 header（含 x-cos-acl=private）原样带上，否则 COS 验签失败
	})
}

// adminVODUploadSign 返回 VOD 客户端上传签名；admin 鉴权过的才能拿。
// 路径：POST /v1/admin/uploads/vod-sign
//
// 前端流程：拿到 signature → 用 vod-js-sdk-v6 / 小程序 SDK upload → 拿到 FileId 后建 episode。
func (s *Server) adminVODUploadSign(c *gin.Context) {
	if !s.vod.Configured() {
		response.Fail(c, response.CodeThirdPartyError, "VOD 未配置，无法生成上传签名")
		return
	}
	result, err := s.vod.ClientUploadSignature()
	if err != nil {
		if errors.Is(err, vod.ErrNotConfigured) {
			response.Fail(c, response.CodeThirdPartyError, "VOD 未配置")
			return
		}
		log.Printf("[vod] sign err=%v", err)
		response.ServerError(c, "签名失败")
		return
	}
	response.OK(c, result)
}

// creatorVODUploadSign —— 创作者拿 VOD 客户端上传签名。
// 与 admin 版同源；分两套接口的目的是审计/限流维度更清楚，未来如要按创作者维度做配额时不用改入口。
// 路径：POST /v1/creator/uploads/vod-sign
func (s *Server) creatorVODUploadSign(c *gin.Context) {
	if !s.vod.Configured() {
		response.Fail(c, response.CodeThirdPartyError, "VOD 未配置，无法生成上传签名")
		return
	}
	result, err := s.vod.ClientUploadSignature()
	if err != nil {
		if errors.Is(err, vod.ErrNotConfigured) {
			response.Fail(c, response.CodeThirdPartyError, "VOD 未配置")
			return
		}
		log.Printf("[vod] creator sign err=%v", err)
		response.ServerError(c, "签名失败")
		return
	}
	response.OK(c, result)
}

// vodCallbackEnvelope —— 节点回调的最常见字段子集。
// 实际腾讯 VOD 节点回调 schema 较复杂，本结构只挑业务关心的字段；其它由 raw body 经签名校验后透传到日志。
//
// FileUploadEvent.MediaUrl 真实路径是 MediaBasicInfo.MediaUrl（首次联调被坑过）；
// 同时保留顶层 MediaUrl 兼容旧/精简 payload，handler 解析时优先取 MediaBasicInfo。
type vodCallbackEnvelope struct {
	EventType       string `json:"EventType"`
	FileUploadEvent *struct {
		FileID         string `json:"FileId"`
		MediaURL       string `json:"MediaUrl"`
		MediaBasicInfo *struct {
			Name     string `json:"Name"`
			Type     string `json:"Type"`
			MediaURL string `json:"MediaUrl"`
			CoverURL string `json:"CoverUrl"`
		} `json:"MediaBasicInfo,omitempty"`
		MetaData *struct {
			Duration float64 `json:"Duration"`
		} `json:"MetaData"`
	} `json:"FileUploadEvent,omitempty"`
	ProcedureStateChangeEvent *struct {
		TaskID                string                   `json:"TaskId"`
		Status                string                   `json:"Status"` // PROCESSING / FINISH / ERROR
		ErrCode               int                      `json:"ErrCode"`
		Message               string                   `json:"Message"`
		FileID                string                   `json:"FileId"`
		FileName              string                   `json:"FileName"`
		MediaProcessResultSet []mediaProcessResultItem `json:"MediaProcessResultSet,omitempty"`
	} `json:"ProcedureStateChangeEvent,omitempty"`
}

// mediaProcessResultItem —— 转码 / 截图 / AI 审核每个子任务的结果。
// 我们只关心 Transcode 这一类：FINISH 时拿其中第一个有 Url 的 Output 作为新的 video_url。
// 文档：https://cloud.tencent.com/document/product/266/33779
type mediaProcessResultItem struct {
	Type          string `json:"Type"` // Transcode / SnapshotByTimeOffset / AiContentReview ...
	TranscodeTask *struct {
		Status  string `json:"Status"` // SUCCESS / FAIL
		ErrCode int    `json:"ErrCode"`
		Message string `json:"Message"`
		Output  *struct {
			URL       string  `json:"Url"`
			Container string  `json:"Container"` // mp4 / hls / dash ...
			Bitrate   int64   `json:"Bitrate"`
			Height    int     `json:"Height"`
			Width     int     `json:"Width"`
			Duration  float64 `json:"Duration"`
		} `json:"Output,omitempty"`
	} `json:"TranscodeTask,omitempty"`
}

// webhookVOD 节点回调：转码 / 上传完成时更新 episodes 表。
// 路径：POST /v1/webhooks/vod
func (s *Server) webhookVOD(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		response.WebhookRetry(c, "读取请求体失败")
		return
	}

	// 签名校验：未配 VOD_CALLBACK_KEY 时 bypass + 告警；配了就强校。
	if s.cfg.VODCallbackKey != "" {
		if err := s.vod.VerifyCallback(c.Request.URL.Query(), body); err != nil {
			log.Printf("[webhook-vod] verify err=%v", err)
			s.alerts.SendAsync(alert.Event{
				Level:   "error",
				Type:    "vod_webhook_verify_failed",
				Message: "VOD 节点回调验签失败",
				Fields:  map[string]interface{}{"error": err.Error()},
			})
			response.WebhookUnauthorized(c, "验签失败")
			return
		}
	} else {
		log.Printf("[webhook-vod] VOD_CALLBACK_KEY 为空，跳过验签（生产请配齐）")
	}

	var env vodCallbackEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		log.Printf("[webhook-vod] unmarshal err=%v body=%s", err, truncate(string(body), 400))
		// 解析失败也 ack：VOD 不会拿到一个有意义的 body 再重试，重发只会重复失败。
		response.OK(c, gin.H{"ack": true, "ignored": "unmarshal_error"})
		return
	}

	log.Printf("[webhook-vod] event=%s body_bytes=%d", env.EventType, len(body))

	switch env.EventType {
	case "NewFileUpload":
		s.handleVODFileUpload(&env)
	case "ProcedureStateChanged":
		s.handleVODProcedureStateChanged(&env)
	default:
		log.Printf("[webhook-vod] unhandled event=%s", env.EventType)
	}

	response.OK(c, gin.H{"ack": true})
}

func (s *Server) handleVODFileUpload(env *vodCallbackEnvelope) {
	e := env.FileUploadEvent
	if e == nil || e.FileID == "" {
		return
	}
	updates := map[string]interface{}{}
	mediaURL := e.MediaURL
	if mediaURL == "" && e.MediaBasicInfo != nil {
		mediaURL = e.MediaBasicInfo.MediaURL
	}
	if mediaURL != "" {
		updates["video_url"] = mediaURL
	}
	if e.MetaData != nil && e.MetaData.Duration > 0 {
		updates["duration_seconds"] = int(e.MetaData.Duration)
	}
	// 若未配 procedure，那么 NewFileUpload 时直接 ready；
	// 若配了 procedure，等 ProcedureStateChanged 再切 ready，更准。
	if s.cfg.VODProcedure == "" {
		updates["status"] = model.EpisodeStatusReady
	}
	if len(updates) == 0 {
		return
	}
	res := s.db.Model(&model.Episode{}).Where("vod_file_id = ?", e.FileID).Updates(updates)
	if res.Error != nil {
		log.Printf("[webhook-vod] update by file_id=%s err=%v", e.FileID, res.Error)
		return
	}
	log.Printf("[webhook-vod] file_upload file_id=%s rows=%d url_set=%v", e.FileID, res.RowsAffected, mediaURL != "")
}

func (s *Server) handleVODProcedureStateChanged(env *vodCallbackEnvelope) {
	e := env.ProcedureStateChangeEvent
	if e == nil || e.FileID == "" {
		return
	}
	updates := map[string]interface{}{}
	switch e.Status {
	case "FINISH":
		updates["status"] = model.EpisodeStatusReady
		// 转码成功后优先把 video_url 切到转码输出（HLS / 多码率），否则前端拿到的还是原始 mp4。
		if outURL, container := pickTranscodeOutput(e.MediaProcessResultSet); outURL != "" {
			updates["video_url"] = outURL
			log.Printf("[webhook-vod] proc_state transcode_picked file_id=%s container=%s url=%s",
				e.FileID, container, truncate(outURL, 120))
		}
	case "ERROR":
		updates["status"] = model.EpisodeStatusFailed
		log.Printf("[webhook-vod] proc_state ERROR file_id=%s err_code=%d msg=%s",
			e.FileID, e.ErrCode, truncate(e.Message, 200))
	default:
		return
	}
	res := s.db.Model(&model.Episode{}).Where("vod_file_id = ?", e.FileID).Updates(updates)
	if res.Error != nil {
		log.Printf("[webhook-vod] proc update file_id=%s err=%v", e.FileID, res.Error)
		return
	}
	log.Printf("[webhook-vod] proc_state file_id=%s status=%s rows=%d", e.FileID, e.Status, res.RowsAffected)
}

// pickTranscodeOutput 在 MediaProcessResultSet 里找一个可用的转码输出 URL。
// 选取规则：优先 container=hls（自适应播放最稳）→ 没有再退到任意 mp4/其它 → 最后才返回 "" 表示没找到。
// 同一种 container 出现多个分辨率时取第一个（运营在控制台模板里把目标清晰度排第一即可）。
func pickTranscodeOutput(items []mediaProcessResultItem) (url, container string) {
	var fallbackURL, fallbackContainer string
	for _, it := range items {
		if it.Type != "Transcode" || it.TranscodeTask == nil || it.TranscodeTask.Output == nil {
			continue
		}
		if it.TranscodeTask.Status != "" && it.TranscodeTask.Status != "SUCCESS" {
			continue
		}
		out := it.TranscodeTask.Output
		if out.URL == "" {
			continue
		}
		if out.Container == "hls" {
			return out.URL, out.Container
		}
		if fallbackURL == "" {
			fallbackURL, fallbackContainer = out.URL, out.Container
		}
	}
	return fallbackURL, fallbackContainer
}

// --- small helpers ---

func sanitizeSegment(s string) string {
	// 只留字母数字 + 横线下划线，防路径穿越
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '-', ch == '_':
			b = append(b, ch)
		}
	}
	if len(b) == 0 {
		return "generic"
	}
	return string(b)
}

func randomToken(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	out := make([]byte, 0, 20)
	for n > 0 {
		out = append(out, byte('0'+n%10))
		n /= 10
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
