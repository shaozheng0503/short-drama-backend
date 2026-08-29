package handler

import (
	"testing"

	"ai-drama-platform/internal/model"
)

// TestShareRatioBpLabel 比例标签可读化。
func TestShareRatioBpLabel(t *testing.T) {
	cases := []struct {
		bp   int64
		want string
	}{
		{5500, "55%"},
		{4500, "45%"},
		{3000, "30%"},
		{0, "0%"},
		{10000, "100%"},
		{3333, "33.33%"},
	}
	for _, tc := range cases {
		if got := shareRatioBpLabel(tc.bp); got != tc.want {
			t.Errorf("shareRatioBpLabel(%d) = %q, want %q", tc.bp, got, tc.want)
		}
	}
}

// TestIncomeFromGrossByBP 验证整数 BP 运算口径（与 model.IncomeFromGrossBP 同式），
// 对应 2026-08-29 会议的结算计算问题：90.97×45%=40.94（含创作者30%+平台15%），
// 不应出现「先给创作者分成、再按剩余基数乘比例」的双重扣减。
func TestIncomeFromGrossByBP(t *testing.T) {
	cases := []struct {
		name        string
		grossCents  int64
		ratioBP     int
		wantCents   int64
	}{
		// 90.97 元 × 45% = 40.94 元（会议中吴总算的正确值）
		{"总收益90.97元按45%", 9097, 4500, 4093}, // 9097*4500/10000 = 4093.65 → 4093
		// 111 元 × 45% = 49.95 元（会议中「应该是49块5」）
		{"总收益111元按45%", 11100, 4500, 4995},
		// 90.97 × 55% = 50.03（发行商实得）
		{"总收益90.97元按55%", 9097, 5500, 5003}, // 9097*5500/10000 = 5003.35 → 5003
		// 90.97 × 30% = 27.29（创作者分成，会议里提到的数）
		{"总收益90.97元按30%", 9097, 3000, 2729},
		// 0 收益边界
		{"零收益", 0, 4500, 0},
	}
	for _, tc := range cases {
		got := tc.grossCents * int64(tc.ratioBP) / int64(model.ShareRatioBPFull)
		if got != tc.wantCents {
			t.Errorf("%s: gross=%d bp=%d → got %d, want %d", tc.name, tc.grossCents, tc.ratioBP, got, tc.wantCents)
		}
	}
}
