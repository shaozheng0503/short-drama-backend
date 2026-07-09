package handler

import (
	"strings"

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

// 0.15.0 发行商账号激活校验
func (s *Server) requireActiveDistributor() gin.HandlerFunc {
	return s.requireActiveAccount(middleware.SubjectDistributor, func(id uint64) (string, error) {
		var d model.Distributor
		if err := s.db.Select("status").First(&d, id).Error; err != nil {
			return "", err
		}
		return d.Status, nil
	}, "发行商不存在", "账号已被封禁或禁用")
}

// requireVerifiedCreator 限定只有实名认证通过(verified)的创作者才能执行上传 / 建剧 / 提交审核等写操作。
// 对应需求「没认证通过不能上传还有相关操作」。挂在 requireActiveCreator 之后（账号正常前提下再校验认证）。
// 未通过认证统一返回 40301 + need_verification 标记，前端据此把用户引导到实名认证页。
// 只读接口（列表 / 详情 / 拉默认值）与认证提交接口本身不挂此中间件，否则没法完成认证。
//
// 2026-07-02 修：吴建棉 14:07 反馈「只做企业认证，上传不了营业执照」——
// 死锁问题：要做企业认证要传营业执照图片 → 传图片要 image-sign → image-sign 挂 verified → verified 要做完企业认证。
// 解法：white-list 凡是「做认证本身需要」的 path（/verification/、/bank-card/、/uploads/），verified 拦截器放行。
// 风险评估：image-sign 只是签 cos URL 拿上传地址，不会真存到库；最多被作恶者拿来签 cos URL 传图到平台桶。
// 但 cos 桶的 key 前缀/路径后续可在 handler 里加强校验（如要求传图必须用 business-license/ 前缀），先做最小白名单。
func (s *Server) requireVerifiedCreator() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 2026-07-02 修：白名单，做认证本身需要的接口放行
		path := c.Request.URL.Path
		if isVerificationRelatedPath(path) {
			c.Next()
			return
		}
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

// isVerificationRelatedPath 2026-07-02 修：「做认证本身需要」的 path 白名单
// 包括：提交个人/企业认证、提交企业认证 OCR、换绑银行卡、上传文件（image-sign/vod-sign 给营业执照/身份证/银行卡用）
func isVerificationRelatedPath(path string) bool {
	whiteList := []string{
		"/v1/creator/verification/",     // 个人/企业/OCR 提交
		"/v1/creator/bank-card/",        // 换绑银行卡（含 4 要素验证）
		"/v1/creator/uploads/",          // image-sign / vod-sign（做认证要传图）
	}
	for _, p := range whiteList {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
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

func adminIsSuper(c *gin.Context) bool {
	role, _ := c.Get(ctxAdminRole)
	r, _ := role.(string)
	return r == model.AdminRoleAdmin
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
