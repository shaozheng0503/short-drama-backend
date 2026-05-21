package ratelimit

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"ai-drama-platform/internal/config"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

type Middleware struct {
	enabled bool
	rps     rate.Limit
	burst   int

	mu              sync.Mutex
	limiters        map[string]*visitorLimiter
	lastCleanup     time.Time
	cleanupInterval time.Duration
	visitorTTL      time.Duration
}

type visitorLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func New(cfg config.Config) *Middleware {
	return &Middleware{
		enabled:         cfg.RateLimitEnabled,
		rps:             rate.Limit(cfg.RateLimitRPS),
		burst:           cfg.RateLimitBurst,
		limiters:        map[string]*visitorLimiter{},
		cleanupInterval: time.Minute,
		visitorTTL:      10 * time.Minute,
	}
}

func (m *Middleware) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !m.enabled || c.Request.Method == http.MethodOptions || c.Request.URL.Path == "/health" || c.Request.URL.Path == "/ready" {
			c.Next()
			return
		}
		if strings.HasPrefix(c.Request.URL.Path, "/v1/webhooks/") {
			c.Next()
			return
		}
		if !m.allow(c.ClientIP() + ":" + c.Request.URL.Path) {
			c.JSON(http.StatusTooManyRequests, response.Body{
				Code:    response.CodeRateLimited,
				Message: "请求过于频繁，请稍后重试",
				Data:    nil,
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

func (m *Middleware) allow(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	item, ok := m.limiters[key]
	if !ok {
		item = &visitorLimiter{limiter: rate.NewLimiter(m.rps, m.burst)}
		m.limiters[key] = item
	}
	item.lastSeen = now
	if now.Sub(m.lastCleanup) >= m.cleanupInterval {
		m.cleanup(now)
		m.lastCleanup = now
	}
	return item.limiter.Allow()
}

// cleanup 在持有 m.mu 时调用；通过 lastCleanup 节流，避免每次请求都扫一遍。
func (m *Middleware) cleanup(now time.Time) {
	for key, item := range m.limiters {
		if now.Sub(item.lastSeen) > m.visitorTTL {
			delete(m.limiters, key)
		}
	}
}
