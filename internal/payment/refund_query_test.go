package payment

import (
	"errors"
	"testing"
	"time"
)

// DevProvider 是联调期占位,新加的 QueryOrder 必须给出可用 stub:
// 状态必为 paid、订单号回填、PaidAt 非空且接近 now。
func TestDevProvider_QueryOrder_ReturnsStubPaid(t *testing.T) {
	p := &DevProvider{method: "alipay"}
	state, err := p.QueryOrder("ORDER-XYZ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.OrderNo != "ORDER-XYZ" {
		t.Errorf("OrderNo=%q want %q", state.OrderNo, "ORDER-XYZ")
	}
	if state.Status != StatusPaid {
		t.Errorf("Status=%q want %q", state.Status, StatusPaid)
	}
	if state.PlatformTradeNo == "" {
		t.Error("PlatformTradeNo 不应为空")
	}
	if state.PaidAt == nil {
		t.Fatal("PaidAt 不应为 nil")
	}
	if time.Since(*state.PaidAt) > time.Second {
		t.Errorf("PaidAt 应接近 now,差距=%v", time.Since(*state.PaidAt))
	}
}

// DevProvider.Refund 永远 Success=true,RefundNo 回填,PlatformRefundNo 非空。
func TestDevProvider_Refund_ReturnsSuccess(t *testing.T) {
	p := &DevProvider{method: "wechat"}
	res, err := p.Refund(RefundInput{
		OrderNo:     "ORDER-1",
		RefundNo:    "REF-1",
		AmountCents: 500,
		Reason:      "test",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Success {
		t.Error("Success 应为 true")
	}
	if res.RefundNo != "REF-1" {
		t.Errorf("RefundNo=%q want %q", res.RefundNo, "REF-1")
	}
	if res.PlatformRefundNo == "" {
		t.Error("PlatformRefundNo 不应为空")
	}
	if res.RefundedAt.IsZero() {
		t.Error("RefundedAt 不应为零值")
	}
}

// UnavailableProvider 必须拒绝所有新方法,避免配置缺失时静默"成功"。
func TestUnavailableProvider_RejectsQueryAndRefund(t *testing.T) {
	p := &UnavailableProvider{method: "alipay"}

	if _, err := p.QueryOrder("X"); !errors.Is(err, ErrProviderUnavailable) {
		t.Errorf("QueryOrder err=%v want ErrProviderUnavailable", err)
	}
	if _, err := p.Refund(RefundInput{OrderNo: "X", RefundNo: "R", AmountCents: 1}); !errors.Is(err, ErrProviderUnavailable) {
		t.Errorf("Refund err=%v want ErrProviderUnavailable", err)
	}
}

// parseAlipayTime 解析"2006-01-02 15:04:05"格式;支付宝时间均为 +08:00。
// 空串/非法返回零值;调用方按 IsZero 判断。
func TestParseAlipayTime(t *testing.T) {
	cases := []struct {
		in       string
		wantZero bool
		wantHour int
	}{
		{"", true, 0},
		{"   ", true, 0},
		{"not-a-time", true, 0},
		{"2026-06-04 14:30:00", false, 14},
		{"2026-06-04 00:00:00", false, 0},
	}
	for _, c := range cases {
		got := parseAlipayTime(c.in)
		if c.wantZero {
			if !got.IsZero() {
				t.Errorf("parseAlipayTime(%q)=%v want zero", c.in, got)
			}
			continue
		}
		if got.IsZero() {
			t.Errorf("parseAlipayTime(%q) 不应是零值", c.in)
			continue
		}
		if got.Hour() != c.wantHour {
			t.Errorf("parseAlipayTime(%q).Hour=%d want %d", c.in, got.Hour(), c.wantHour)
		}
		if got.Location() != time.Local {
			t.Errorf("parseAlipayTime(%q) location=%v want Local", c.in, got.Location())
		}
	}
}

// AlipayProvider 实现了 Provider 接口的所有方法 — 编译时静态保证。
// 没有这个 assert,接口扩了一个方法但忘了实现某个 Provider 时不会立刻报错。
func TestProviderInterfaceCompliance(t *testing.T) {
	var _ Provider = (*DevProvider)(nil)
	var _ Provider = (*UnavailableProvider)(nil)
	var _ Provider = (*AlipayProvider)(nil)
	var _ Provider = (*WechatProvider)(nil)
}

// 用 cents-yuan 函数保证退款金额换算与下单一致 — 防止"分"和"元"两套表示混淆。
func TestRefundAmountFormatting(t *testing.T) {
	// 990 分 = 9.90 元;退款也用同一函数,保证 alipay refund_amount 字段格式正确。
	if centsToYuan(990) != "9.90" {
		t.Errorf("centsToYuan(990)=%q want %q", centsToYuan(990), "9.90")
	}
	if centsToYuan(1) != "0.01" {
		t.Errorf("centsToYuan(1)=%q want %q", centsToYuan(1), "0.01")
	}
}
