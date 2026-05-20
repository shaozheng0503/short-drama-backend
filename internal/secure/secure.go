package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

var (
	ErrKeyMissing   = errors.New("DATA_ENCRYPTION_KEY 未设置")
	ErrKeyInvalid   = errors.New("DATA_ENCRYPTION_KEY 长度非法（需要 base64 解码后 32 字节）")
	ErrCipherShort  = errors.New("密文格式非法")
)

// Cryptor 用对称密钥做 AES-GCM 加密。
// keyB64：32 字节的 base64 编码（例：openssl rand -base64 32）
type Cryptor struct {
	gcm cipher.AEAD
}

func New(keyB64 string) (*Cryptor, error) {
	if keyB64 == "" {
		return nil, ErrKeyMissing
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKeyInvalid, err)
	}
	if len(key) != 32 {
		return nil, ErrKeyInvalid
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cryptor{gcm: gcm}, nil
}

// Encrypt 把明文加密为 base64(nonce || ciphertext || tag)。
func (c *Cryptor) Encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, c.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := c.gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt 反向操作。
func (c *Cryptor) Decrypt(cipherB64 string) (string, error) {
	if cipherB64 == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(cipherB64)
	if err != nil {
		return "", err
	}
	nonceSize := c.gcm.NonceSize()
	if len(raw) < nonceSize {
		return "", ErrCipherShort
	}
	nonce, body := raw[:nonceSize], raw[nonceSize:]
	plain, err := c.gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// MaskBankCard 把卡号脱敏成 622***1234；少于 8 位时直接返回原值。
func MaskBankCard(no string) string {
	n := len(no)
	if n < 8 {
		return no
	}
	return no[:3] + "***" + no[n-4:]
}

// MaskIDCard 把身份证脱敏成 110*************1234；少于 10 位时直接返回原值。
func MaskIDCard(no string) string {
	n := len(no)
	if n < 10 {
		return no
	}
	return no[:3] + "*************" + no[n-4:]
}

// Last4 返回末尾 4 位（不足时全返）。
func Last4(s string) string {
	if len(s) <= 4 {
		return s
	}
	return s[len(s)-4:]
}
