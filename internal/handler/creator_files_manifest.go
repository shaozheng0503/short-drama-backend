package handler

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

// 2026-07-06 加：一键下载全部素材（前端用 JSZip 打包）
// 设计思路：后端只下发文件清单（不打包），前端 fetch + JSZip
//   理由：避免后端 5GB zip 的内存/流量/超时问题
//   资产范围（MVP）：封面 + 集视频 + 权属（3 类）
//   后续可加：合同扫描件（独立接口）、承诺函、成本、角色
//
// 命名规则（已和创作者确认）：
//   顶级目录：{剧名}_{剧ID}_素材包
//   01_封面/cover_main.{ext}         主封面
//   01_封面/cover_{n}.{ext}          n=2..5 多图封面
//   02_集视频/{剧名} - 第 N 集.{ext}  按 episode_no 升序
//   03_权属/copyright_{n}.{ext}       n=1..10 权属文件
//
// 排序保证：后端 ORDER BY 排好，前端按数组顺序写入 zip central directory
//   （不依赖文件名字典序，因为「第 1 集」字典序会在「第 2 集」前，
//    但「第 10 集」也会在「第 2 集」前——会乱）

// 文件名清洗（Win/macOS/Linux 通用）
//   9 个 Windows 非法字符替换为 _：\ / : * ? " < > |
//   控制字符 \x00-\x1F 删除
//   末尾 . 和空格删除（Windows 不允许）
//   长度限制（rune 数）30 字
//   全部清洗后为空 → 兜底 "untitled"
func cleanFileName(s string) string {
	replacer := strings.NewReplacer(
		"\\", "_", "/", "_", ":", "_", "*", "_",
		"?", "_", `"`, "_", "<", "_", ">", "_", "|", "_",
	)
	s = replacer.Replace(s)
	// 控制字符删除
	s = strings.Map(func(r rune) rune {
		if r < 32 {
			return -1
		}
		return r
	}, s)
	// 末尾 . 和空格（Windows 不允许文件以 . 或空格结尾）
	s = strings.TrimRight(s, " .")
	s = strings.TrimSpace(s)
	// 长度限制
	if utf8.RuneCountInString(s) > 30 {
		s = string([]rune(s)[:30])
	}
	if s == "" {
		s = "untitled"
	}
	return s
}

// inferExt 从 URL 末段 / Content-Type 推扩展名
//   优先 URL 末段（.jpg / .png / .mp4 等）
//   次之 Content-Type 映射
//   最后兜底 .bin
func inferExt(rawURL, contentType string) string {
	// 1) URL 末段
	if u, err := url.Parse(rawURL); err == nil {
		path := u.Path
		if idx := strings.LastIndex(path, "."); idx != -1 {
			ext := path[idx+1:]
			if len(ext) >= 1 && len(ext) <= 5 && isAlnumLower(ext) {
				return strings.ToLower(ext)
			}
		}
	}
	// 2) Content-Type
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/bmp":
		return "bmp"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	case "video/mp4":
		return "mp4"
	case "video/quicktime":
		return "mov"
	case "application/pdf":
		return "pdf"
	}
	return "bin"
}

func isAlnumLower(s string) bool {
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return len(s) > 0
}

// 顶级目录名：{剧名}_{剧ID}_素材包
func buildRootDirName(dramaTitle string, dramaID uint64) string {
	clean := cleanFileName(dramaTitle)
	return fmt.Sprintf("%s_%d_素材包", clean, dramaID)
}

// 封面：01_封面/cover_main.{ext} (主) / 01_封面/cover_{n}.{ext} (n=2..)
func buildCoverRelPath(idx int, rawURL, contentType string) string {
	name := "cover_main"
	if idx > 0 {
		name = fmt.Sprintf("cover_%d", idx+1)
	}
	return fmt.Sprintf("01_封面/%s.%s", name, inferExt(rawURL, contentType))
}

// 集视频：02_集视频/{剧名} - 第 N 集.{ext}
func buildEpisodeRelPath(dramaTitle string, episodeNo int, rawURL, contentType string) string {
	clean := cleanFileName(dramaTitle)
	if clean == "" {
		clean = "untitled"
	}
	return fmt.Sprintf("02_集视频/%s - 第 %d 集.%s", clean, episodeNo, inferExt(rawURL, contentType))
}

