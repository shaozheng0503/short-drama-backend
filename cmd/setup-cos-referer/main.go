// setup-cos-referer 把 COS 桶的 Referer 防盗链白名单设到 .env::COS_REFERER_WHITELIST。
//
// 目的：当前桶是 public-read，封面图 URL 谁拿到都能盗用 + 盗刷流量。开启
// Referer 白名单后，HTTP Referer 不在名单里的请求会被 COS 直接 403。
//
// 用法（在服务器上手动执行，不挂 systemd / 不自动跑）：
//
//	# 1. 在 .env 里追加白名单（逗号分隔，例如 *.shoplazza.com、apifox.com、自己前端域名）：
//	COS_REFERER_WHITELIST=*.shoplazza.com,localhost,127.0.0.1,apifox.com
//
//	# 2. 跑一次：
//	./drama-setup-cos-referer
//
//	# 3. 如果要回滚 / 关掉 Referer 防盗链：
//	./drama-setup-cos-referer --disable
//
// 不需要重启 drama-api：Referer 是 COS 桶级配置，对所有现有 URL 立刻生效。
//
// 文档：https://cloud.tencent.com/document/product/436/32492 (PutBucketReferer)
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"ai-drama-platform/internal/config"

	"github.com/tencentyun/cos-go-sdk-v5"
)

func main() {
	disable := flag.Bool("disable", false, "关闭 Referer 防盗链（保留白名单设置但 Status=Disabled）")
	emptyAllow := flag.Bool("empty-allow", false, "允许 Referer 为空的请求（curl/Apifox 默认不带 Referer，联调期建议 true）")
	flag.Parse()

	cfg := config.Load()
	if cfg.COSBucket == "" || cfg.COSRegion == "" || cfg.COSSecretID == "" || cfg.COSSecretKey == "" {
		fmt.Fprintln(os.Stderr, "COS_* 配置不全，请确认 .env 里 COS_BUCKET / COS_REGION / COS_SECRET_ID / COS_SECRET_KEY 都填了")
		os.Exit(1)
	}

	whitelist := getWhitelist(cfg.COSRefererWhitelist)
	status := "Enabled"
	if *disable {
		status = "Disabled"
	}
	emptyRef := "Deny"
	if *emptyAllow {
		emptyRef = "Allow"
	}

	bucketURL := fmt.Sprintf("https://%s.cos.%s.myqcloud.com", cfg.COSBucket, cfg.COSRegion)
	parsed, err := url.Parse(bucketURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse bucket url:", err)
		os.Exit(1)
	}
	client := cos.NewClient(&cos.BaseURL{BucketURL: parsed}, &http.Client{
		Transport: &cos.AuthorizationTransport{
			SecretID:  cfg.COSSecretID,
			SecretKey: cfg.COSSecretKey,
		},
		Timeout: 15 * time.Second,
	})

	opt := &cos.BucketPutRefererOptions{
		Status:                  status,
		RefererType:             "White-List",
		DomainList:              whitelist,
		EmptyReferConfiguration: emptyRef,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := client.Bucket.PutReferer(ctx, opt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "PutBucketReferer 失败:", err)
		if resp != nil && resp.Response != nil {
			fmt.Fprintln(os.Stderr, "  HTTP", resp.StatusCode)
		}
		os.Exit(1)
	}

	fmt.Printf("✅ COS Referer 防盗链已写入桶 %s\n", cfg.COSBucket)
	fmt.Printf("   Status         = %s\n", status)
	fmt.Printf("   RefererType    = White-List\n")
	fmt.Printf("   DomainList     = %v\n", whitelist)
	fmt.Printf("   EmptyRef       = %s （%s）\n", emptyRef, emptyRefHint(*emptyAllow))
	fmt.Println()
	fmt.Println("⚠️  联调注意：")
	fmt.Println("  - curl / Apifox 默认不发 Referer。要在联调期就跑这条命令，必须加 --empty-allow，")
	fmt.Println("    否则同事所有 curl 访问图片 URL 会被 403 拦掉。")
	fmt.Println("  - 联调结束 / 上线前去掉 --empty-allow 再跑一次，把 EmptyRef 切回 Deny。")
	fmt.Println("  - DomainList 支持通配符（*.example.com），必填一项以上才能 Enable。")
}

func getWhitelist(raw string) []string {
	out := []string{}
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		fmt.Fprintln(os.Stderr, "COS_REFERER_WHITELIST 为空，请在 .env 里加一行（逗号分隔），例如：")
		fmt.Fprintln(os.Stderr, "  COS_REFERER_WHITELIST=*.shoplazza.com,localhost,127.0.0.1,apifox.com")
		os.Exit(1)
	}
	return out
}

func emptyRefHint(allow bool) string {
	if allow {
		return "联调 OK，curl/Apifox 不带 Referer 也能访问"
	}
	return "生产推荐：不带 Referer 一律 403"
}
