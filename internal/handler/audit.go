package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"

	"github.com/gin-gonic/gin"
)

func (s *Server) auditMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodOptions {
			c.Next()
			return
		}

		writer := &auditWriter{ResponseWriter: c.Writer, body: &bytes.Buffer{}}
		c.Writer = writer
		c.Next()

		actorSubject := middleware.CurrentSubject(c)
		if actorSubject == "" {
			actorSubject = "admin"
		}
		log := model.OperationLog{
			ActorSubject:    actorSubject,
			ActorID:         middleware.CurrentID(c),
			Method:          c.Request.Method,
			Path:            c.Request.URL.Path,
			FullPath:        c.FullPath(),
			Action:          actionName(c.Request.Method, c.FullPath()),
			ResourceType:    resourceType(c.FullPath()),
			ResourceID:      firstResourceID(c),
			StatusCode:      writer.status,
			ResponseCode:    responseCode(writer.body.Bytes()),
			ResponseMessage: truncateRune(responseMessage(writer.body.Bytes()), 255),
			ClientIP:        c.ClientIP(),
			UserAgent:       truncateRune(c.Request.UserAgent(), 255),
		}
		if log.StatusCode == 0 {
			log.StatusCode = http.StatusOK
		}
		if err := s.db.Create(&log).Error; err != nil {
			// 审计失败不阻断主链路，避免后台操作因为日志表异常失败。
			return
		}
	}
}

type auditWriter struct {
	gin.ResponseWriter
	body   *bytes.Buffer
	status int
}

func (w *auditWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *auditWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.body.Write(data)
	return w.ResponseWriter.Write(data)
}

func (w *auditWriter) WriteString(data string) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.body.WriteString(data)
	return w.ResponseWriter.WriteString(data)
}

func actionName(method, fullPath string) string {
	path := strings.TrimPrefix(fullPath, "/v1/admin/")
	path = strings.ReplaceAll(path, "/", ".")
	path = strings.ReplaceAll(path, ":", "")
	return strings.ToLower(method) + "." + path
}

func resourceType(fullPath string) string {
	parts := strings.Split(strings.TrimPrefix(fullPath, "/v1/admin/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "admin"
	}
	return parts[0]
}

func firstResourceID(c *gin.Context) string {
	for _, name := range []string{"id", "order_no"} {
		if value := c.Param(name); value != "" {
			return value
		}
	}
	return ""
}

func responseCode(body []byte) int {
	var payload struct {
		Code int `json:"code"`
	}
	_ = json.Unmarshal(body, &payload)
	return payload.Code
}

func responseMessage(body []byte) string {
	var payload struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &payload)
	return payload.Message
}

// truncateRune 按 UTF-8 rune 截断，避免把中文字符切成半个字节。
func truncateRune(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}
