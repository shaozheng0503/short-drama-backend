package handler

import (
	"strings"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// ============================================================
// 管理员账号管理 CRUD + 权限分配
// ============================================================

// GET /v1/admin/admins — 管理员列表（分页，含权限）
// 2026-08-25 加：role / region 筛选——超管按省市查看地区管理员。
func (s *Server) adminListAdmins(c *gin.Context) {
	page, pageSize := paginate(c)

	q := s.db.Model(&model.Admin{})
	if v := strings.TrimSpace(c.Query("role")); v != "" {
		q = q.Where("role = ?", v)
	}
	// region：模糊匹配（可只传省，如「广东省」，也可传省+市）。
	if v := strings.TrimSpace(c.Query("region")); v != "" {
		q = q.Where("region ILIKE ?", "%"+v+"%")
	}
	var total int64
	q.Count(&total)

	var admins []model.Admin
	q.Order("id ASC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&admins)

	// 批量查权限
	adminIDs := make([]uint64, 0, len(admins))
	for _, a := range admins {
		adminIDs = append(adminIDs, a.ID)
	}
	permMap := map[uint64][]string{}
	if len(adminIDs) > 0 {
		var perms []model.AdminPermission
		s.db.Where("admin_id IN ?", adminIDs).Find(&perms)
		for _, p := range perms {
			permMap[p.AdminID] = append(permMap[p.AdminID], p.Permission)
		}
	}

	list := make([]gin.H, 0, len(admins))
	for _, a := range admins {
		perms := permMap[a.ID]
		if perms == nil {
			perms = []string{}
		}
		list = append(list, adminDetailView(a, perms))
	}

	response.OK(c, pageResp(list, page, pageSize, total))
}

// POST /v1/admin/admins — 创建管理员账号
// 2026-08-25 加：支持创建地区管理员（role=region_admin，必填 region 精确到市，可填 remark）。
func (s *Server) adminCreateAdminAccount(c *gin.Context) {
	var req struct {
		Username    string   `json:"username" binding:"required"`
		Password    string   `json:"password" binding:"required"`
		Role        string   `json:"role"`
		Region      string   `json:"region"`
		Remark      string   `json:"remark"`
		Permissions []string `json:"permissions"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "username 与 password 必填")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if len(req.Username) < 2 || len(req.Username) > 64 {
		response.InvalidParam(c, "用户名长度须 2-64 字符")
		return
	}
	if len(req.Password) < 6 {
		response.InvalidParam(c, "密码长度至少 6 位")
		return
	}

	// 地区管理员：region 必填（精确到市，如「广东省深圳市」）；普通管理员不接收 region/remark。
	req.Region = strings.TrimSpace(req.Region)
	req.Remark = strings.TrimSpace(req.Remark)
	if req.Role == model.AdminRoleRegionAdmin {
		if req.Region == "" {
			response.InvalidParam(c, "地区管理员必须填写负责地区（region，精确到市）")
			return
		}
		if runeLen(req.Region) > adminRegionMaxRune {
			response.InvalidParam(c, "region 过长（最长 64 个字符）")
			return
		}
		// 地区管理员不带任何权限项：权限完全由 role 围栏收口（只读本地区）。
		if len(req.Permissions) > 0 {
			response.InvalidParam(c, "地区管理员不支持配置权限项（角色已内置只读权限）")
			return
		}
	} else if req.Role != "" && req.Role != model.AdminRoleAdmin {
		response.InvalidParam(c, "role 只能是 admin / region_admin（留空为普通权限项管理员）")
		return
	}
	if runeLen(req.Remark) > adminRemarkMaxRune {
		response.InvalidParam(c, "remark 过长（最长 255 个字符）")
		return
	}
	if !validPermissions(req.Permissions) {
		response.InvalidParam(c, "权限项不合法")
		return
	}

	// 用户名唯一校验
	var count int64
	s.db.Model(&model.Admin{}).Where("username = ?", req.Username).Count(&count)
	if count > 0 {
		response.InvalidParam(c, "用户名已存在")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), s.cfg.BcryptCost)
	if err != nil {
		response.ServerError(c, "密码加密失败")
		return
	}

	admin := model.Admin{
		Username:     req.Username,
		PasswordHash: string(hash),
		Role:         "", // 新建账号默认不绑定角色，纯权限项制；region_admin 例外（下方覆盖）
		Status:       model.StatusActive,
	}
	if req.Role == model.AdminRoleRegionAdmin {
		admin.Role = model.AdminRoleRegionAdmin
		admin.Region = req.Region
		admin.Remark = req.Remark
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&admin).Error; err != nil {
			return err
		}
		for _, perm := range req.Permissions {
			if err := tx.Create(&model.AdminPermission{
				AdminID:    admin.ID,
				Permission: perm,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if isUniqueViolation(err) {
			response.InvalidParam(c, "用户名已存在（并发冲突）")
			return
		}
		response.ServerError(c, "创建管理员失败")
		return
	}

	response.OK(c, adminDetailView(admin, req.Permissions))
}

// GET /v1/admin/admins/:id — 管理员详情（含权限列表）
func (s *Server) adminGetAdminAccount(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var admin model.Admin
	if err := s.db.First(&admin, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "管理员不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	perms := s.loadAdminPermissions(id)
	response.OK(c, adminDetailView(admin, perms))
}

// PUT /v1/admin/admins/:id — 更新管理员（用户名/状态/region/remark）
// 2026-08-25 加：支持更新地区管理员的 region（负责地区）与 remark（备注）。
func (s *Server) adminUpdateAdminAccount(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req struct {
		Username *string `json:"username"`
		Status   *string `json:"status"`
		Region   *string `json:"region"`
		Remark   *string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "参数格式错误")
		return
	}

	var admin model.Admin
	if err := s.db.First(&admin, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "管理员不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}

	// 安全约束：不可封禁自己
	currentID := middleware.CurrentID(c)
	if id == currentID && req.Status != nil && *req.Status == model.StatusBanned {
		response.Forbidden(c, "不可封禁自己")
		return
	}

	updates := map[string]interface{}{}
	if req.Username != nil {
		username := strings.TrimSpace(*req.Username)
		if len(username) < 2 || len(username) > 64 {
			response.InvalidParam(c, "用户名长度须 2-64 字符")
			return
		}
		// 唯一性校验（排除自身）
		var count int64
		s.db.Model(&model.Admin{}).Where("username = ? AND id <> ?", username, id).Count(&count)
		if count > 0 {
			response.InvalidParam(c, "用户名已存在")
			return
		}
		updates["username"] = username
	}
	if req.Status != nil {
		if *req.Status != model.StatusActive && *req.Status != model.StatusBanned {
			response.InvalidParam(c, "状态值不合法")
			return
		}
		updates["status"] = *req.Status
	}
	// region / remark：仅地区管理员账号可改（超管自己创建时怎么填，后续也只对 region_admin 生效）。
	if req.Region != nil {
		region := strings.TrimSpace(*req.Region)
		if region == "" {
			response.InvalidParam(c, "region 不可清空（如需取消地区管理员请调整账号）")
			return
		}
		if runeLen(region) > adminRegionMaxRune {
			response.InvalidParam(c, "region 过长（最长 64 个字符）")
			return
		}
		updates["region"] = region
	}
	if req.Remark != nil {
		remark := strings.TrimSpace(*req.Remark)
		if runeLen(remark) > adminRemarkMaxRune {
			response.InvalidParam(c, "remark 过长（最长 255 个字符）")
			return
		}
		updates["remark"] = remark
	}

	if len(updates) > 0 {
		if err := s.db.Model(&admin).Updates(updates).Error; err != nil {
			if isUniqueViolation(err) {
				response.InvalidParam(c, "用户名已存在（并发冲突）")
				return
			}
			response.ServerError(c, "更新失败")
			return
		}
	}

	s.db.First(&admin, id)
	perms := s.loadAdminPermissions(id)
	response.OK(c, adminDetailView(admin, perms))
}

// PUT /v1/admin/admins/:id/password — 重置密码
func (s *Server) adminResetAdminPassword(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req struct {
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "password 必填")
		return
	}
	if len(req.Password) < 6 {
		response.InvalidParam(c, "密码长度至少 6 位")
		return
	}

	var admin model.Admin
	if err := s.db.First(&admin, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "管理员不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), s.cfg.BcryptCost)
	if err != nil {
		response.ServerError(c, "密码加密失败")
		return
	}
	if err := s.db.Model(&admin).Update("password_hash", string(hash)).Error; err != nil {
		response.ServerError(c, "重置密码失败")
		return
	}
	response.OK(c, gin.H{"id": admin.ID, "message": "密码已重置"})
}

// PUT /v1/admin/admins/:id/permissions — 设置权限（整体覆盖）
func (s *Server) adminSetAdminPermissions(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req struct {
		Permissions []string `json:"permissions"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "参数格式错误")
		return
	}
	if !validPermissions(req.Permissions) {
		response.InvalidParam(c, "权限项不合法")
		return
	}

	var admin model.Admin
	if err := s.db.First(&admin, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "管理员不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}

	// 安全约束：不可撤销自身的 account_manage 权限（防止锁死）
	currentID := middleware.CurrentID(c)
	if id == currentID {
		hasAccountManage := false
		for _, p := range req.Permissions {
			if p == model.PermAccountManage || p == model.PermSuperAdmin {
				hasAccountManage = true
				break
			}
		}
		if !hasAccountManage {
			response.Forbidden(c, "不可撤销自身的系统用户管理权限")
			return
		}
	}

	// 超管保护：不可撤销超管的 super_admin
	if admin.Role == model.AdminRoleAdmin {
		hasSuper := false
		for _, p := range req.Permissions {
			if p == model.PermSuperAdmin {
				hasSuper = true
				break
			}
		}
		if !hasSuper {
			response.Forbidden(c, "不可撤销超级管理员的 super_admin 权限")
			return
		}
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 先删后建（整体覆盖）
		if err := tx.Where("admin_id = ?", id).Delete(&model.AdminPermission{}).Error; err != nil {
			return err
		}
		for _, perm := range req.Permissions {
			if err := tx.Create(&model.AdminPermission{
				AdminID:    id,
				Permission: perm,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		response.ServerError(c, "权限设置失败")
		return
	}

	perms := s.loadAdminPermissions(id)
	response.OK(c, gin.H{
		"id":          admin.ID,
		"username":    admin.Username,
		"permissions": perms,
	})
}

// POST /v1/admin/admins/:id/ban — 封禁
func (s *Server) adminBanAdminAccount(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	currentID := middleware.CurrentID(c)
	if id == currentID {
		response.Forbidden(c, "不可封禁自己")
		return
	}

	var admin model.Admin
	if err := s.db.First(&admin, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "管理员不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	if admin.Role == model.AdminRoleAdmin {
		response.Forbidden(c, "不可封禁超级管理员")
		return
	}
	if err := s.db.Model(&admin).Update("status", model.StatusBanned).Error; err != nil {
		response.ServerError(c, "封禁失败")
		return
	}
	response.OK(c, gin.H{"id": admin.ID, "status": model.StatusBanned})
}

// POST /v1/admin/admins/:id/unban — 解封
func (s *Server) adminUnbanAdminAccount(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var admin model.Admin
	if err := s.db.First(&admin, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "管理员不存在")
			return
		}
		response.ServerError(c, "查询失败")
		return
	}
	if err := s.db.Model(&admin).Update("status", model.StatusActive).Error; err != nil {
		response.ServerError(c, "解封失败")
		return
	}
	response.OK(c, gin.H{"id": admin.ID, "status": model.StatusActive})
}

// GET /v1/admin/permissions — 获取全部权限项列表
func (s *Server) adminListPermissions(c *gin.Context) {
	response.OK(c, gin.H{
		"list": model.AllPermissions,
	})
}

// ============================================================
// 辅助函数
// ============================================================

// loadAdminPermissions 查询某管理员的权限列表
func (s *Server) loadAdminPermissions(adminID uint64) []string {
	var perms []string
	s.db.Model(&model.AdminPermission{}).
		Where("admin_id = ?", adminID).
		Pluck("permission", &perms)
	if perms == nil {
		perms = []string{}
	}
	return perms
}

// validPermissions 校验权限项列表是否全部合法
func validPermissions(perms []string) bool {
	validSet := map[string]bool{}
	for _, p := range model.AllPermissions {
		validSet[p.Key] = true
	}
	for _, p := range perms {
		if !validSet[p] {
			return false
		}
	}
	// super_admin 与其他权限互斥
	hasSuper := false
	hasOther := false
	for _, p := range perms {
		if p == model.PermSuperAdmin {
			hasSuper = true
		} else {
			hasOther = true
		}
	}
	if hasSuper && hasOther {
		return false
	}
	return true
}

// adminDetailView 构造管理员详情视图
// 2026-08-25 加：region / remark（地区管理员专属字段）。
func adminDetailView(admin model.Admin, perms []string) gin.H {
	return gin.H{
		"id":          admin.ID,
		"username":    admin.Username,
		"role":        admin.Role,
		"region":      admin.Region,
		"remark":      admin.Remark,
		"status":      admin.Status,
		"permissions": perms,
		"created_at":  admin.CreatedAt,
		"updated_at":  admin.UpdatedAt,
	}
}

const (
	adminRegionMaxRune = 64  // 地区（省+市）最长字符数，与 creators.region 同口径
	adminRemarkMaxRune = 255 // 备注最长字符数
)

// parseUintParam 解析路径参数为 uint64
func parseUintParam(c *gin.Context, key string) (uint64, bool) {
	val := c.Param(key)
	id := parseUint(val)
	if id == 0 {
		response.InvalidParam(c, key+" 参数不合法")
		return 0, false
	}
	return id, true
}
