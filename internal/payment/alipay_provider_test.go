package payment

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"net/url"
	"sort"
	"strings"
	"testing"

	"ai-drama-platform/internal/config"
)

func TestCentsYuanRoundTrip(t *testing.T) {
	cases := []struct {
		cents int64
		yuan  string
	}{
		{0, "0.00"}, {1, "0.01"}, {99, "0.99"}, {100, "1.00"},
		{990, "9.90"}, {1290, "12.90"}, {123456, "1234.56"},
	}
	for _, c := range cases {
		if got := centsToYuan(c.cents); got != c.yuan {
			t.Errorf("centsToYuan(%d)=%q want %q", c.cents, got, c.yuan)
		}
		if got := yuanToCents(c.yuan); got != c.cents {
			t.Errorf("yuanToCents(%q)=%d want %d", c.yuan, got, c.cents)
		}
	}
}

// testProvider 用临时生成的 RSA2 密钥对构造 provider：应用私钥用于签名，
// 同一对的公钥当作「支付宝公钥」用于验签，便于本地自签自验。
func testProvider(t *testing.T) (*AlipayProvider, *rsa.PrivateKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	cfg := config.Config{
		AlipayAppID:      "2021000000000000",
		AlipayPrivateKey: string(privPEM),
		AlipayPublicKey:  string(pubPEM),
		AlipaySandbox:    true,
	}
	p, err := NewAlipayProvider(cfg)
	if err != nil {
		t.Fatalf("NewAlipayProvider: %v", err)
	}
	return p, priv
}

func TestAlipayPrepayScenes(t *testing.T) {
	p, _ := testProvider(t)

	app, err := p.Prepay(PrepayInput{OrderNo: "OD-APP-1", AmountCents: 990, Subject: "解锁", Scene: "app"})
	if err != nil {
		t.Fatalf("app prepay: %v", err)
	}
	if app["scene"] != "app" || app["order_string"] == "" {
		t.Errorf("app prepay params bad: %v", app)
	}

	wap, err := p.Prepay(PrepayInput{OrderNo: "OD-WAP-1", AmountCents: 990, Subject: "解锁", Scene: "wap"})
	if err != nil {
		t.Fatalf("wap prepay: %v", err)
	}
	if wap["scene"] != "wap" || wap["pay_url"] == "" {
		t.Errorf("wap prepay params bad: %v", wap)
	}

	def, err := p.Prepay(PrepayInput{OrderNo: "OD-DEF-1", AmountCents: 100})
	if err != nil {
		t.Fatalf("default prepay: %v", err)
	}
	if def["scene"] != "app" {
		t.Errorf("empty scene should default to app, got %v", def["scene"])
	}
}

func TestAlipayVerifyRejectsBadSign(t *testing.T) {
	p, _ := testProvider(t)
	body := "out_trade_no=OD-1&trade_status=TRADE_SUCCESS&total_amount=9.90&sign=garbage&sign_type=RSA2"
	if _, err := p.VerifyAndParse(nil, []byte(body)); !errors.Is(err, ErrVerifyFailed) {
		t.Errorf("want ErrVerifyFailed for bad sign, got %v", err)
	}
}

func TestAlipayVerifyValidNotify(t *testing.T) {
	p, priv := testProvider(t)
	values := url.Values{}
	values.Set("app_id", "2021000000000000")
	values.Set("out_trade_no", "OD-OK-1")
	values.Set("trade_no", "2026000000000001")
	values.Set("trade_status", "TRADE_SUCCESS")
	values.Set("total_amount", "9.90")
	values.Set("gmt_payment", "2026-06-02 15:00:00")
	values.Set("sign", signNotify(t, priv, values))
	values.Set("sign_type", "RSA2")

	ev, err := p.VerifyAndParse(nil, []byte(values.Encode()))
	if err != nil {
		t.Fatalf("VerifyAndParse: %v", err)
	}
	if !ev.Paid {
		t.Error("want Paid=true for TRADE_SUCCESS")
	}
	if ev.OrderNo != "OD-OK-1" {
		t.Errorf("OrderNo=%q want OD-OK-1", ev.OrderNo)
	}
	if ev.PlatformTradeNo != "2026000000000001" {
		t.Errorf("PlatformTradeNo=%q", ev.PlatformTradeNo)
	}
	if ev.AmountCents != 990 {
		t.Errorf("AmountCents=%d want 990", ev.AmountCents)
	}
}

// signNotify 复刻 nsign 的规范化（排除 sign/sign_type，trim 非空，k=v，排序，& 连接）+ RSA2 签名。
func signNotify(t *testing.T, priv *rsa.PrivateKey, values url.Values) string {
	t.Helper()
	var pairs []string
	for k := range values {
		if k == "sign" || k == "sign_type" {
			continue
		}
		for _, v := range values[k] {
			if vv := strings.TrimSpace(v); vv != "" {
				pairs = append(pairs, k+"="+vv)
			}
		}
	}
	sort.Strings(pairs)
	h := sha256.Sum256([]byte(strings.Join(pairs, "&")))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, h[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}
