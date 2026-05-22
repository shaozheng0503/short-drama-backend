// Package vod 给视频上传用：生成腾讯云 VOD 客户端上传签名，并验签节点回调。
//
// 不引 vod SDK：客户端签名只是 HMAC-SHA1 over query params 的 base64，自实现更轻。
// 文档：https://cloud.tencent.com/document/product/266/9221
//
// 上传链路：
//   1. 前端 POST /v1/admin/uploads/vod-sign 拿到 sign
//   2. 前端用 vod-js-sdk-v6 / 小程序 SDK 把 sign 喂给上传组件，直传 VOD
//   3. 拿到 FileId，前端调 POST /v1/admin/dramas/:id/episodes 带 vod_file_id 建剧集
//   4. VOD 转码完成 → 节点回调 POST /v1/webhooks/vod → 把 episode.video_url + status=ready 写回
package vod

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"

	"ai-drama-platform/internal/config"
)

var (
	ErrNotConfigured       = errors.New("vod not configured")
	ErrCallbackKeyMissing  = errors.New("vod callback key missing")
	ErrCallbackBadSignature = errors.New("vod callback signature mismatch")
)

type Signer struct {
	cfg config.Config
}

func New(cfg config.Config) *Signer { return &Signer{cfg: cfg} }

func (s *Signer) Configured() bool {
	c := s.cfg
	return c.VODSecretID != "" && c.VODSecretKey != ""
}

// SignResult 喂给前端 VOD SDK 用。
type SignResult struct {
	Signature     string    `json:"signature"`
	SubAppID      uint64    `json:"sub_app_id,omitempty"`
	Region        string    `json:"region"`
	ProcedureName string    `json:"procedure_name,omitempty"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// ClientUploadSignature 生成客户端上传签名。
// 算法：HMAC-SHA1(SecretKey, raw) → 与 raw 拼接再 base64，raw 是原始 query string。
func (s *Signer) ClientUploadSignature() (*SignResult, error) {
	if !s.Configured() {
		return nil, ErrNotConfigured
	}
	now := time.Now()
	expire := s.cfg.VODSignExpire
	if expire <= 0 {
		expire = time.Hour
	}
	expiresAt := now.Add(expire)

	q := url.Values{}
	q.Set("secretId", s.cfg.VODSecretID)
	q.Set("currentTimeStamp", itoa(now.Unix()))
	q.Set("expireTime", itoa(expiresAt.Unix()))
	q.Set("random", itoa(int64(randomUint32())))
	if s.cfg.VODProcedure != "" {
		q.Set("procedure", s.cfg.VODProcedure)
	}
	if s.cfg.VODSubAppID != 0 {
		q.Set("vodSubAppId", itoa(int64(s.cfg.VODSubAppID)))
	}
	raw := sortedEncode(q)

	mac := hmac.New(sha1.New, []byte(s.cfg.VODSecretKey))
	_, _ = mac.Write([]byte(raw))
	sigBytes := mac.Sum(nil)

	// Tencent VOD 客户端签名：sig (20 bytes) 跟在 raw 字符串之前，整体再 base64 一次。
	concat := append(sigBytes, []byte(raw)...)
	signature := base64.StdEncoding.EncodeToString(concat)

	return &SignResult{
		Signature:     signature,
		SubAppID:      s.cfg.VODSubAppID,
		Region:        s.cfg.VODRegion,
		ProcedureName: s.cfg.VODProcedure,
		ExpiresAt:     expiresAt,
	}, nil
}

// VerifyCallback 校验节点回调签名。
// 腾讯 VOD 节点回调把 sign 放在 query param `Sign` 里；签名值是
//   base64( hmac-sha1(CallbackKey, rawBody) )
// 文档：https://cloud.tencent.com/document/product/266/33779 (节点回调)
//
// 注意：腾讯不同事件 / 通知方式签名拼法略有差异；本实现按"节点回调"主流形式。
// 若 VOD 控制台勾的是"普通回调"，会没有签名，此时只能用 VOD_CALLBACK_KEY=空 来 bypass。
func (s *Signer) VerifyCallback(query url.Values, rawBody []byte) error {
	if s.cfg.VODCallbackKey == "" {
		return ErrCallbackKeyMissing
	}
	got := query.Get("Sign")
	if got == "" {
		// 兼容 sign 小写
		got = query.Get("sign")
	}
	if got == "" {
		return ErrCallbackBadSignature
	}
	mac := hmac.New(sha1.New, []byte(s.cfg.VODCallbackKey))
	_, _ = mac.Write(rawBody)
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(got)) {
		return ErrCallbackBadSignature
	}
	return nil
}

func itoa(n int64) string {
	// 不引 strconv 也可以，但既然标准库就有就直接用；这里保持包内自洽。
	return formatInt(n)
}

func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := make([]byte, 0, 20)
	for n > 0 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	if neg {
		buf = append(buf, '-')
	}
	// reverse
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}

func randomUint32() uint32 {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 在 darwin/linux 不会失败；万一失败用时间兜底，避免签名带 0
		return uint32(time.Now().UnixNano() & 0xffffffff)
	}
	return binary.BigEndian.Uint32(b[:])
}

// sortedEncode 按 key 字典序输出 querystring；VOD 签名要求顺序一致。
func sortedEncode(v url.Values) string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v.Get(k)))
	}
	return strings.Join(parts, "&")
}
