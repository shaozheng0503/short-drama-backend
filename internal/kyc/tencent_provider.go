package kyc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"

	"ai-drama-platform/internal/config"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	tcerrors "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	faceid "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/faceid/v20180301"
	ocr "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/ocr/v20181119"
)

// TencentProvider 腾讯云真实核验接入：
//   - 银行卡三要素：人脸核身 faceid.BankCardVerification（faceid.tencentcloudapi.com）
//   - 营业执照识别：文字识别 ocr.BizLicenseOCR（ocr.tencentcloudapi.com）
//
// client 懒加载，避免启动期密钥不全直接 panic；任何 SDK 错误统一包成 ErrProviderUnavailable，
// 让上层降级为人工审核而不是把用户卡死。
type TencentProvider struct {
	cfg config.Config

	mu        sync.Mutex
	faceidCli *faceid.Client
	ocrCli    *ocr.Client
}

func (*TencentProvider) Name() string { return "tencent" }

func (p *TencentProvider) faceidClient() (*faceid.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.faceidCli != nil {
		return p.faceidCli, nil
	}
	cred := common.NewCredential(p.cfg.TencentcloudSecretID, p.cfg.TencentcloudSecretKey)
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = "faceid.tencentcloudapi.com"
	cpf.HttpProfile.ReqTimeout = 15 // 核验在认证请求路径上，收紧到 15s（SDK 默认 60s）
	c, err := faceid.NewClient(cred, p.cfg.KYCFaceIDRegion, cpf)
	if err != nil {
		return nil, err
	}
	p.faceidCli = c
	return c, nil
}

func (p *TencentProvider) ocrClient() (*ocr.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ocrCli != nil {
		return p.ocrCli, nil
	}
	cred := common.NewCredential(p.cfg.TencentcloudSecretID, p.cfg.TencentcloudSecretKey)
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = "ocr.tencentcloudapi.com"
	cpf.HttpProfile.ReqTimeout = 15 // OCR 识别可能略慢，给 15s（SDK 默认 60s 太长）
	c, err := ocr.NewClient(cred, p.cfg.KYCOCRRegion, cpf)
	if err != nil {
		return nil, err
	}
	p.ocrCli = c
	return c, nil
}

func (p *TencentProvider) VerifyBankCard3(_ context.Context, in BankCard3Input) (*BankCard3Result, error) {
	c, err := p.faceidClient()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	req := faceid.NewBankCardVerificationRequest()
	req.Name = common.StringPtr(in.Name)
	req.IdCard = common.StringPtr(in.IDCard)
	req.BankCard = common.StringPtr(in.BankCard)
	resp, err := c.BankCardVerification(req)
	if err != nil {
		logTCErr("bankcard3", err)
		return nil, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	if resp == nil || resp.Response == nil {
		return nil, fmt.Errorf("%w: 空响应", ErrProviderUnavailable)
	}
	code := strVal(resp.Response.Result)
	return &BankCard3Result{
		Passed:      code == "0", // 腾讯云：Result="0" 表示三要素一致
		Code:        code,
		Description: strVal(resp.Response.Description),
	}, nil
}

func (p *TencentProvider) VerifyBizLicense4(_ context.Context, in BizLicense4Input) (*BizLicense4Result, error) {
	c, err := p.ocrClient()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	req := ocr.NewVerifyBizLicenseEnterprise4Request()
	req.EntName = common.StringPtr(in.EntName)
	req.CreditCode = common.StringPtr(in.CreditCode)
	req.LrName = common.StringPtr(in.LegalName)
	req.IdNum = common.StringPtr(in.LegalIDNum)
	resp, err := c.VerifyBizLicenseEnterprise4(req)
	if err != nil {
		logTCErr("biz_license_4e", err)
		return nil, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	if resp == nil || resp.Response == nil {
		return nil, fmt.Errorf("%w: 空响应", ErrProviderUnavailable)
	}
	r := resp.Response
	raw, _ := json.Marshal(r)
	return &BizLicense4Result{
		Available:       int64Val(r.StatusCode) == 0, // StatusCode=0 成功可用；1=系统异常
		Passed:          int64Val(r.VerifyResult) == 1,
		EntNameOK:       boolVal(r.IsEntNameConsistent),
		CreditCodeOK:    boolVal(r.IsCreditCodeConsistent),
		LegalNameOK:     boolVal(r.IsLrNameConsistent),
		LegalIDNumOK:    boolVal(r.IsIdNumConsistent),
		OperatingStatus: strVal(r.OperatingStatus),
		Raw:             string(raw),
	}, nil
}

func (p *TencentProvider) RecognizeBizLicense(_ context.Context, in BizLicenseInput) (*BizLicenseResult, error) {
	c, err := p.ocrClient()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	req := ocr.NewBizLicenseOCRRequest()
	req.ImageUrl = common.StringPtr(in.ImageURL)
	resp, err := c.BizLicenseOCR(req)
	if err != nil {
		logTCErr("biz_license_ocr", err)
		return nil, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	if resp == nil || resp.Response == nil {
		return nil, fmt.Errorf("%w: 空响应", ErrProviderUnavailable)
	}
	r := resp.Response
	raw, _ := json.Marshal(r)
	return &BizLicenseResult{
		Name:        strVal(r.Name),
		CreditCode:  strVal(r.RegNum), // 新版营业执照 RegNum 即统一社会信用代码
		LegalPerson: strVal(r.Person),
		Address:     strVal(r.Address),
		Business:    strVal(r.Business),
		Status:      strVal(r.Period), // 营业期限（无独立工商状态字段，OCR 仅识别不核验在营状态）
		Raw:         string(raw),
	}, nil
}

func logTCErr(action string, err error) {
	var tcErr *tcerrors.TencentCloudSDKError
	if errors.As(err, &tcErr) {
		log.Printf("[kyc-tencent] %s SDK code=%s msg=%s requestId=%s", action, tcErr.Code, tcErr.Message, tcErr.RequestId)
		return
	}
	log.Printf("[kyc-tencent] %s err=%v", action, err)
}

func strVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func int64Val(v *int64) int64 {
	if v == nil {
		return -1
	}
	return *v
}

func boolVal(v *bool) bool {
	return v != nil && *v
}
