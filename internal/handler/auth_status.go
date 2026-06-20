package handler

import (
	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

func (s *Server) requireActiveApp() gin.HandlerFunc {
	return s.requireActiveAccount(middleware.SubjectApp, func(id uint64) (string, error) {
		var user model.User
		if err := s.db.Select("status").First(&user, id).Error; err != nil {
			return "", err
		}
		return user.Status, nil
	}, "用户不存在", "账号已被封禁或禁用")
}

func (s *Server) requireActiveCreator() gin.HandlerFunc {
	return s.requireActiveAccount(middleware.SubjectCreator, func(id uint64) (string, error) {
		var creator model.Creator
		if err := s.db.Select("status").First(&creator, id).Error; err != nil {
			return "", err
		}
		return creator.Status, nil
	}, "创作者不存在", "账号已被封禁或禁用")
}

// requireVerifiedCreator 限定只有实名认证通过(verified)的创作者才能执行上传 / 建剧 / 提交审核等写操作。
// 对应需求「没认证通过不能上传还有相关操作」。挂在 requireActiveCreator 之后（账号正常前提下再校验认证）。
// 未通过认证统一返回 40301 + need_verification 标记，前端据此把用户引导到实名认证页。
// 只读接口（列表 / 详情 / 拉默认值）与认证提交接口本身不挂此中间件，否则没法完成认证。
func (s *Server) requireVerifiedCreator() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := middleware.CurrentID(c)
		var creator model.Creator
		if err := s.db.Select("verify_status").First(&creator, id).Error; err != nil {
			if isNotFound(err) {
				response.NotFound(c, "创作者不存在")
			} else {
				response.ServerError(c, "账号状态校验失败")
			}
			c.Abort()
			return
		}
		if creator.VerifyStatus != model.CreatorVerifyVerified {
			response.FailWithData(c, response.CodeForbidden, "请先完成实名认证后再进行上传等操作", gin.H{
				"need_verification": true,
				"verify_status":     creator.VerifyStatus,
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

const ctxAdminRole = "admin.role"

// requireActiveAdmin 校验管理员账号正常，并把角色写入 context 供 requireAdminRole 使用。
func (s *Server) requireActiveAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if middleware.CurrentSubject(c) != middleware.SubjectAdmin {
			c.Next()
			return
		}
		id := middleware.CurrentID(c)
		var admin model.Admin
		if err := s.db.Select("status", "role").First(&admin, id).Error; err != nil {
			if isNotFound(err) {
				response.NotFound(c, "管理员不存在")
			} else {
				response.ServerError(c, "账号状态校验失败")
			}
			c.Abort()
			return
		}
		if admin.Status != model.StatusActive {
			response.Forbidden(c, "账号已被封禁或禁用")
			c.Abort()
			return
		}
		c.Set(ctxAdminRole, admin.Role)
		c.Next()
	}
}

// requireAdminRole 限定只有指定角色（或超管 admin）可访问。挂在 requireActiveAdmin 之后。
func (s *Server) requireAdminRole(allowed ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get(ctxAdminRole)
		r, _ := role.(string)
		if r == model.AdminRoleAdmin { // 超管放行一切
			c.Next()
			return
		}
		for _, a := range allowed {
			if r == a {
				c.Next()
				return
			}
		}
		response.Forbidden(c, "当前角色无权执行该操作")
		c.Abort()
	}
}

func (s *Server) requireActiveAccount(
	expectedSubject string,
	loadStatus func(uint64) (string, error),
	notFoundMessage string,
	bannedMessage string,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		if middleware.CurrentSubject(c) != expectedSubject {
			c.Next()
			return
		}
		id := middleware.CurrentID(c)
		status, err := loadStatus(id)
		if err != nil {
			if isNotFound(err) {
				response.NotFound(c, notFoundMessage)
			} else {
				response.ServerError(c, "账号状态校验失败")
			}
			c.Abort()
			return
		}
		if status != model.StatusActive {
			response.Forbidden(c, bannedMessage)
			c.Abort()
			return
		}
		c.Next()
	}
}
