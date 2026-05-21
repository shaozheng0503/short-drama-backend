package sms

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"math/big"
	"regexp"
	"sync"
	"time"

	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/model"

	"golang.org/x/time/rate"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrInvalidPhone      = errors.New("手机号格式不正确")
	ErrInvalidScene      = errors.New("不支持的短信场景")
	ErrTooFrequent       = errors.New("发送过于频繁，请稍后再试")
	ErrCodeMismatch      = errors.New("验证码错误或已过期")
	ErrTooManyAttempts   = errors.New("验证码尝试次数过多，请稍后再试")
	ErrProviderFail      = errors.New("短信下发失败")
	ErrSendIPRateLimited = errors.New("发送过于频繁，请稍后再试")
)

var phoneRegex = regexp.MustCompile(`^1[3-9]\d{9}$`)

type verifyLock struct {
	failures    int
	lockedUntil time.Time
}

// Service 短信验证码服务：负责持久化 + 速率控制 + 调用 Provider 真实下发。
type Service struct {
	db       *gorm.DB
	cfg      config.Config
	provider Provider

	verifyMu    sync.Mutex
	verifyLocks map[string]verifyLock

	sendIPMu       sync.Mutex
	sendIPLimiters map[string]*rate.Limiter
}

func New(db *gorm.DB, cfg config.Config) *Service {
	p := SelectProvider(cfg)
	log.Printf("[sms] provider=%s dev_mode=%v", p.Name(), cfg.SMSDevMode)
	return &Service{
		db:             db,
		cfg:            cfg,
		provider:       p,
		verifyLocks:    map[string]verifyLock{},
		sendIPLimiters: map[string]*rate.Limiter{},
	}
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

// AllowSendByIP 对短信发送接口做 IP 级限流，防止批量刷短信。
func (s *Service) AllowSendByIP(ip string) bool {
	if ip == "" {
		return true
	}
	s.sendIPMu.Lock()
	defer s.sendIPMu.Unlock()

	item, ok := s.sendIPLimiters[ip]
	if !ok {
		item = rate.NewLimiter(rate.Limit(s.cfg.SMSSendIPRPS), s.cfg.SMSSendIPBurst)
		s.sendIPLimiters[ip] = item
	}
	return item.Allow()
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
	var record model.SMSCode
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var latest model.SMSCode
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("phone = ? AND scene = ?", phone, scene).
			Order("created_at desc").
			First(&latest).Error
		if err == nil && latest.CreatedAt.After(cooldownStart) {
			return ErrTooFrequent
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		code, err := generateCode(4)
		if err != nil {
			return err
		}
		now := time.Now()
		record = model.SMSCode{
			Phone:     phone,
			Code:      code,
			Scene:     scene,
			ExpiredAt: now.Add(s.cfg.SMSCodeTTL),
			CreatedAt: now,
		}
		return tx.Create(&record).Error
	})
	if err != nil {
		return "", err
	}

	if err := s.provider.Send(context.Background(), phone, record.Code, scene); err != nil {
		log.Printf("[sms] provider=%s send failed phone=%s err=%v", s.provider.Name(), phone, err)
		if s.provider.Name() != "dev" {
			if deleteErr := s.db.Delete(&record).Error; deleteErr != nil {
				log.Printf("[sms] cleanup failed sms_code_id=%d err=%v", record.ID, deleteErr)
			}
			return "", ErrProviderFail
		}
	}

	s.resetVerifyLock(phone, scene)
	return record.Code, nil
}

// Verify 校验未过期、未使用的验证码；校验通过时把 used_at 写入。
// 事务 + SELECT FOR UPDATE：避免两个并发 Verify 同时使用同一条验证码导致重复登录。
func (s *Service) Verify(phone, scene, code string) error {
	if !ValidPhone(phone) {
		return ErrInvalidPhone
	}
	if !ValidScene(scene) {
		return ErrInvalidScene
	}
	if code == "" {
		return s.recordVerifyFailure(phone, scene, ErrCodeMismatch)
	}
	if err := s.checkVerifyLock(phone, scene); err != nil {
		return err
	}

	now := time.Now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var record model.SMSCode
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("phone = ? AND scene = ? AND code = ? AND used_at IS NULL AND expired_at > ?",
				phone, scene, code, now).
			Order("created_at desc").
			First(&record).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCodeMismatch
			}
			return err
		}
		return tx.Model(&record).Update("used_at", &now).Error
	})
	if err != nil {
		return s.recordVerifyFailure(phone, scene, err)
	}
	s.resetVerifyLock(phone, scene)
	return nil
}

func (s *Service) verifyKey(phone, scene string) string {
	return phone + ":" + scene
}

func (s *Service) checkVerifyLock(phone, scene string) error {
	key := s.verifyKey(phone, scene)
	now := time.Now()
	s.verifyMu.Lock()
	defer s.verifyMu.Unlock()
	state, ok := s.verifyLocks[key]
	if ok && now.Before(state.lockedUntil) {
		return ErrTooManyAttempts
	}
	return nil
}

func (s *Service) recordVerifyFailure(phone, scene string, err error) error {
	if !errors.Is(err, ErrCodeMismatch) {
		return err
	}
	key := s.verifyKey(phone, scene)
	now := time.Now()
	s.verifyMu.Lock()
	defer s.verifyMu.Unlock()
	state := s.verifyLocks[key]
	state.failures++
	if state.failures >= s.cfg.SMSMaxVerifyAttempts {
		state.lockedUntil = now.Add(s.cfg.SMSVerifyLockWindow)
		state.failures = 0
	}
	s.verifyLocks[key] = state
	if now.Before(state.lockedUntil) {
		return ErrTooManyAttempts
	}
	return ErrCodeMismatch
}

func (s *Service) resetVerifyLock(phone, scene string) {
	key := s.verifyKey(phone, scene)
	s.verifyMu.Lock()
	delete(s.verifyLocks, key)
	s.verifyMu.Unlock()
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
