package kyc

import (
	"context"
	"log"
)

// DevProvider 联调用 stub：银行卡三要素恒通过；营业执照 OCR 返回空结果。
// 调用方据 Name()=="dev" 跳过真实核验/比对，保留原「提交即 pending、人工审核」流程。
type DevProvider struct{}

func (*DevProvider) Name() string { return "dev" }

func (*DevProvider) VerifyBankCard3(_ context.Context, in BankCard3Input) (*BankCard3Result, error) {
	log.Printf("[kyc-dev] bankcard3 name=%s -> pass(dev)", in.Name)
	return &BankCard3Result{Passed: true, Code: "0", Description: "dev 模式跳过真实核验"}, nil
}

func (*DevProvider) VerifyBizLicense4(_ context.Context, in BizLicense4Input) (*BizLicense4Result, error) {
	log.Printf("[kyc-dev] biz_license_4e ent=%s -> pass(dev)", in.EntName)
	return &BizLicense4Result{Available: true, Passed: true, EntNameOK: true, CreditCodeOK: true, LegalNameOK: true, LegalIDNumOK: true, OperatingStatus: "1", Raw: "dev 模式跳过四要素核验"}, nil
}

func (*DevProvider) RecognizeBizLicense(_ context.Context, _ BizLicenseInput) (*BizLicenseResult, error) {
	log.Printf("[kyc-dev] biz_license_ocr -> skip(dev)")
	return &BizLicenseResult{Raw: "dev 模式跳过 OCR"}, nil
}
