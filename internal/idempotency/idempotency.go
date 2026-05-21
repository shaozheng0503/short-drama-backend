package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

var (
	ErrInProgress      = errors.New("idempotency request in progress")
	ErrPayloadMismatch = errors.New("idempotency payload mismatch")
)

type Service struct {
	client *redis.Client
	ttl    time.Duration
}

func New(client *redis.Client, ttl time.Duration) *Service {
	return &Service{client: client, ttl: ttl}
}

func (s *Service) Enabled() bool {
	return s != nil && s.client != nil && s.ttl > 0
}

func (s *Service) Middleware(subject string, subjectID func(*gin.Context) uint64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.Enabled() {
			c.Next()
			return
		}
		key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
		if key == "" {
			c.Next()
			return
		}

		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			response.ServerError(c, "读取请求体失败")
			c.Abort()
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		requestHash := hashRequest(c.Request.Method, c.FullPath(), body)
		cacheKey := fmt.Sprintf("idem:resp:%s:%d:%s:%s", subject, subjectID(c), c.FullPath(), key)
		lockKey := fmt.Sprintf("idem:lock:%s:%d:%s:%s", subject, subjectID(c), c.FullPath(), key)

		if cached, ok, err := s.loadCached(c.Request.Context(), cacheKey, requestHash); errors.Is(err, ErrPayloadMismatch) {
			response.Conflict(c, "Idempotency-Key 已被不同请求体使用")
			c.Abort()
			return
		} else if err != nil {
			response.ServerError(c, "幂等校验失败")
			c.Abort()
			return
		} else if ok {
			writeCached(c, cached)
			c.Abort()
			return
		}

		locked, err := s.client.SetNX(c.Request.Context(), lockKey, requestHash, s.ttl).Result()
		if err != nil {
			response.ServerError(c, "幂等锁获取失败")
			c.Abort()
			return
		}
		if !locked {
			existingHash, err := s.client.Get(c.Request.Context(), lockKey).Result()
			if err != nil && !errors.Is(err, redis.Nil) {
				response.ServerError(c, "幂等锁读取失败")
				c.Abort()
				return
			}
			if existingHash != "" && existingHash != requestHash {
				response.Conflict(c, "Idempotency-Key 已被不同请求体使用")
				c.Abort()
				return
			}
			response.Conflict(c, "相同 Idempotency-Key 的请求正在处理中，请稍后重试")
			c.Abort()
			return
		}
		defer s.client.Del(context.Background(), lockKey)

		writer := &captureWriter{ResponseWriter: c.Writer, body: &bytes.Buffer{}}
		c.Writer = writer
		c.Next()

		if writer.status >= http.StatusInternalServerError {
			return
		}
		if err := s.saveCached(context.Background(), cacheKey, requestHash, writer); err != nil {
			// 幂等缓存失败不回滚主业务，下一次请求仍可依赖业务幂等兜底。
			return
		}
	}
}

func (s *Service) loadCached(ctx context.Context, cacheKey, requestHash string) (cachedResponse, bool, error) {
	raw, err := s.client.Get(ctx, cacheKey).Result()
	if errors.Is(err, redis.Nil) {
		return cachedResponse{}, false, nil
	}
	if err != nil {
		return cachedResponse{}, false, err
	}
	var cached cachedResponse
	if err := json.Unmarshal([]byte(raw), &cached); err != nil {
		return cachedResponse{}, false, err
	}
	if cached.RequestHash != requestHash {
		return cachedResponse{}, false, ErrPayloadMismatch
	}
	return cached, true, nil
}

func (s *Service) saveCached(ctx context.Context, cacheKey, requestHash string, writer *captureWriter) error {
	contentType := writer.Header().Get("Content-Type")
	cached := cachedResponse{
		Status:      writer.status,
		ContentType: contentType,
		BodyB64:     base64.StdEncoding.EncodeToString(writer.body.Bytes()),
		RequestHash: requestHash,
	}
	raw, err := json.Marshal(cached)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, cacheKey, raw, s.ttl).Err()
}

type cachedResponse struct {
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	BodyB64     string `json:"body_b64"`
	RequestHash string `json:"request_hash"`
}

func writeCached(c *gin.Context, cached cachedResponse) {
	body, err := base64.StdEncoding.DecodeString(cached.BodyB64)
	if err != nil {
		response.ServerError(c, "幂等缓存响应读取失败")
		return
	}
	if cached.ContentType != "" {
		c.Header("Content-Type", cached.ContentType)
	}
	c.Status(cached.Status)
	_, _ = c.Writer.Write(body)
}

type captureWriter struct {
	gin.ResponseWriter
	body   *bytes.Buffer
	status int
}

func (w *captureWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *captureWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.body.Write(data)
	return w.ResponseWriter.Write(data)
}

func (w *captureWriter) WriteString(data string) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.body.WriteString(data)
	return w.ResponseWriter.WriteString(data)
}

func hashRequest(method, path string, body []byte) string {
	sum := sha256.Sum256(append([]byte(method+" "+path+"\n"), body...))
	return hex.EncodeToString(sum[:])
}
