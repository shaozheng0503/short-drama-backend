// cmd/test-alipay —— 支付宝凭据一次性烟测，不依赖 DB / 不起服务 / 不动钱。
//
// 走哪个网关由 ALIPAY_SANDBOX 决定（与生产代码 NewAlipayProvider 完全一致）：
//   - ALIPAY_SANDBOX=true（默认）→ 新沙箱网关 openapi-sandbox.dl.alipaydev.com
//   - ALIPAY_SANDBOX=false       → 生产网关 openapi.alipay.com
//
// 验证：
//  1. NewAlipayProvider 能用给定密钥初始化（私钥解析 + 支付宝公钥加载）；
//  2. Prepay(app) 本地签名能产出 order_string（不连网、不动钱）；
//  3. QueryOrder 一个不存在的订单（只读查询、不动钱）——一次同时考验
//     ① 应用私钥签请求 ② 支付宝公钥验响应签名 ③ 网关 + app_id 被认可。
//     预期 "alipay trade not exist" = 凭据全对，只是订单不存在。
//
// 用法：
//
//	ALIPAY_APP_ID=... ALIPAY_PRIVATE_KEY_PATH=... ALIPAY_PUBLIC_KEY_PATH=... \
//	ALIPAY_SANDBOX=false go run ./cmd/test-alipay
package main

import (
	"fmt"
	"os"
	"strings"

	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/payment"
)

func main() {
	sandbox := os.Getenv("ALIPAY_SANDBOX") != "false"
	gateway := "生产网关 openapi.alipay.com"
	if sandbox {
		gateway = "新沙箱网关 openapi-sandbox.dl.alipaydev.com"
	}
	cfg := config.Config{
		AlipayAppID:          os.Getenv("ALIPAY_APP_ID"),
		AlipayPrivateKeyPath: os.Getenv("ALIPAY_PRIVATE_KEY_PATH"),
		AlipayPublicKeyPath:  os.Getenv("ALIPAY_PUBLIC_KEY_PATH"),
		AlipaySandbox:        sandbox,
	}
	fmt.Printf("app_id=%s\n网关=%s\n\n", cfg.AlipayAppID, gateway)

	p, err := payment.NewAlipayProvider(cfg)
	if err != nil {
		fmt.Printf("✗ [1] NewAlipayProvider 失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ [1] 初始化成功（应用私钥 + 支付宝公钥均已加载）")

	if app, err := p.Prepay(payment.PrepayInput{OrderNo: "SMOKE-APP-001", AmountCents: 990, Subject: "接入烟测", Scene: "app"}); err != nil {
		fmt.Printf("✗ [2] Prepay(app) 失败: %v\n", err)
	} else {
		fmt.Printf("✓ [2] Prepay(app) order_string 长度=%d（本地签名成功）\n", len(app["order_string"]))
	}

	fmt.Printf("\n[3] QueryOrder(不存在订单) —— 只读，不动钱…\n")
	st, err := p.QueryOrder("SMOKE-NONEXIST-0001")
	switch {
	case err == nil:
		fmt.Printf("⚠ 意外：订单竟然存在？%+v\n", st)
	case err.Error() == "alipay trade not exist":
		fmt.Println("✓ [3] 网关可达 + 应用私钥签名被接受 + 支付宝公钥验签通过")
		fmt.Println("      → 这套生产凭据有效，可以继续配置真实接入。")
	case strings.Contains(err.Error(), "40002"):
		fmt.Printf("✗ [3] 仍是 40002 无效 AppID: %v\n", err)
		fmt.Println("      → app_id 与该网关不匹配（核对 16 位 / 确认是这个网关的应用）。")
	case strings.Contains(err.Error(), "sign") || strings.Contains(err.Error(), "验签") || strings.Contains(err.Error(), "isv"):
		fmt.Printf("✗ [3] 签名/权限类错误: %v\n", err)
		fmt.Println("      → 私钥与应用不配套，或该应用未签约/未开通相关产品。")
	default:
		fmt.Printf("✗ [3] 非预期错误: %v\n", err)
	}
}
