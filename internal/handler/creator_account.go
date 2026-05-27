package handler

import (
	"strings"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"
	"ai-drama-platform/internal/sms"

	"github.com/gin-gonic/gin"
)

type creatorAccountRequest struct {
	Nickname  *string `json:"nickname"`
	AvatarURL *string `json:"avatar_url"`
}

func (s *Server) creatorGetAccount(c *gin.Context) {
	cid := middleware.CurrentID(c)
	var creator model.Creator
	if err := s.db.First(&creator, cid).Error; err != nil {
		response.ServerError(c, "查询账号信息失败")
		return
	}
	response.OK(c, gin.H{
		"account_uid": creator.AccountUID,
		"nickname":    creator.Nickname,
		"avatar_url":  creatorAvatarURL(creator),
		"login_phone": smsMaskCreatorPhone(creator.Phone),
	})
}

func (s *Server) creatorUpdateAccount(c *gin.Context) {
	cid := middleware.CurrentID(c)
	var req creatorAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}
	updates := map[string]interface{}{}
	if req.Nickname != nil {
		nickname := strings.TrimSpace(*req.Nickname)
		if nickname == "" || runeLen(nickname) > creatorNameMaxRune {
			response.InvalidParam(c, "nickname 长度需在 1~50 个字符之间")
			return
		}
		updates["nickname"] = nickname
	}
	if req.AvatarURL != nil {
		if len(*req.AvatarURL) > 512 {
			response.InvalidParam(c, "avatar_url 过长")
			return
		}
		updates["avatar_url"] = *req.AvatarURL
	}
	if len(updates) > 0 {
		if err := s.db.Model(&model.Creator{}).Where("id = ?", cid).Updates(updates).Error; err != nil {
			response.ServerError(c, "更新账号信息失败")
			return
		}
	}
	s.creatorGetAccount(c)
}

func smsMaskCreatorPhone(phone string) string {
	return sms.MaskPhone(phone)
}
