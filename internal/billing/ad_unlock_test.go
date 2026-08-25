package billing

import (
	"testing"

	"ai-drama-platform/internal/config"
)

// TestParseCSJEcpmCents 验证 GroMore 回调 ecpm → 单次收益（分）换算。
// 单位口径：CSJ_ECPM_UNIT=fen（默认，SDK getEcpm() 文档口径）/ yuan（元）。
func TestParseCSJEcpmCents(t *testing.T) {
	cases := []struct {
		name string
		unit string
		in   string
		want int64
	}{
		// 默认按分：单次收益（分）= ecpm / 1000
		{"fen ecpm=3000 → 3分", "fen", "3000", 3},
		{"fen ecpm=500 → 1分(四舍五入)", "fen", "500", 1},
		{"fen ecpm=499 → 0分", "fen", "499", 0},
		{"fen ecpm=12500 → 13分(四舍五入)", "fen", "12500", 13},
		{"fen 带空格", "fen", " 3000 ", 3},

		// 按元：单次收益（分）= ecpm × 100 / 1000
		{"yuan ecpm=30 → 3分", "yuan", "30", 3},
		{"yuan ecpm=0.5 → 0分", "yuan", "0.5", 0},
		{"yuan ecpm=12.5 → 1分(四舍五入)", "yuan", "12.5", 1},
		{"yuan 大写 YUAN 也认", "YUAN", "30", 3},

		// 异常值：全部返回 0，调用方跳过记账
		{"空串", "fen", "", 0},
		{"null 字面量", "fen", "null", 0},
		{"NULL 字面量", "fen", "NULL", 0},
		{"非数字", "fen", "abc", 0},
		{"负数", "fen", "-3000", 0},
		{"零", "fen", "0", 0},
		{"未知单位按分兜底", "xxx", "3000", 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Service{cfg: config.Config{CSJEcpmUnit: c.unit}}
			if got := s.parseCSJEcpmCents(c.in); got != c.want {
				t.Errorf("parseCSJEcpmCents(%q, unit=%q) = %d, want %d", c.in, c.unit, got, c.want)
			}
		})
	}
}
