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

func (s *Server) requireActiveAdmin() gin.HandlerFunc {
	return s.requireActiveAccount(middleware.SubjectAdmin, func(id uint64) (string, error) {
		var admin model.Admin
		if err := s.db.Select("status").First(&admin, id).Error; err != nil {
			return "", err
		}
		return admin.Status, nil
	}, "管理员不存在", "账号已被封禁或禁用")
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
