package sms

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"math/big"
	"regexp"
	"time"

	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/model"

	"gorm.io/gorm"
)

var (
	ErrInvalidPhone = errors.New("手机号格式不正确")
	ErrInvalidScene = errors.New("不支持的短信场景")
	ErrTooFrequent  = errors.New("发送过于频繁，请稍后再试")
	ErrCodeMismatch = errors.New("验证码错误或已过期")
	ErrProviderFail = errors.New("短信下发失败")
)

var phoneRegex = regexp.MustCompile(`^1[3-9]\d{9}$`)

// Service 短信验证码服务：负责持久化 + 速率控制 + 调用 Provider 真实下发。
type Service struct {
	db       *gorm.DB
	cfg      config.Config
	provider Provider
}

func New(db *gorm.DB, cfg config.Config) *Service {
	p := SelectProvider(cfg)
	log.Printf("[sms] provider=%s dev_mode=%v", p.Name(), cfg.SMSDevMode)
	return &Service{db: db, cfg: cfg, provider: p}
}

// ProviderName 暴露当前使用的 provider 名称，便于 handler 决定是否回显 dev_code。
func (s *Service) ProviderName() string { return s.provider.Name() }

func ValidScene(scene string) bool {
	switch scene {
	case model.SMSScenAppLogin, model.SMSSceneCreatorLogin:
		return true
	}
	return false
}

func ValidPhone(phone string) bool {
	return phoneRegex.MatchString(phone)
}

// Send 发送验证码：60 秒频控 + 5 分钟有效期 + 通过 provider 下发。
// 返回新生成的验证码（仅供 dev 模式回显，生产模式不应当回写到响应）。
func (s *Service) Send(phone, scene string) (string, error) {
	if !ValidPhone(phone) {
		return "", ErrInvalidPhone
	}
	if !ValidScene(scene) {
		return "", ErrInvalidScene
	}

	cooldownStart := time.Now().Add(-s.cfg.SMSResendWindow)
	var latest model.SMSCode
	err := s.db.Where("phone = ? AND scene = ? AND created_at > ?", phone, scene, cooldownStart).
		Order("created_at desc").
		First(&latest).Error
	if err == nil {
		return "", ErrTooFrequent
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}

	code, err := generateCode(4)
	if err != nil {
		return "", err
	}
	now := time.Now()
	record := model.SMSCode{
		Phone:     phone,
		Code:      code,
		Scene:     scene,
		ExpiredAt: now.Add(s.cfg.SMSCodeTTL),
		CreatedAt: now,
	}
	if err := s.db.Create(&record).Error; err != nil {
		return "", err
	}

	if err := s.provider.Send(context.Background(), phone, code, scene); err != nil {
		log.Printf("[sms] provider=%s send failed phone=%s err=%v", s.provider.Name(), phone, err)
		// dev provider 失败不阻塞：验证码已落库，dev_code 回显仍可走完登录链路用于排查；
		// 真实 provider 失败必须透传，让前端知道短信没真正发出。
		if s.provider.Name() != "dev" {
			return "", ErrProviderFail
		}
	}

	return code, nil
}

// Verify 校验未过期、未使用的验证码；校验通过时把 used_at 写入。
func (s *Service) Verify(phone, scene, code string) error {
	if !ValidPhone(phone) {
		return ErrInvalidPhone
	}
	if !ValidScene(scene) {
		return ErrInvalidScene
	}
	if code == "" {
		return ErrCodeMismatch
	}

	now := time.Now()
	var record model.SMSCode
	err := s.db.Where("phone = ? AND scene = ? AND code = ? AND used_at IS NULL AND expired_at > ?",
		phone, scene, code, now).
		Order("created_at desc").
		First(&record).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrCodeMismatch
		}
		return err
	}
	if err := s.db.Model(&record).Update("used_at", &now).Error; err != nil {
		return err
	}
	return nil
}

func generateCode(length int) (string, error) {
	max := big.NewInt(10)
	out := make([]byte, length)
	for i := 0; i < length; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = byte('0' + n.Int64())
	}
	return string(out), nil
}

func MaskPhone(phone string) string {
	if len(phone) != 11 {
		return phone
	}
	return fmt.Sprintf("%s****%s", phone[:3], phone[7:])
}
