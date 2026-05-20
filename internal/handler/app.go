package handler

import (
	"errors"
	"time"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"
	"ai-drama-platform/internal/sms"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type appLoginRequest struct {
	Phone string `json:"phone" binding:"required"`
	Code  string `json:"code" binding:"required"`
}

func (s *Server) appLogin(c *gin.Context) {
	var req appLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "phone 与 code 必填")
		return
	}
	if !sms.ValidPhone(req.Phone) {
		response.InvalidParam(c, "手机号格式不正确")
		return
	}

	if err := s.sms.Verify(req.Phone, model.SMSScenAppLogin, req.Code); err != nil {
		if errors.Is(err, sms.ErrCodeMismatch) {
			response.InvalidParam(c, "验证码错误或已过期")
			return
		}
		response.ServerError(c, "校验验证码失败")
		return
	}

	user, isNew, err := s.findOrCreateAppUser(req.Phone)
	if err != nil {
		response.ServerError(c, "登录失败")
		return
	}
	if user.Status == model.StatusBanned {
		response.Forbidden(c, "账号已被封禁")
		return
	}

	token, _, err := middleware.IssueToken(s.cfg, middleware.SubjectApp, user.ID)
	if err != nil {
		response.ServerError(c, "签发 token 失败")
		return
	}

	response.OK(c, gin.H{
		"token":       token,
		"user":        appUserView(user),
		"is_new_user": isNew,
	})
}

func (s *Server) appMe(c *gin.Context) {
	uid := middleware.CurrentID(c)
	var user model.User
	if err := s.db.First(&user, uid).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "用户不存在")
			return
		}
		response.ServerError(c, "获取用户失败")
		return
	}
	response.OK(c, appUserView(user))
}

type appUpdateMeRequest struct {
	Nickname *string `json:"nickname"`
	Avatar   *string `json:"avatar"`
}

func (s *Server) appUpdateMe(c *gin.Context) {
	uid := middleware.CurrentID(c)
	var req appUpdateMeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}

	updates := map[string]interface{}{}
	if req.Nickname != nil {
		if len(*req.Nickname) == 0 || len(*req.Nickname) > 64 {
			response.InvalidParam(c, "昵称长度需在 1~64 之间")
			return
		}
		updates["nickname"] = *req.Nickname
	}
	if req.Avatar != nil {
		if len(*req.Avatar) > 512 {
			response.InvalidParam(c, "头像 URL 过长")
			return
		}
		updates["avatar"] = *req.Avatar
	}

	if len(updates) > 0 {
		updates["updated_at"] = time.Now()
		if err := s.db.Model(&model.User{}).Where("id = ?", uid).Updates(updates).Error; err != nil {
			response.ServerError(c, "更新失败")
			return
		}
	}

	var user model.User
	if err := s.db.First(&user, uid).Error; err != nil {
		response.ServerError(c, "获取用户失败")
		return
	}
	response.OK(c, appUserView(user))
}

func (s *Server) findOrCreateAppUser(phone string) (model.User, bool, error) {
	var user model.User
	err := s.db.Where("phone = ?", phone).First(&user).Error
	if err == nil {
		return user, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return user, false, err
	}
	user = model.User{
		Phone:    phone,
		Nickname: defaultNickname(phone),
		Status:   model.StatusActive,
	}
	if err := s.db.Create(&user).Error; err != nil {
		return user, false, err
	}
	return user, true, nil
}

func defaultNickname(phone string) string {
	if len(phone) >= 4 {
		return "用户" + phone[len(phone)-4:]
	}
	return "用户"
}

func appUserView(u model.User) gin.H {
	return gin.H{
		"id":       u.ID,
		"phone":    sms.MaskPhone(u.Phone),
		"nickname": u.Nickname,
		"avatar":   u.Avatar,
		"status":   u.Status,
	}
}
