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

	// uploadRPS / uploadBurst：上传签名类路由的专用限流（比全局更高，因为前端批量上传时会在短时间内连续请求 vod-sign）
	uploadRPS   rate.Limit
	uploadBurst int

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
		uploadRPS:       rate.Limit(cfg.RateLimitUploadRPS),
		uploadBurst:     cfg.RateLimitUploadBurst,
		limiters:        map[string]*visitorLimiter{},
		cleanupInterval: time.Minute,
		visitorTTL:      10 * time.Minute,
	}
}

// isUploadRoute 判断是否为上传签名类路由（轻量接口，前端批量上传时会密集调用）
func isUploadRoute(route string) bool {
	switch route {
	case "/v1/creator/uploads/vod-sign",
		"/v1/creator/uploads/image-sign",
		"/v1/admin/uploads/vod-sign",
		"/v1/admin/uploads/contract-sign",
		"/v1/common/uploads/image-sign":
		return true
	default:
		return false
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
		// 用匹配到的路由模板（c.FullPath()，如 /v1/app/episodes/:id）做 key，而不是原始 URL.Path：
		// 否则攻击者用 /v1/<随机路径> 每条都会新建一条 limiter 记录 → 内存无界膨胀，且能逐路径绕过限流。
		// FullPath 的取值被注册路由数封顶；未匹配路由(404)统一归到一个桶。
		route := c.FullPath()
		if route == "" {
			route = "_unmatched"
		}
		if !m.allow(c.ClientIP()+":"+route, isUploadRoute(route)) {
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

func (m *Middleware) allow(key string, isUpload bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	item, ok := m.limiters[key]
	if !ok {
		if isUpload {
			item = &visitorLimiter{limiter: rate.NewLimiter(m.uploadRPS, m.uploadBurst)}
		} else {
			item = &visitorLimiter{limiter: rate.NewLimiter(m.rps, m.burst)}
		}
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
