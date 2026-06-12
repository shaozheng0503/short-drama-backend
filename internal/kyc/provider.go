// Package kyc 封装创作者实名/企业认证的第三方核验能力：
//   - 个人：银行卡三要素核验（姓名 + 身份证号 + 银行卡号一致性，腾讯云人脸核身 BankCardVerification），
//     三要素本身已隐含「姓名+身份证一致」的实名校验。
//   - 企业：营业执照 OCR 识别（腾讯云文字识别 BizLicenseOCR），识别企业名/统一社会信用代码/法人，
//     供与用户填写做一致性比对（注意：OCR 仅识别图片，不核验工商真伪，真伪核验需企业四要素，列为后续）。
//
// 通过 KYC_DEV_MODE / 密钥是否齐全门控真实 provider，dev 模式走 stub 直通，便于联调。
package kyc

import (
	"context"
	"errors"
	"log"

	"ai-drama-platform/internal/config"
)

// ErrProviderUnavailable 在腾讯云鉴权失败 / 网络异常 / SDK 报错时返回，调用方据此降级为人工审核（不阻断提交）。
var ErrProviderUnavailable = errors.New("实名核验 provider 不可用")

// BankCard3Input 银行卡三要素核验入参（姓名 + 身份证号 + 银行卡号）。
type BankCard3Input struct {
	Name     string
	IDCard   string
	BankCard string
}

// BankCard3Result 银行卡三要素核验结果。Passed=true 表示三要素一致。
type BankCard3Result struct {
	Passed      bool
	Code        string // 渠道返回码（腾讯云 "0" = 一致）
	Description string // 人类可读说明（如「认证通过」「身份证与姓名不一致」）
}

// BizLicenseInput 营业执照 OCR 识别入参；ImageURL 需公网可访问。
type BizLicenseInput struct {
	ImageURL string
}

// BizLicenseResult 营业执照 OCR 识别结果（结构化）。
type BizLicenseResult struct {
	Name        string // 企业名称
	CreditCode  string // 统一社会信用代码（新版营业执照即注册号 RegNum）
	LegalPerson string // 法定代表人
	Address     string // 住所
	Business    string // 经营范围
	Status      string // 识别校验结果（CheckResult）
	Raw         string // 原始识别 JSON，存档供 Admin 复核
}

// Provider 第三方实名/企业核验抽象。
type Provider interface {
	Name() string
	VerifyBankCard3(ctx context.Context, in BankCard3Input) (*BankCard3Result, error)
	RecognizeBizLicense(ctx context.Context, in BizLicenseInput) (*BizLicenseResult, error)
}

// SelectProvider 选择具体实现：KYC_DEV_MODE=true 或腾讯云密钥不全 → DevProvider（stub 直通）；否则 TencentProvider。
func SelectProvider(cfg config.Config) Provider {
	if cfg.KYCDevMode {
		return &DevProvider{}
	}
	if cfg.TencentcloudSecretID == "" || cfg.TencentcloudSecretKey == "" {
		log.Printf("[kyc] KYC_DEV_MODE=false 但腾讯云密钥不全，退回 DevProvider（不做真实核验）。需配置 TENCENTCLOUD_SECRET_ID/KEY。")
		return &DevProvider{}
	}
	return &TencentProvider{cfg: cfg}
}