// 权属：03_权属/copyright_{n}.{ext}
func buildCopyrightRelPath(idx int, rawURL, contentType string) string {
	return fmt.Sprintf("03_权属/copyright_%d.%s", idx+1, inferExt(rawURL, contentType))
}

// creatorGetDramaFilesManifest —— GET /v1/creator/dramas/:id/files-manifest
// 一键下载素材清单（MVP：封面 + 集视频 + 权属）
// 前端拿到清单后用 JSZip 一个个 fetch + 打包
func (s *Server) creatorGetDramaFilesManifest(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	d, ok := s.requireCreatorOwnsDrama(c, id)
	if !ok {
		return
	}

	dramaTitle := d.Title
	rootDir := buildRootDirName(dramaTitle, d.ID)

	type fileEntry struct {
		Category  string `json:"category"`            // cover / episode_video / copyright
		RelPath   string `json:"rel_path"`            // 完整 zip 内路径（含子目录）
		URL       string `json:"url"`                 // 下载源 URL（COS / VOD）
		SizeBytes int64  `json:"size_bytes"`          // 字节数（前端用来估算）
		Mime      string `json:"mime,omitempty"`      // Content-Type（兜底用）
		// 集视频专属
		EpisodeNo       int `json:"episode_no,omitempty"`
		DurationSeconds int `json:"duration_seconds,omitempty"`
	}

	files := make([]fileEntry, 0, 30)

	// === 1) 封面 ===
	// 1.1 主封面：dramas.cover_url
	if d.CoverURL != "" {
		files = append(files, fileEntry{
			Category:  "cover",
			RelPath:   buildCoverRelPath(0, d.CoverURL, ""),
			URL:       d.CoverURL,
			SizeBytes: 0, // 未知；前端 fetch 完能拿到 size
		})
	}
	// 1.2 多图封面：drama_covers 按 sort_order ASC
	var coverRows []model.DramaCover
	s.db.Where("drama_id = ?", d.ID).Order("sort_order asc").Find(&coverRows)
	coverIdx := 1 // 主封面已用 idx=0
	for _, cv := range coverRows {
		if cv.URL == "" {
			continue
		}
		files = append(files, fileEntry{
			Category:  "cover",
			RelPath:   buildCoverRelPath(coverIdx, cv.URL, ""),
			URL:       cv.URL,
			SizeBytes: 0,
		})
		coverIdx++
	}

	// === 2) 集视频：episodes 按 episode_no ASC ===
	var episodes []model.Episode
	s.db.Where("drama_id = ? AND video_url <> ''", d.ID).
		Order("episode_no asc").Find(&episodes)
	for _, ep := range episodes {
		if ep.VideoURL == "" {
			continue
		}
		files = append(files, fileEntry{
			Category:        "episode_video",
			RelPath:         buildEpisodeRelPath(dramaTitle, ep.EpisodeNo, ep.VideoURL, ""),
			URL:             ep.VideoURL,
			SizeBytes:       0,
			EpisodeNo:       ep.EpisodeNo,
			DurationSeconds: ep.DurationSeconds,
		})
	}

	// === 3) 权属：dramas.copyright_file_urls（JSON 数组） ===
	for i, url := range d.CopyrightFileURLs {
		if url == "" {
			continue
		}
		files = append(files, fileEntry{
			Category:  "copyright",
			RelPath:   buildCopyrightRelPath(i, url, ""),
			URL:       url,
			SizeBytes: 0,
		})
	}

	// === 4) summary ===
	var totalSize int64
	for _, f := range files {
		totalSize += f.SizeBytes
	}

	response.OK(c, gin.H{
		"drama": gin.H{
			"id":         d.ID,
			"title":      dramaTitle,
			"creator_id": d.CreatorID,
		},
		"root_dir": rootDir,
		"files":    files,
		"summary": gin.H{
			"total_files":      len(files),
			"total_size_bytes": totalSize, // 0（前端 fetch 完自行统计）
			"categories": gin.H{
				"cover":         coverIdx,
				"episode_video": len(episodes),
				"copyright":     len(d.CopyrightFileURLs),
			},
		},
	})
}
