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

// fakeKYC 可配置的 kyc.Provider 桩，用于覆盖核验分支（通过/不通过/异常/OCR 比对）。
type fakeKYC struct {
	name    string
	bankRes *kyc.BankCard3Result
	bankErr error
	bizRes  *kyc.BizLicenseResult
	bizErr  error
}

func (f *fakeKYC) Name() string { return f.name }
func (f *fakeKYC) VerifyBankCard3(_ context.Context, _ kyc.BankCard3Input) (*kyc.BankCard3Result, error) {
	return f.bankRes, f.bankErr
}
func (f *fakeKYC) RecognizeBizLicense(_ context.Context, _ kyc.BizLicenseInput) (*kyc.BizLicenseResult, error) {
	return f.bizRes, f.bizErr
}

func testCtx() *gin.Context {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/", nil)
	return c
}

func TestRunBankCard3Verify(t *testing.T) {
	cases := []struct {
		name        string
		provider    *fakeKYC
		wantMethod  string
		wantBlocked bool
		wantChecked bool
	}{
		{
			name:        "dev 跳过 → manual 不拦截",
			provider:    &fakeKYC{name: "dev"},
			wantMethod:  "manual",
			wantBlocked: false,
			wantChecked: false,
		},
		{
			name:        "核验通过 → bankcard3 + 存档时间",
			provider:    &fakeKYC{name: "tencent", bankRes: &kyc.BankCard3Result{Passed: true, Code: "0", Description: "认证通过"}},
			wantMethod:  "bankcard3",
			wantBlocked: false,
			wantChecked: true,
		},
		{
			name:        "核验不通过 → 拦截",
			provider:    &fakeKYC{name: "tencent", bankRes: &kyc.BankCard3Result{Passed: false, Code: "1", Description: "姓名与身份证不一致"}},
			wantMethod:  "",
			wantBlocked: true,
			wantChecked: false,
		},
		{
			name:        "第三方异常 → 降级 manual 不拦截",
			provider:    &fakeKYC{name: "tencent", bankErr: errors.New("timeout")},
			wantMethod:  "manual",
			wantBlocked: false,
			wantChecked: false,
		},
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

func TestRunBizLicenseVerify(t *testing.T) {
	const orgName = "腾云计算机（北京）有限责任公司"
	const creditCode = "911101085636548888"

	cases := []struct {
		name         string
		provider     *fakeKYC
		wantMethod   string
		wantBlocked  bool
		wantLegal    string
		blockKeyword string
	}{
		{
			name:       "dev 跳过 → manual",
			provider:   &fakeKYC{name: "dev"},
			wantMethod: "manual",
		},
		{
			name: "识别一致 → biz_ocr + 法人",
			provider: &fakeKYC{name: "tencent", bizRes: &kyc.BizLicenseResult{
				Name: orgName, CreditCode: creditCode, LegalPerson: "张三", Raw: "{}",
			}},
			wantMethod: "biz_ocr",
			wantLegal:  "张三",
		},
		{
			name: "信用代码不一致 → 拦截",
			provider: &fakeKYC{name: "tencent", bizRes: &kyc.BizLicenseResult{
				Name: orgName, CreditCode: "910000000000000000",
			}},
			wantBlocked:  true,
			blockKeyword: "统一社会信用代码",
		},
		{
			name: "企业名不一致 → 拦截",
			provider: &fakeKYC{name: "tencent", bizRes: &kyc.BizLicenseResult{
				Name: "另一家公司", CreditCode: creditCode,
			}},
			wantBlocked:  true,
			blockKeyword: "企业名称",
		},
		{
			name:       "识别不出关键信息 → 降级 manual",
			provider:   &fakeKYC{name: "tencent", bizRes: &kyc.BizLicenseResult{}},
			wantMethod: "manual",
		},
		{
			name:       "第三方异常 → 降级 manual",
			provider:   &fakeKYC{name: "tencent", bizErr: errors.New("ocr down")},
			wantMethod: "manual",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{kyc: tc.provider}
			method, legal, _, _, blockMsg := s.runBizLicenseVerify(testCtx(), orgName, creditCode, "https://example.com/license.jpg")
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
			if tc.wantLegal != "" && legal != tc.wantLegal {
				t.Errorf("legal=%q want %q", legal, tc.wantLegal)
			}
		})
	}
}
