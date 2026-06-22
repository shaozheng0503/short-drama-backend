package handler

import (
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

type adminLoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

const (
	adminMaxFailedAttempts = 5
	adminLockoutDuration   = 15 * time.Minute
)

func (s *Server) adminLogin(c *gin.Context) {
	var req adminLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "username 与 password 必填")
		return
	}

	var admin model.Admin
	if err := s.db.Where("username = ?", req.Username).First(&admin).Error; err != nil {
		if isNotFound(err) {
			response.InvalidParam(c, "账号或密码错误")
			return
		}
		response.ServerError(c, "登录失败")
		return
	}
	if admin.Status != model.StatusActive {
		response.Forbidden(c, "账号已被禁用")
		return
	}
	now := time.Now()
	if admin.LockedUntil != nil && admin.LockedUntil.After(now) {
		response.Forbidden(c, "账号已临时锁定，请稍后再试")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(req.Password)); err != nil {
		attempts := admin.FailedLoginAttempts + 1
		updates := map[string]interface{}{"failed_login_attempts": attempts}
		if attempts >= adminMaxFailedAttempts {
			lockUntil := now.Add(adminLockoutDuration)
			updates["locked_until"] = lockUntil
		}
		s.db.Model(&model.Admin{}).Where("id = ?", admin.ID).Updates(updates)
		response.InvalidParam(c, "账号或密码错误")
		return
	}

	// 成功登录：清空失败计数与锁
	if admin.FailedLoginAttempts > 0 || admin.LockedUntil != nil {
		s.db.Model(&model.Admin{}).Where("id = ?", admin.ID).
			Updates(map[string]interface{}{"failed_login_attempts": 0, "locked_until": nil})
	}

	token, _, err := middleware.IssueToken(s.cfg, middleware.SubjectAdmin, admin.ID)
	if err != nil {
		response.ServerError(c, "签发 token 失败")
		return
	}
	response.OK(c, gin.H{
		"token": token,
		"admin": adminView(admin),
	})
}

// adminRefreshToken —— POST /v1/admin/auth/refresh（需有效 admin JWT）
// 滑动续期：用未过期的 token 换一个新的 7 天 token，前端在 app 加载/聚焦时调一次，
// 常用管理员就不会因 token 到期被踢。无需旧 token 失效（JWT 无状态，老 token 自然到期）。
func (s *Server) adminRefreshToken(c *gin.Context) {
	aid := middleware.CurrentID(c)
	var admin model.Admin
	if err := s.db.First(&admin, aid).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "管理员不存在")
			return
		}
		response.ServerError(c, "刷新失败")
		return
	}
	token, expiresAt, err := middleware.IssueToken(s.cfg, middleware.SubjectAdmin, admin.ID)
	if err != nil {
		response.ServerError(c, "签发 token 失败")
		return
	}
	response.OK(c, gin.H{
		"token":      token,
		"expires_at": expiresAt,
		"admin":      adminView(admin),
	})
}

func (s *Server) adminMe(c *gin.Context) {
	aid := middleware.CurrentID(c)
	var admin model.Admin
	if err := s.db.First(&admin, aid).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "管理员不存在")
			return
		}
		response.ServerError(c, "获取管理员失败")
		return
	}
	response.OK(c, adminView(admin))
}

func adminView(a model.Admin) gin.H {
	return gin.H{
		"id":       a.ID,
		"username": a.Username,
		"role":     a.Role,
		"status":   a.Status,
	}
}
