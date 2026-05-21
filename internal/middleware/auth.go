package middleware

import (
	"strings"
	"time"

	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

const (
	SubjectApp     = "app"
	SubjectCreator = "creator"
	SubjectAdmin   = "admin"

	ctxKeySubject = "auth.subject"
	ctxKeyID      = "auth.id"
)

type Claims struct {
	Subject   string `json:"subject"`
	SubjectID uint64 `json:"subject_id"`
	jwt.RegisteredClaims
}

func IssueToken(cfg config.Config, subject string, id uint64) (string, time.Time, error) {
	expiresAt := time.Now().Add(cfg.JWTExpires)
	claims := Claims{
		Subject:   subject,
		SubjectID: id,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(cfg.JWTSecret))
	if err != nil {
		return "", expiresAt, err
	}
	return signed, expiresAt, nil
}

func parseClaims(cfg config.Config, tokenString string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		return []byte(cfg.JWTSecret), nil
	})
	if err != nil || !token.Valid {
		if err == nil {
			err = jwt.ErrTokenInvalidClaims
		}
		return nil, err
	}
	return claims, nil
}

func RequireSubject(cfg config.Config, subject string) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			response.Unauthorized(c, "缺少 Bearer token")
			c.Abort()
			return
		}
		raw := strings.TrimPrefix(header, "Bearer ")
		claims, err := parseClaims(cfg, raw)
		if err != nil {
			response.Unauthorized(c, "token 无效或已过期")
			c.Abort()
			return
		}
		if claims.Subject != subject {
			response.Forbidden(c, "身份与接口不匹配")
			c.Abort()
			return
		}
		c.Set(ctxKeySubject, claims.Subject)
		c.Set(ctxKeyID, claims.SubjectID)
		c.Next()
	}
}

func RequireApp(cfg config.Config) gin.HandlerFunc {
	return RequireSubject(cfg, SubjectApp)
}

func RequireCreator(cfg config.Config) gin.HandlerFunc {
	return RequireSubject(cfg, SubjectCreator)
}

func RequireAdmin(cfg config.Config) gin.HandlerFunc {
	return RequireSubject(cfg, SubjectAdmin)
}

func CurrentID(c *gin.Context) uint64 {
	if v, ok := c.Get(ctxKeyID); ok {
		if id, ok := v.(uint64); ok {
			return id
		}
	}
	return 0
}

func CurrentSubject(c *gin.Context) string {
	if v, ok := c.Get(ctxKeySubject); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// TryAppUserID 仅在 Authorization 是合法的 APP token 时返回 user id；
// 用于"可匿名、登录后扩展字段"的只读接口。
func TryAppUserID(c *gin.Context, cfg config.Config) uint64 {
	header := c.GetHeader("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return 0
	}
	raw := strings.TrimPrefix(header, "Bearer ")
	claims, err := parseClaims(cfg, raw)
	if err != nil {
		return 0
	}
	if claims.Subject != SubjectApp {
		return 0
	}
	return claims.SubjectID
}
