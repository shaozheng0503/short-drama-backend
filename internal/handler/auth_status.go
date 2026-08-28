package handler

import (
	"net/http"
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
const ctxAdminPermissions = "admin.permissions"

// requireActiveAdmin 校验管理员账号正常，并把角色和权限列表写入 context。
// 权限列表一次性查出，后续 requirePermission 从 context 读取，零额外 DB 开销。
func (s *Server) requireActiveAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if middleware.CurrentSubject(c) != middleware.SubjectAdmin {
			c.Next()
			return
		}
		id := middleware.CurrentID(c)
		var admin model.Admin
		if err := s.db.Select("status", "role", "region").First(&admin, id).Error; err != nil {
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
		c.Set(ctxAdminRegion, admin.Region)

		// 查权限列表（超管快捷跳过，减少 DB 查询）
		if admin.Role == model.AdminRoleAdmin {
			c.Set(ctxAdminPermissions, []string{model.PermSuperAdmin})
		} else {
			var perms []string
			s.db.Model(&model.AdminPermission{}).
				Where("admin_id = ?", id).Pluck("permission", &perms)
			c.Set(ctxAdminPermissions, perms)
		}

		c.Next()
	}
}

func adminIsSuper(c *gin.Context) bool {
	role, _ := c.Get(ctxAdminRole)
	r, _ := role.(string)
	if r == model.AdminRoleAdmin {
		return true
	}
	perms, _ := c.Get(ctxAdminPermissions)
	permList, _ := perms.([]string)
	for _, p := range permList {
		if p == model.PermSuperAdmin {
			return true
		}
	}
	return false
}

// ============================================================
// 地区管理员（region_admin）—— 数据范围与权限收口
//
// 设计（2026-08-25 需求）：
//  1. 只能查看「本地区（creators.region = admins.region，精确到市）」的
//     创作者及其发布的作品（短剧、剧集元数据），不包括视频地址；
//  2. 没有审核权限（approve/reject 全拒）；
//  3. 没有其他所有权限（写操作、财务、配置、用户管理、渠道账号、合同、
//     发行商、订单、提现、结算等全部 403）。
//
// 实现：requireActiveAdmin 已把 admin.Role 写进 context；
// adminIsRegionAdmin 判定角色；restrictRegionAdmin 统一拦截：
//   - 白名单 = 允许的只读接口前缀（登录态、创作者列表/详情、短剧列表/详情、剧集列表）；
//   - 白名单内再由各 handler 做 region 过滤（本文件 regionFilter 系列辅助）；
//   - 白名单外一律 403。
// ============================================================

const ctxAdminRegion = "admin.region"

// adminIsRegionAdmin 当前登录管理员是否地区管理员。
func adminIsRegionAdmin(c *gin.Context) bool {
	role, _ := c.Get(ctxAdminRole)
	r, _ := role.(string)
	return r == model.AdminRoleRegionAdmin
}

// regionAdminAllowedActions 地区管理员可用接口：key=路径前缀，value=允许的 HTTP 方法。
// 默认拒（deny by default）：不在下表内的任何方法/路径一律 403，包括审核、上下架、
// 增删改、导入、导出、刷新 VOD、剧集 preview（返回视频播放地址）等——
// 地区管理员没有任何写权限，也拿不到任何视频地址。
//
// 路径均按 HasPrefix 匹配，因此：
//   - "/v1/admin/creators" + GET 覆盖 GET /creators（列表）与 GET /creators/:id（详情），
//     同前缀下的 POST/PUT/DELETE（创建/导入/封禁/认证审核）全部 403；
//   - "/v1/admin/dramas" + GET 覆盖 GET /dramas（列表）、GET /dramas/:id（详情）、
//     GET /dramas/:id/episodes（剧集列表），同前缀下所有写操作与审核 403。
//   - 剧集独立路由 /v1/admin/episodes/*（preview 返回视频播放地址）不放行。
var regionAdminAllowedActions = map[string][]string{
	"/v1/admin/auth/refresh": {http.MethodPost}, // 登录态续期（非数据操作）
	"/v1/admin/me":           {http.MethodGet}, // 自身信息
	"/v1/admin/creators":     {http.MethodGet}, // 创作者列表 + 详情
	"/v1/admin/dramas":       {http.MethodGet}, // 短剧列表 + 详情 + 剧集列表
}

