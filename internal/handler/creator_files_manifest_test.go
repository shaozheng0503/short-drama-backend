package handler

import "testing"

// cleanFileName —— Windows / macOS / Linux 通用文件名清洗
func TestCleanFileName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "总裁的逆袭", "总裁的逆袭"},
		{"win_illegal_all_9", `a\b/c:d*e?f"g<h>i|j`, "a_b_c_d_e_f_g_h_i_j"},
		{"control_char", "总裁\x00的\x01逆\x1F袭", "总裁的逆袭"},
		{"trailing_dot_space", "总裁的逆袭. ", "总裁的逆袭"},
		{"rune_truncate_30", "一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十百", "一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十"},
		{"all_to_underscore_no_fallback", `\\\\:::`, "_______"},
		{"only_dot_space_trim_to_empty", "...   ...", "untitled"},
		{"only_control", "\x00\x01\x02", "untitled"},
		{"normal_with_emoji", "总裁的逆袭🔥", "总裁的逆袭🔥"},
		{"trim_only", "   ", "untitled"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := cleanFileName(c.in)
			if got != c.want {
				t.Errorf("cleanFileName(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// inferExt —— URL 末段优先 / Content-Type 兜底
func TestInferExt(t *testing.T) {
	cases := []struct {
		name        string
		url         string
		contentType string
		want        string
	}{
		{"url_jpg", "https://cos.example.com/cover.jpg", "", "jpg"},
		{"url_png_query", "https://cos.example.com/x.png?sign=abc", "", "png"},
		{"url_uppercase", "https://cos.example.com/A.JPG", "", "jpg"},
		{"url_mp4", "https://vod.example.com/v.mp4", "", "mp4"},
		{"url_pdf", "https://cos.example.com/copyright.pdf", "", "pdf"},
		{"url_no_ext_fallback_ct", "https://cos.example.com/path", "image/png", "png"},
		{"ct_jpeg", "https://cos.example.com/path", "image/jpeg", "jpg"},
		{"ct_jpg_alias", "https://cos.example.com/path", "image/jpg", "jpg"},
		{"ct_webp", "https://cos.example.com/path", "image/webp", "webp"},
		{"ct_mp4", "https://cos.example.com/path", "video/mp4", "mp4"},
		{"ct_pdf", "https://cos.example.com/path", "application/pdf", "pdf"},
		{"unknown_fallback_bin", "https://cos.example.com/path", "application/octet-stream", "bin"},
		{"url_ext_too_long_fallback_ct", "https://cos.example.com/file.abcdef", "image/png", "png"},
		{"url_ext_with_special_fallback_ct", "https://cos.example.com/file.123abc", "image/png", "png"},
		{"empty_all", "", "", "bin"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := inferExt(c.url, c.contentType)
			if got != c.want {
				t.Errorf("inferExt(%q, %q) = %q, want %q", c.url, c.contentType, got, c.want)
			}
		})
	}
}

// buildRootDirName —— 顶级目录名格式：{剧名}_{剧ID}_素材包
func TestBuildRootDirName(t *testing.T) {
	cases := []struct {
		title string
		id    uint64
		want  string
	}{
		{"总裁的逆袭", 123, "总裁的逆袭_123_素材包"},
		{"总裁/逆袭", 456, "总裁_逆袭_456_素材包"},
		{"", 789, "untitled_789_素材包"},
		{"   ", 100, "untitled_100_素材包"},
	}
	for _, c := range cases {
		t.Run(c.title, func(t *testing.T) {
			got := buildRootDirName(c.title, c.id)
			if got != c.want {
				t.Errorf("buildRootDirName(%q, %d) = %q, want %q", c.title, c.id, got, c.want)
			}
		})
	}
}

// buildCoverRelPath —— 01_封面/cover_main.{ext} 或 cover_{n}.{ext}
func TestBuildCoverRelPath(t *testing.T) {
	cases := []struct {
		idx  int
		url  string
		want string
	}{
		{0, "https://cos.example.com/main.jpg", "01_封面/cover_main.jpg"},
		{1, "https://cos.example.com/c2.png", "01_封面/cover_2.png"},
		{2, "https://cos.example.com/c3.webp", "01_封面/cover_3.webp"},
		{4, "https://cos.example.com/c5", "01_封面/cover_5.bin"},
	}
	for _, c := range cases {
		t.Run(c.url, func(t *testing.T) {
			got := buildCoverRelPath(c.idx, c.url, "")
			if got != c.want {
				t.Errorf("buildCoverRelPath(%d, %q) = %q, want %q", c.idx, c.url, got, c.want)
			}
		})
	}
}

// buildEpisodeRelPath —— 02_集视频/{剧名} - 第 N 集.{ext}
//   注意：N 集顺序由后端 ORDER BY episode_no ASC 保证，前端按数组顺序写入 zip
func TestBuildEpisodeRelPath(t *testing.T) {
	cases := []struct {
		title    string
		epNo     int
		url      string
		want     string
	}{
		{"总裁的逆袭", 1, "https://vod.example.com/1.mp4", "02_集视频/总裁的逆袭 - 第 1 集.mp4"},
		{"总裁的逆袭", 10, "https://vod.example.com/10.mp4", "02_集视频/总裁的逆袭 - 第 10 集.mp4"},
		{"a/b:c", 2, "https://vod.example.com/2.mp4", "02_集视频/a_b_c - 第 2 集.mp4"},
		{"", 1, "https://vod.example.com/1.mp4", "02_集视频/untitled - 第 1 集.mp4"},
	}
	for _, c := range cases {
		t.Run(c.title, func(t *testing.T) {
			got := buildEpisodeRelPath(c.title, c.epNo, c.url, "")
			if got != c.want {
				t.Errorf("buildEpisodeRelPath(%q, %d, %q) = %q, want %q", c.title, c.epNo, c.url, got, c.want)
			}
		})
	}
}

// buildCopyrightRelPath —— 03_权属/copyright_{n}.{ext}
func TestBuildCopyrightRelPath(t *testing.T) {
	cases := []struct {
		idx  int
		url  string
		want string
	}{
		{0, "https://cos.example.com/cp1.pdf", "03_权属/copyright_1.pdf"},
		{1, "https://cos.example.com/cp2.png", "03_权属/copyright_2.png"},
		{9, "https://cos.example.com/cp10.jpg", "03_权属/copyright_10.jpg"},
	}
	for _, c := range cases {
		t.Run(c.url, func(t *testing.T) {
			got := buildCopyrightRelPath(c.idx, c.url, "")
			if got != c.want {
				t.Errorf("buildCopyrightRelPath(%d, %q) = %q, want %q", c.idx, c.url, got, c.want)
			}
		})
	}
}

// 端到端校验：3 类路径互不冲突
func TestPathsNoConflict(t *testing.T) {
	files := []string{
		buildRootDirName("测试剧", 1),
		buildCoverRelPath(0, "https://x/a.jpg", ""),
		buildCoverRelPath(1, "https://x/b.jpg", ""),
		buildEpisodeRelPath("测试剧", 1, "https://x/1.mp4", ""),
		buildEpisodeRelPath("测试剧", 10, "https://x/10.mp4", ""),
		buildCopyrightRelPath(0, "https://x/cp.pdf", ""),
		buildCopyrightRelPath(9, "https://x/cp10.jpg", ""),
	}
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f] {
			t.Errorf("path conflict: %s", f)
		}
		seen[f] = true
	}
	// 关键：第 10 集必须在第 2 集前？不对——我们要求后端按 episode_no ASC 排好
	// 前端不再依赖字典序，所以「第 10 集」排在「第 2 集」前是没问题的
	// 这里只验证所有 path 不冲突即可
}
