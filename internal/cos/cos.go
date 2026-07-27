// Package cos 给图片上传用：生成腾讯云 COS V5 PUT 预签名 URL。
// 自实现签名（HMAC-SHA1），不引 cos-go-sdk-v5；避免再加一坨依赖。
//
// 协议参考：https://cloud.tencent.com/document/product/436/7778
// 关键点：
//  1. SignKey = HMAC-SHA1(SecretKey, KeyTime)
//  2. StringToSign = "sha1\nKeyTime\nSHA1(HttpString)\n"
//  3. Signature = HMAC-SHA1(SignKey, StringToSign)
//  4. URL 拼出 q-sign-algorithm / q-ak / q-sign-time / q-key-time / q-header-list / q-url-param-list / q-signature
package cos

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"ai-drama-platform/internal/config"

	cos "github.com/tencentyun/cos-go-sdk-v5"
)

var ErrNotConfigured = errors.New("cos not configured")

type Signer struct {
	cfg config.Config
}

func New(cfg config.Config) *Signer { return &Signer{cfg: cfg} }

// Configured 用于挂路由前判断；缺任一关键项就视为未配置，handler 直接 503。
func (s *Signer) Configured() bool {
	c := s.cfg
	return c.COSBucket != "" && c.COSRegion != "" && c.COSSecretID != "" && c.COSSecretKey != ""
}

// Host 返回 cos 域名（不含 scheme）。
// 开启全球加速时返回 {bucket}.cos.accelerate.myqcloud.com，否则返回 {bucket}.cos.{region}.myqcloud.com。
// 生产可绑 CDN 域名再覆盖 PublicURL。
func (s *Signer) Host() string {
	if s.cfg.COSAccelerate {
		return fmt.Sprintf("%s.cos.accelerate.myqcloud.com", s.cfg.COSBucket)
	}
	return fmt.Sprintf("%s.cos.%s.myqcloud.com", s.cfg.COSBucket, s.cfg.COSRegion)
}

// PublicURL 返回上传完成后对外的访问链接。
// 如果配了 CDN 就用 CDN，否则走 COS 默认域名。
func (s *Signer) PublicURL(key string) string {
	if s.cfg.COSCDNDomain != "" {
		return strings.TrimRight(s.cfg.COSCDNDomain, "/") + "/" + strings.TrimLeft(key, "/")
	}
	return "https://" + s.Host() + "/" + strings.TrimLeft(key, "/")
}

// PresignedPUT 生成 PUT 直传预签名 URL。
//
// key 用业务自己定的路径（如 images/2026/05/avatars/abc.jpg）。
// 调用方拿着返回的 URL 直接 PUT 文件即可，Body 是文件原文。
// 关键：URL 签名里强制带 x-cos-acl=public-read 头，让对象一上传就是公有读，
//
//	绕开"桶私有但要单文件公开"的常见踩坑场景。前端 PUT 时必须同时发这个 header。
func (s *Signer) PresignedPUT(key string) (signedURL string, expiresAt time.Time, requiredHeaders map[string]string, err error) {
	return s.PresignedPUTWithACL(key, "public-read")
}

// PresignedPUTWithACL 同 PresignedPUT，但允许指定对象 ACL。
// 图片等公开资源用 public-read；合同扫描件等含 PII 的用 private（仅能凭 PresignedGET 下载）。
func (s *Signer) PresignedPUTWithACL(key, acl string) (signedURL string, expiresAt time.Time, requiredHeaders map[string]string, err error) {
	if !s.Configured() {
		return "", time.Time{}, nil, ErrNotConfigured
	}
	if acl == "" {
		acl = "public-read"
	}
	now := time.Now()
	expire := s.cfg.COSSignExpire
	if expire <= 0 {
		expire = 15 * time.Minute
	}
	expiresAt = now.Add(expire)

	keyTime := fmt.Sprintf("%d;%d", now.Unix(), expiresAt.Unix())

	// 把 x-cos-acl 签进签名 — 客户端 PUT 时必须带这个 header，COS 才认。
	headers := map[string]string{
		"x-cos-acl": acl,
	}
	headerListStr, headerStr := buildHeaderParts(headers)

	httpString := strings.Join([]string{
		"put",
		"/" + strings.TrimLeft(key, "/"),
		"",        // url params
		headerStr, // 已签的 header k=v；按 COS 规范 lower-case + urlencode value
		"",
	}, "\n")

	signKey := hmacSHA1Hex(s.cfg.COSSecretKey, keyTime)
	stringToSign := strings.Join([]string{"sha1", keyTime, sha1Hex(httpString), ""}, "\n")
	signature := hmacSHA1Hex(signKey, stringToSign)

	q := url.Values{}
	q.Set("q-sign-algorithm", "sha1")
	q.Set("q-ak", s.cfg.COSSecretID)
	q.Set("q-sign-time", keyTime)
	q.Set("q-key-time", keyTime)
	q.Set("q-header-list", headerListStr)
	q.Set("q-url-param-list", "")
	q.Set("q-signature", signature)

	signedURL = "https://" + s.Host() + "/" + strings.TrimLeft(key, "/") + "?" + sortedEncode(q)
	return signedURL, expiresAt, headers, nil
}

