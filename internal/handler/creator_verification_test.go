package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-drama-platform/internal/kyc"
	"ai-drama-platform/internal/model"

	"github.com/gin-gonic/gin"
)

// fakeKYC 可配置的 kyc.Provider 桩，覆盖核验分支（通过/不通过/异常/四要素比对）。
type fakeKYC struct {
	name    string
	bankRes *kyc.BankCard3Result
	bankErr error
	biz4Res *kyc.BizLicense4Result
	biz4Err error
	ocrRes  *kyc.BizLicenseResult
	ocrErr  error
}

func (f *fakeKYC) Name() string { return f.name }
func (f *fakeKYC) VerifyBankCard3(_ context.Context, _ kyc.BankCard3Input) (*kyc.BankCard3Result, error) {
	return f.bankRes, f.bankErr
}
func (f *fakeKYC) VerifyBizLicense4(_ context.Context, _ kyc.BizLicense4Input) (*kyc.BizLicense4Result, error) {
	return f.biz4Res, f.biz4Err
}
func (f *fakeKYC) RecognizeBizLicense(_ context.Context, _ kyc.BizLicenseInput) (*kyc.BizLicenseResult, error) {
	return f.ocrRes, f.ocrErr
}

func testCtx() *gin.Context {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/", nil)
	return c
}

func TestPersonalVerifyStatusFor(t *testing.T) {
	if got := personalVerifyStatusFor("bankcard3"); got != model.CreatorVerifyVerified {
		t.Errorf("bankcard3 应免人工复核直接 verified，得到 %q", got)
	}
	for _, m := range []string{"manual", ""} {
		if got := personalVerifyStatusFor(m); got != model.CreatorVerifyPending {
			t.Errorf("method=%q 应 pending（人工兜底），得到 %q", m, got)
		}
	}
}

func TestRunBankCard3Verify(t *testing.T) {
	cases := []struct {
		name        string
		provider    *fakeKYC
		wantMethod  string
		wantBlocked bool
		wantChecked bool
	}{
		{"dev 跳过 → manual 不拦截", &fakeKYC{name: "dev"}, "manual", false, false},
		{"核验通过 → bankcard3 + 存档时间", &fakeKYC{name: "tencent", bankRes: &kyc.BankCard3Result{Passed: true, Code: "0", Description: "认证通过"}}, "bankcard3", false, true},
		{"核验不通过 → 拦截", &fakeKYC{name: "tencent", bankRes: &kyc.BankCard3Result{Passed: false, Code: "1", Description: "姓名与身份证不一致"}}, "", true, false},
		{"第三方异常 → 降级 manual 不拦截", &fakeKYC{name: "tencent", bankErr: errors.New("timeout")}, "manual", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{kyc: tc.provider}
			method, _, checkedAt, blockMsg := s.runBankCard3Verify(testCtx(), "张三", "110101199003074518", "6222021234567890123")
			if method != tc.wantMethod {
				t.Errorf("method=%q want %q", method, tc.wantMethod)
			}
			if (blockMsg != "") != tc.wantBlocked {
				t.Errorf("blockMsg=%q wantBlocked=%v", blockMsg, tc.wantBlocked)
			}
			if (checkedAt != nil) != tc.wantChecked {
				t.Errorf("checkedAt=%v wantChecked=%v", checkedAt, tc.wantChecked)
			}
			if tc.wantBlocked && !strings.Contains(blockMsg, "实名核验未通过") {
				t.Errorf("blockMsg 缺少提示前缀: %q", blockMsg)
			}
		})
	}
}

func TestRunBizLicense4Verify(t *testing.T) {
	cases := []struct {
		name         string
		provider     *fakeKYC
		wantMethod   string
		wantBlocked  bool
		blockKeyword string
	}{
		{"dev 跳过 → manual", &fakeKYC{name: "dev"}, "manual", false, ""},
		{"四要素完全匹配 → biz_4e", &fakeKYC{name: "tencent", biz4Res: &kyc.BizLicense4Result{Available: true, Passed: true, EntNameOK: true, CreditCodeOK: true, LegalNameOK: true, LegalIDNumOK: true, Raw: "{}"}}, "biz_4e", false, ""},
		{"法人不一致 → 拦截", &fakeKYC{name: "tencent", biz4Res: &kyc.BizLicense4Result{Available: true, Passed: false, EntNameOK: true, CreditCodeOK: true, LegalNameOK: false, LegalIDNumOK: true}}, "", true, "法人姓名"},
		{"信用代码不一致 → 拦截", &fakeKYC{name: "tencent", biz4Res: &kyc.BizLicense4Result{Available: true, Passed: false, EntNameOK: true, CreditCodeOK: false, LegalNameOK: true, LegalIDNumOK: true}}, "", true, "统一社会信用代码"},
		{"系统不可用 → 降级 manual", &fakeKYC{name: "tencent", biz4Res: &kyc.BizLicense4Result{Available: false}}, "manual", false, ""},
		{"第三方异常 → 降级 manual", &fakeKYC{name: "tencent", biz4Err: errors.New("ocr down")}, "manual", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{kyc: tc.provider}
			method, _, _, blockMsg := s.runBizLicense4Verify(testCtx(), "腾云计算机", "911101085636548888", "张三", "110101199003074518")
			if tc.wantBlocked {
				if blockMsg == "" {
					t.Fatalf("期望拦截，blockMsg 为空")
				}
				if tc.blockKeyword != "" && !strings.Contains(blockMsg, tc.blockKeyword) {
					t.Errorf("blockMsg=%q 缺少关键字 %q", blockMsg, tc.blockKeyword)
				}
				return
			}
			if blockMsg != "" {
				t.Fatalf("非拦截用例却被拦截: %q", blockMsg)
			}
			if method != tc.wantMethod {
				t.Errorf("method=%q want %q", method, tc.wantMethod)
			}
		})
	}
}