// regionAdminPathAllowed 地区管理员是否可访问「方法 + 路径」。
func regionAdminPathAllowed(method, path string) bool {
	// 显式排除：同前缀下的导出类只读接口（模板下载等），不属于「查看创作者/作品」范畴。
	for _, banned := range regionAdminBannedPaths {
		if path == banned {
			return false
		}
	}
	for prefix, methods := range regionAdminAllowedActions {
		if strings.HasPrefix(path, prefix) {
			for _, m := range methods {
				if m == method {
					return true
				}
			}
			// 前缀命中但方法不在允许列表：直接拒（不再看其他前缀，避免误放行）。
			return false
		}
	}
	return false
}

// regionAdminBannedPaths 前缀白名单内但需显式排除的精确路径（导出/下载类）。
var regionAdminBannedPaths = []string{
	"/v1/admin/creators/template.xlsx", // 批量导入模板下载
}

// restrictRegionAdmin 地区管理员权限围栏：挂在 requireActiveAdmin 之后。
// 白名单内放行（数据范围由各 handler 的 region 过滤保证），白名单外 403。
func (s *Server) restrictRegionAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !adminIsRegionAdmin(c) {
			c.Next()
			return
		}
		if regionAdminPathAllowed(c.Request.Method, c.Request.URL.Path) {
			c.Next()
			return
		}
		response.Forbidden(c, "地区管理员仅可查看本地区创作者及其作品（只读）")
		c.Abort()
	}
}

// regionScope 当前管理员的数据范围地区；非地区管理员返回空串（不限制）。
func regionScope(c *gin.Context) string {
	if !adminIsRegionAdmin(c) {
		return ""
	}
	region, _ := c.Get(ctxAdminRegion)
	r, _ := region.(string)
	return r
}

// regionScopedCreatorIDs 地区管理员可见的创作者 ID 集合。
// 返回 nil 表示不限制（非地区管理员）；返回空 map 表示本地区暂无创作者。
func (s *Server) regionScopedCreatorIDs(c *gin.Context) map[uint64]bool {
	scope := regionScope(c)
	if scope == "" {
		return nil
	}
	ids := map[uint64]bool{}
	var list []uint64
	s.db.Model(&model.Creator{}).Where("region = ?", scope).Pluck("id", &list)
	for _, id := range list {
		ids[id] = true
	}
	return ids
}

// regionAdminCanSeeCreator 地区管理员是否可见该创作者。
// region 为空（未填写地区）的创作者地区管理员不可见。
func (s *Server) regionAdminCanSeeCreator(c *gin.Context, creatorID uint64) bool {
	scope := regionScope(c)
	if scope == "" {
		return true
	}
	if creatorID == 0 {
		return false
	}
	var cnt int64
	s.db.Model(&model.Creator{}).Where("id = ? AND region = ?", creatorID, scope).Count(&cnt)
	return cnt > 0
}

// requireAdminRole 限定只有指定角色（或超管 admin）可访问。挂在 requireActiveAdmin 之后。
// 向后兼容：保留原有角色判定，同时支持权限项判定（拥有对应权限也放行）。
func (s *Server) requireAdminRole(allowed ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if adminIsSuper(c) {
			c.Next()
			return
		}
		role, _ := c.Get(ctxAdminRole)
		r, _ := role.(string)
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

// requirePermission 校验当前管理员是否拥有指定权限项。
// 超管（role=admin 或拥有 super_admin 权限）恒放行。
// 非超管从 context 中读取权限列表（由 requireActiveAdmin 一次性查出）。
func (s *Server) requirePermission(perm string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if adminIsSuper(c) {
			c.Next()
			return
		}
		perms, _ := c.Get(ctxAdminPermissions)
		permList, _ := perms.([]string)
		for _, p := range permList {
			if p == perm {
				c.Next()
				return
			}
		}
		response.Forbidden(c, "当前账号无此操作权限")
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
