package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"
	"ai-drama-platform/internal/sms"

	"github.com/gin-gonic/gin"
)

func (s *Server) health(c *gin.Context) {
	response.OK(c, gin.H{
		"status":         "ok",
		"uptime_seconds": int64(time.Since(s.started).Seconds()),
	})
}

func (s *Server) ready(c *gin.Context) {
	sqlDB, err := s.db.DB()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, response.Body{
			Code:    response.CodeServerError,
			Message: "database handle unavailable",
			Data: gin.H{
				"status":   "not_ready",
				"database": "error",
			},
		})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		c.JSON(http.StatusServiceUnavailable, response.Body{
			Code:    response.CodeServerError,
			Message: "database ping failed",
			Data: gin.H{
				"status":   "not_ready",
				"database": "error",
			},
		})
		return
	}

	redisStatus := "skipped"
	if s.redis != nil {
		if err := s.redis.Ping(ctx).Err(); err != nil {
			c.JSON(http.StatusServiceUnavailable, response.Body{
				Code:    response.CodeServerError,
				Message: "redis ping failed",
				Data: gin.H{
					"status":   "not_ready",
					"database": "ok",
					"redis":    "error",
				},
			})
			return
		}
		redisStatus = "ok"
	}

	response.OK(c, gin.H{
		"status":         "ready",
		"database":       "ok",
		"redis":          redisStatus,
		"uptime_seconds": int64(time.Since(s.started).Seconds()),
	})
}

type smsSendRequest struct {
	Phone string `json:"phone" binding:"required"`
	Scene string `json:"scene" binding:"required"`
}

func (s *Server) sendSMS(c *gin.Context) {
	var req smsSendRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "phone 与 scene 必填")
		return
	}
	if !s.sms.AllowSendByIP(c.ClientIP()) {
		response.Fail(c, response.CodeRateLimited, "发送过于频繁，请稍后再试")
		return
	}
	switch req.Scene {
	case model.SMSScenAppLogin, model.SMSSceneCreatorLogin:
	default:
		response.InvalidParam(c, "scene 必须是 login 或 creator_login；换绑银行卡请用 POST /creator/bank-card/send-sms")
		return
	}
	code, err := s.sms.Send(req.Phone, req.Scene)
	if err != nil {
		switch {
		case errors.Is(err, sms.ErrInvalidPhone):
			response.InvalidParam(c, "手机号格式不正确")
		case errors.Is(err, sms.ErrInvalidScene):
			response.InvalidParam(c, "scene 必须是 login 或 creator_login")
		case errors.Is(err, sms.ErrTooFrequent):
			response.Conflict(c, "发送过于频繁，请 60 秒后重试")
		case errors.Is(err, sms.ErrProviderFail):
			response.Fail(c, response.CodeThirdPartyError, "短信网关下发失败，请稍后重试")
		default:
			response.ServerError(c, "短信发送失败")
		}
		return
	}

	data := gin.H{
		"expire_seconds": int(s.cfg.SMSCodeTTL.Seconds()),
	}
	if s.cfg.SMSDevMode {
		// dev 模式下回显验证码，便于 Apifox 自测；生产模式禁用。
		data["dev_code"] = code
	}
	response.OK(c, data)
}
