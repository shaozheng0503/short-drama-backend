package handler

import (
	"net/http"
	"testing"
)

// TestRegionAdminPathAllowed 地区管理员路由白名单：只放行「本地区创作者/短剧的只读接口」。
// 2026-08-25 需求：只有查看本地区创作者及其发布作品（不含视频）的权限，
// 没有审核权限，也没有其他所有权限。
func TestRegionAdminPathAllowed(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   bool
	}{
		// 登录态
		{http.MethodPost, "/v1/admin/auth/refresh", true},
		{http.MethodGet, "/v1/admin/auth/refresh", false},

		// 自身信息
		{http.MethodGet, "/v1/admin/me", true},
		{http.MethodPut, "/v1/admin/me", false},

		// 创作者：只读放行
		{http.MethodGet, "/v1/admin/creators", true},
		{http.MethodGet, "/v1/admin/creators/123", true},
		// 创作者：写/审核全拒
		{http.MethodPost, "/v1/admin/creators", false},
		{http.MethodPost, "/v1/admin/creators/import", false},
		{http.MethodGet, "/v1/admin/creators/template.xlsx", false}, // 导出模板 = 导出类操作，拒
		{http.MethodPut, "/v1/admin/creators/123", false},
		{http.MethodPost, "/v1/admin/creators/123/ban", false},
		{http.MethodPost, "/v1/admin/creators/123/unban", false},
		{http.MethodPost, "/v1/admin/creators/123/verification/approve", false},
		{http.MethodPost, "/v1/admin/creators/123/verification/reject", false},

		// 短剧：只读放行（列表/详情/剧集列表）
		{http.MethodGet, "/v1/admin/dramas", true},
		{http.MethodGet, "/v1/admin/dramas/456", true},
		{http.MethodGet, "/v1/admin/dramas/456/episodes", true},
		// 短剧：写/审核/上下架全拒
		{http.MethodPost, "/v1/admin/dramas", false},
		{http.MethodPut, "/v1/admin/dramas/456", false},
		{http.MethodDelete, "/v1/admin/dramas/456", false},
		{http.MethodPost, "/v1/admin/dramas/456/publish", false},
		{http.MethodPost, "/v1/admin/dramas/456/offline", false},
		{http.MethodPost, "/v1/admin/dramas/456/approve", false},
		{http.MethodPost, "/v1/admin/dramas/456/reject", false},
		{http.MethodPost, "/v1/admin/dramas/456/audit", false},
		{http.MethodPost, "/v1/admin/dramas/456/sendback", false},
		{http.MethodPut, "/v1/admin/dramas/456/distributable", false},
		{http.MethodPut, "/v1/admin/dramas/456/ad-unlock", false},
		{http.MethodPost, "/v1/admin/dramas/456/episodes", false},
		{http.MethodPost, "/v1/admin/dramas/456/episodes/batch", false},
		{http.MethodPut, "/v1/admin/dramas/456/episodes/reorder", false},

		// 剧集独立路由：不放行（preview 返回视频播放地址，地区管理员不可见视频）
		{http.MethodGet, "/v1/admin/episodes/789/preview", false},
		{http.MethodPut, "/v1/admin/episodes/789", false},
		{http.MethodDelete, "/v1/admin/episodes/789", false},
		{http.MethodPost, "/v1/admin/episodes/789/refresh-vod", false},

		// 其他所有模块：全拒
		{http.MethodGet, "/v1/admin/dashboard", false},
		{http.MethodGet, "/v1/admin/admins", false},
		{http.MethodPost, "/v1/admin/admins", false},
		{http.MethodGet, "/v1/admin/users", false},
		{http.MethodGet, "/v1/admin/orders", false},
		{http.MethodGet, "/v1/admin/withdrawals", false},
		{http.MethodPost, "/v1/admin/withdrawals/1/approve", false},
		{http.MethodGet, "/v1/admin/settlements", false},
		{http.MethodGet, "/v1/admin/finance/app-income", false},
		{http.MethodGet, "/v1/admin/config/pricing", false},
		{http.MethodPut, "/v1/admin/config/pricing", false},
		{http.MethodGet, "/v1/admin/contracts", false},
		{http.MethodGet, "/v1/admin/distributors", false},
		{http.MethodGet, "/v1/admin/distributor-claims", false},
		{http.MethodPost, "/v1/admin/distributor-claims/1/approve", false},
		{http.MethodGet, "/v1/admin/comments", false},
		{http.MethodGet, "/v1/admin/categories", false},
		{http.MethodPost, "/v1/admin/uploads/vod-sign", false},
		{http.MethodGet, "/v1/admin/creator-channel-accounts", false},
	}

	for _, tc := range cases {
		if got := regionAdminPathAllowed(tc.method, tc.path); got != tc.want {
			t.Errorf("regionAdminPathAllowed(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}
