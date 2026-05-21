package payment

// UnavailableProvider 在生产环境配置缺失时使用：拒绝预支付与回调，避免无验签 DevProvider 兜底。
type UnavailableProvider struct {
	method string
}

func (p *UnavailableProvider) Method() string { return p.method }

func (p *UnavailableProvider) Prepay(_ PrepayInput) (PrepayParams, error) {
	return nil, ErrProviderUnavailable
}

func (p *UnavailableProvider) VerifyAndParse(_ map[string]string, _ []byte) (*WebhookEvent, error) {
	return nil, ErrVerifyFailed
}
