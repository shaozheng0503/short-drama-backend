package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const (
	CodeOK              = 0
	CodeInvalidParam    = 40001
	CodeUnauthorized    = 40101
	CodeForbidden       = 40301
	CodeNotFound        = 40401
	CodeConflict        = 40901
	CodeEpisodeLocked   = 42001
	CodeOrderUnusable   = 42002
	CodeRateLimited     = 42901
	CodeServerError     = 50001
	CodeThirdPartyError = 60001
)

type Body struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}

func OK(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, Body{Code: CodeOK, Message: "ok", Data: data})
}

func Fail(c *gin.Context, code int, message string) {
	c.JSON(http.StatusOK, Body{Code: code, Message: message, Data: nil})
}

func FailWithData(c *gin.Context, code int, message string, data interface{}) {
	c.JSON(http.StatusOK, Body{Code: code, Message: message, Data: data})
}

func InvalidParam(c *gin.Context, message string) {
	if message == "" {
		message = "参数错误"
	}
	Fail(c, CodeInvalidParam, message)
}

func Unauthorized(c *gin.Context, message string) {
	if message == "" {
		message = "未登录或 token 无效"
	}
	Fail(c, CodeUnauthorized, message)
}

func Forbidden(c *gin.Context, message string) {
	if message == "" {
		message = "无权限"
	}
	Fail(c, CodeForbidden, message)
}

func NotFound(c *gin.Context, message string) {
	if message == "" {
		message = "资源不存在"
	}
	Fail(c, CodeNotFound, message)
}

func Conflict(c *gin.Context, message string) {
	if message == "" {
		message = "重复操作"
	}
	Fail(c, CodeConflict, message)
}

func ServerError(c *gin.Context, message string) {
	if message == "" {
		message = "服务端错误"
	}
	c.JSON(http.StatusInternalServerError, Body{Code: CodeServerError, Message: message, Data: nil})
}

// WebhookUnauthorized 支付回调验签失败：返回 HTTP 401，让平台区分非法请求。
func WebhookUnauthorized(c *gin.Context, message string) {
	if message == "" {
		message = "验签失败"
	}
	c.JSON(http.StatusUnauthorized, Body{Code: CodeThirdPartyError, Message: message, Data: nil})
}

// WebhookRetry 支付回调业务处理失败：返回 HTTP 500，让平台重试。
func WebhookRetry(c *gin.Context, message string) {
	if message == "" {
		message = "处理失败"
	}
	c.JSON(http.StatusInternalServerError, Body{Code: CodeServerError, Message: message, Data: nil})
}