// PresignedGET 生成 GET 下载预签名 URL，用于读取私有对象（如合同扫描件 PDF）。
// 使用官方 cos-go-sdk-v5 生成签名，确保兼容性。
// 短时有效，由后端鉴权通过后下发，避免把私有合同放成公共可访问。
// 始终走 COS 默认域名（不走 CDN，CDN 通常不校验 COS 签名）。
func (s *Signer) PresignedGET(key string) (signedURL string, expiresAt time.Time, err error) {
	if !s.Configured() {
		return "", time.Time{}, ErrNotConfigured
	}

	// 优先用官方 SDK 生成 presigned URL
	u, err2 := s.presignedGetSDK(key)
	if err2 == nil && u != "" {
		expire := s.cfg.COSSignExpire
		if expire <= 0 {
			expire = 15 * time.Minute
		}
		return u, time.Now().Add(expire), nil
	}

	// fallback: 手工签名
	now := time.Now()
	expire := s.cfg.COSSignExpire
	if expire <= 0 {
		expire = 15 * time.Minute
	}
	expiresAt = now.Add(expire)

	keyTime := fmt.Sprintf("%d;%d", now.Unix(), expiresAt.Unix())

	httpString := strings.Join([]string{
		"get",
		"/" + strings.TrimLeft(key, "/"),
		"", // url params
		"", // headers
		"",
	}, "\n")

	signKey := hmacSHA1Hex(s.cfg.COSSecretKey, keyTime)
	stringToSign := strings.Join([]string{"sha1", keyTime, sha1Hex(httpString), ""}, "\n")
	signature := hmacSHA1Hex(signKey, stringToSign)

	q := url.Values{}
	q.Set("q-sign-algorithm", "sha1")
	q.Set("q-ak", s.cfg.COSSecretID)
	q.Set("q-sign-time", keyTime)
	q.Set("q-key-time", keyTime)
	q.Set("q-header-list", "")
	q.Set("q-url-param-list", "")
	q.Set("q-signature", signature)

	signedURL = "https://" + s.Host() + "/" + strings.TrimLeft(key, "/") + "?" + sortedEncode(q)
	return signedURL, expiresAt, nil
}

// presignedGetSDK 使用官方 cos-go-sdk-v5 生成 presigned GET URL
func (s *Signer) presignedGetSDK(key string) (string, error) {
	bucketURL := "https://" + s.Host()
	u, err := url.Parse(bucketURL)
	if err != nil {
		return "", err
	}
	b := &cos.BaseURL{BucketURL: u}
	client := cos.NewClient(b, &http.Client{
		Transport: &cos.AuthorizationTransport{
			SecretID:  s.cfg.COSSecretID,
			SecretKey: s.cfg.COSSecretKey,
		},
	})

	expire := s.cfg.COSSignExpire
	if expire <= 0 {
		expire = 15 * time.Minute
	}

	opt := &cos.PresignedURLOptions{
		Query:  nil,
		Header: nil,
	}
	signedURL, err := client.Object.GetPresignedURL(context.Background(), http.MethodGet, key, s.cfg.COSSecretID, s.cfg.COSSecretKey, expire, opt)
	if err != nil {
		return "", err
	}
	return signedURL.String(), nil
}

// buildHeaderParts 输出两个值：
//  1. q-header-list = "x-cos-acl;..." 用分号连接的小写 header 名（字典序）
//  2. headerStr     = "x-cos-acl=public-read&..." 用 & 连的 key=urlencode(value)
func buildHeaderParts(headers map[string]string) (string, string) {
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, strings.ToLower(k))
	}
	sort.Strings(keys)
	lower := make(map[string]string, len(headers))
	for k, v := range headers {
		lower[strings.ToLower(k)] = v
	}
	listParts := make([]string, 0, len(keys))
	pairParts := make([]string, 0, len(keys))
	for _, k := range keys {
		listParts = append(listParts, k)
		pairParts = append(pairParts, k+"="+url.QueryEscape(lower[k]))
	}
	return strings.Join(listParts, ";"), strings.Join(pairParts, "&")
}

func hmacSHA1Hex(key, data string) string {
	m := hmac.New(sha1.New, []byte(key))
	_, _ = m.Write([]byte(data))
	return hex.EncodeToString(m.Sum(nil))
}

func sha1Hex(data string) string {
	h := sha1.New()
	_, _ = h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

// sortedEncode 按 key 字典序拼 query；COS 规定 q-* 参数顺序必须固定。
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
