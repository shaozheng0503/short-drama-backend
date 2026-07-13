package handler

import (
	"context"
	"errors"
	"log"
	"time"

	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"
	"ai-drama-platform/internal/vod"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (s *Server) creatorListEpisodes(c *gin.Context) {
	dramaID := dramaIDFromPath(c)
	if dramaID == 0 {
		response.InvalidParam(c, "drama_id 不合法")
		return
	}
	if _, ok := s.requireCreatorOwnsDrama(c, dramaID); !ok {
		return
	}
	var episodes []model.Episode
	if err := s.db.Where("drama_id = ?", dramaID).Order("episode_no asc").Find(&episodes).Error; err != nil {
		response.ServerError(c, "查询剧集失败")
		return
	}
	views := make([]gin.H, 0, len(episodes))
	for _, ep := range episodes {
		views = append(views, episodeAdminView(ep))
		// v0.13.1：列表加载时给 uploading 状态的剧集加一次懒同步（VOD 回调漏了的兜底）
		s.lazySyncEpisodeVOD(&ep)
	}
	response.OK(c, gin.H{"list": views})
}

func (s *Server) creatorCreateEpisode(c *gin.Context) {
	dramaID := dramaIDFromPath(c)
	if dramaID == 0 {
		response.InvalidParam(c, "drama_id 不合法")
		return
	}
	if _, ok := s.requireCreatorOwnsDrama(c, dramaID); !ok {
		return
	}

	var req episodeCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}
	if req.EpisodeNo < 1 {
		response.InvalidParam(c, "episode_no 必须大于 0")
		return
	}

	status := model.EpisodeStatusUploading
	if req.Status != "" {
		switch req.Status {
		case model.EpisodeStatusUploading, model.EpisodeStatusReady, model.EpisodeStatusFailed:
			status = req.Status
		default:
			response.InvalidParam(c, "status 非法")
			return
		}
	}
	if req.VideoURL != "" && req.Status == "" {
		status = model.EpisodeStatusReady
	}
	if status == model.EpisodeStatusReady && req.VideoURL == "" && req.VODFileID == "" {
		response.InvalidParam(c, "ready 状态剧集必须提供 video_url 或 vod_file_id")
		return
	}

	ep := model.Episode{
		DramaID:         dramaID,
		EpisodeNo:       req.EpisodeNo,
		Title:           req.Title,
		VODFileID:       req.VODFileID,
		VideoURL:        req.VideoURL,
		DurationSeconds: req.DurationSeconds,
		Status:          status,
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		return tx.Create(&ep).Error
	})
	if err != nil {
		if isUniqueViolation(err) {
			response.Conflict(c, "同一短剧下 episode_no 已存在")
			return
		}
		response.ServerError(c, "创建剧集失败")
		return
	}
	response.OK(c, episodeAdminView(ep))
}

func (s *Server) creatorBatchCreateEpisodes(c *gin.Context) {
	dramaID := dramaIDFromPath(c)
	if dramaID == 0 {
		response.InvalidParam(c, "drama_id 不合法")
		return
	}
	if _, ok := s.requireCreatorOwnsDrama(c, dramaID); !ok {
		return
	}

	var req episodeBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Items) == 0 {
		response.InvalidParam(c, "items 必填且不能为空")
		return
	}
	if len(req.Items) > 100 {
		response.InvalidParam(c, "单次最多 100 集")
		return
	}
	seen := make(map[int]bool, len(req.Items))
	for _, it := range req.Items {
		if it.EpisodeNo < 1 {
			response.InvalidParam(c, "episode_no 必须大于 0")
			return
		}
		if seen[it.EpisodeNo] {
			response.InvalidParam(c, "请求内 episode_no 重复")
			return
		}
		seen[it.EpisodeNo] = true
	}

	eps := make([]model.Episode, 0, len(req.Items))
	for _, it := range req.Items {
		status := model.EpisodeStatusUploading
		if it.VideoURL != "" {
			status = model.EpisodeStatusReady
		}
		eps = append(eps, model.Episode{
			DramaID:         dramaID,
			EpisodeNo:       it.EpisodeNo,
			Title:           it.Title,
			VODFileID:       it.VODFileID,
			VideoURL:        it.VideoURL,
			DurationSeconds: it.DurationSeconds,
			Status:          status,
		})
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		return tx.Create(&eps).Error
	})
	if err != nil {
		if isUniqueViolation(err) {
			response.Conflict(c, "存在与已有剧集 episode_no 冲突的项")
			return
		}
		response.ServerError(c, "批量创建剧集失败")
		return
	}
	views := make([]gin.H, 0, len(eps))
	for _, ep := range eps {
		views = append(views, episodeAdminView(ep))
	}
	response.OK(c, gin.H{"list": views, "count": len(eps)})
}

func (s *Server) creatorReorderEpisodes(c *gin.Context) {
	dramaID := dramaIDFromPath(c)
	if dramaID == 0 {
		response.InvalidParam(c, "drama_id 不合法")
		return
	}
	d, ok := s.requireCreatorOwnsDrama(c, dramaID)
	if !ok {
		return
	}
	if d.Status != model.DramaStatusDraft {
		response.Conflict(c, "仅草稿状态短剧可重排剧集")
		return
	}

	var req episodeReorderRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Items) == 0 {
		response.InvalidParam(c, "items 必填且不能为空")
		return
	}
	seenID := make(map[uint64]bool, len(req.Items))
	seenNo := make(map[int]bool, len(req.Items))
	for _, it := range req.Items {
		if it.EpisodeNo < 1 {
			response.InvalidParam(c, "episode_no 必须大于 0")
			return
		}
		if seenID[it.EpisodeID] {
			response.InvalidParam(c, "episode_id 重复")
			return
		}
		if seenNo[it.EpisodeNo] {
			response.InvalidParam(c, "episode_no 重复")
			return
		}
		seenID[it.EpisodeID] = true
		seenNo[it.EpisodeNo] = true
	}

	ids := make([]uint64, 0, len(req.Items))
	for _, it := range req.Items {
		ids = append(ids, it.EpisodeID)
	}
	var existing []model.Episode
	if err := s.db.Select("id", "drama_id").Where("id IN ?", ids).Find(&existing).Error; err != nil {
		response.ServerError(c, "查询剧集失败")
		return
	}
	if len(existing) != len(req.Items) {
		response.NotFound(c, "存在不存在的 episode_id")
		return
	}
	for _, ep := range existing {
		if ep.DramaID != dramaID {
			response.InvalidParam(c, "存在不属于该短剧的 episode_id")
			return
		}
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		for _, it := range req.Items {
			if err := tx.Model(&model.Episode{}).
				Where("id = ?", it.EpisodeID).
				Update("episode_no", -int64(it.EpisodeID)).Error; err != nil {
				return err
			}
		}
		for _, it := range req.Items {
			if err := tx.Model(&model.Episode{}).
				Where("id = ?", it.EpisodeID).
				Update("episode_no", it.EpisodeNo).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if isUniqueViolation(err) {
			response.Conflict(c, "目标 episode_no 与该剧其它剧集冲突")
			return
		}
		response.ServerError(c, "重排剧集失败")
		return
	}
	var list []model.Episode
	s.db.Where("drama_id = ?", dramaID).Order("episode_no asc").Find(&list)
	views := make([]gin.H, 0, len(list))
	for _, ep := range list {
		views = append(views, episodeAdminView(ep))
	}
	response.OK(c, gin.H{"list": views})
}

func (s *Server) creatorUpdateEpisode(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	ep, _, ok := s.requireCreatorOwnsEpisode(c, id)
	if !ok {
		return
	}

	var req episodeUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}

	updates := map[string]interface{}{}
	if req.Title != nil {
		updates["title"] = *req.Title
	}
	if req.VODFileID != nil {
		updates["vod_file_id"] = *req.VODFileID
	}
	if req.VideoURL != nil {
		updates["video_url"] = *req.VideoURL
	}
	if req.DurationSeconds != nil && *req.DurationSeconds >= 0 {
		updates["duration_seconds"] = *req.DurationSeconds
	}
	if req.Status != nil {
		switch *req.Status {
		case model.EpisodeStatusUploading, model.EpisodeStatusReady, model.EpisodeStatusFailed:
			updates["status"] = *req.Status
		case "":
		default:
			response.InvalidParam(c, "status 非法")
			return
		}
	}

	nextStatus := ep.Status
	if req.Status != nil && *req.Status != "" {
		nextStatus = *req.Status
	}
	nextVideoURL := ep.VideoURL
	if req.VideoURL != nil {
		nextVideoURL = *req.VideoURL
	}
	nextVODFileID := ep.VODFileID
	if req.VODFileID != nil {
		nextVODFileID = *req.VODFileID
	}
	if nextStatus == model.EpisodeStatusReady && nextVideoURL == "" && nextVODFileID == "" {
		response.InvalidParam(c, "ready 状态剧集必须提供 video_url 或 vod_file_id")
		return
	}

	if len(updates) > 0 {
		if err := s.db.Model(ep).Updates(updates).Error; err != nil {
			response.ServerError(c, "更新剧集失败")
			return
		}
	}
	var fresh model.Episode
	s.db.First(&fresh, id)
	response.OK(c, episodeAdminView(fresh))
}

func (s *Server) creatorDeleteEpisode(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	ep, d, ok := s.requireCreatorOwnsEpisode(c, id)
	if !ok {
		return
	}
	if d.Status != model.DramaStatusDraft {
		response.Conflict(c, "仅草稿短剧的剧集可删除")
		return
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		return tx.Delete(&model.Episode{}, id).Error
	})
	if err != nil {
		response.ServerError(c, "删除剧集失败")
		return
	}
	response.OK(c, gin.H{"deleted": true, "id": id, "drama_id": ep.DramaID})
}

// lazySyncEpisodeVOD —— 后端兜底：episode 处于 uploading 状态时，
// 在列表 / 详情 / 预览接口里被调用，**后台**调一次 DescribeMediaInfos 自动切 ready。
//
// 关键点：
//  1. **非阻塞**：用 goroutine 起，handler 不等 VOD 返回，避免拉慢剧集列表接口
//  2. **不频繁**：用 vod_synced_at 字段节流，30 秒内已经主动同步过的就不再调
//  3. **不写库**：只读 episodes / 只在拿到结果时写回 status / video_url / duration_seconds
//  4. **失败静默**：任何错误只 log，不影响用户当前请求
//
// 这是 VOD 节点回调 / NewFileUpload 漏了的兜底——VOD 控制台没配回调时仍能自动 ready。
// 文档：见 announcement-2026-07-03-v0.13.1.md
func (s *Server) lazySyncEpisodeVOD(ep *model.Episode) {
	if ep == nil {
		return
	}
	if ep.Status != model.EpisodeStatusUploading {
		return
	}
	if ep.VODFileID == "" {
		return
	}
	if !s.vod.Configured() {
		return
	}
	// 30 秒节流：避免短时间多次进入列表接口时反复调 VOD
	if ep.VODSyncedAt != nil && time.Since(*ep.VODSyncedAt) < 30*time.Second {
		return
	}
	epID := ep.ID
	fileID := ep.VODFileID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		info, err := s.vod.DescribeMediaInfo(ctx, fileID)
		if err != nil {
			log.Printf("[episode] lazy-sync vod file_id=%s err=%v", fileID, err)
			return
		}
		updates := map[string]interface{}{
			"vod_synced_at": gorm.Expr("NOW()"),
		}
		if info.VideoURL != "" {
			updates["video_url"] = info.VideoURL
		}
		if info.DurationSeconds > 0 {
			updates["duration_seconds"] = info.DurationSeconds
		}
		// 拿到 URL 即视为转码/上传完成
		if info.VideoURL != "" {
			updates["status"] = model.EpisodeStatusReady
		}
		if err := s.db.Model(&model.Episode{}).Where("id = ?", epID).Updates(updates).Error; err != nil {
			log.Printf("[episode] lazy-sync update ep=%d err=%v", epID, err)
		} else {
			log.Printf("[episode] lazy-sync ep=%d file_id=%s status=%s url_set=%v",
				epID, fileID, model.EpisodeStatusReady, info.VideoURL != "")
		}
	}()
}

func (s *Server) creatorRefreshEpisodeVOD(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	ep, _, ok := s.requireCreatorOwnsEpisode(c, id)
	if !ok {
		return
	}
	if ep.VODFileID == "" {
		response.InvalidParam(c, "剧集未关联 vod_file_id，无法刷新")
		return
	}
	if !s.vod.Configured() {
		response.Fail(c, response.CodeThirdPartyError, "VOD 未配置")
		return
	}
	info, err := s.vod.DescribeMediaInfo(c.Request.Context(), ep.VODFileID)
	if err != nil {
		if errors.Is(err, vod.ErrMediaNotFound) {
			response.NotFound(c, "VOD 文件不存在或已删除")
			return
		}
		log.Printf("[creator] refresh-vod file_id=%s err=%v", ep.VODFileID, err)
		response.Fail(c, response.CodeThirdPartyError, "调用 VOD 接口失败")
		return
	}

	updates := map[string]interface{}{
		"vod_synced_at": gorm.Expr("NOW()"), // v0.13.1：刷新成功后写入，给懒加载节流
	}
	if info.VideoURL != "" {
		updates["video_url"] = info.VideoURL
	}
	if info.DurationSeconds > 0 {
		updates["duration_seconds"] = info.DurationSeconds
	}
	if info.VideoURL != "" && ep.Status != model.EpisodeStatusReady {
		updates["status"] = model.EpisodeStatusReady
	}
	// 如果 VOD 文件存在但 VideoURL 仍为空，说明文件还在上传中/转码中，不改状态
	if info.VideoURL == "" {
		response.OK(c, gin.H{
			"updated":  false,
			"episode":  episodeAdminView(*ep),
			"vod_status": "uploading",
			"hint":     "VOD 文件仍在上传或转码中，请稍后再试",
		})
		return
	}
	if len(updates) == 0 {
		response.OK(c, gin.H{"updated": false, "episode": episodeAdminView(*ep)})
		return
	}
	if err := s.db.Model(ep).Updates(updates).Error; err != nil {
		response.ServerError(c, "更新剧集失败")
		return
	}
	var fresh model.Episode
	s.db.First(&fresh, id)
	response.OK(c, gin.H{
		"updated":   true,
		"episode":   episodeAdminView(fresh),
		"cover_url": info.CoverURL,
		"container": info.Container,
	})
}

func (s *Server) creatorRetryEpisode(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	ep, d, ok := s.requireCreatorOwnsEpisode(c, id)
	if !ok {
		return
	}
	if d.Status == model.DramaStatusPublished {
		response.Conflict(c, "已上架短剧的剧集不可重传，请先下架")
		return
	}
	updates := map[string]interface{}{
		"vod_file_id":      "",
		"video_url":        "",
		"duration_seconds": 0,
		"status":           model.EpisodeStatusUploading,
	}
	if err := s.db.Model(ep).Updates(updates).Error; err != nil {
		response.ServerError(c, "重置剧集失败")
		return
	}
	var fresh model.Episode
	s.db.First(&fresh, id)
	response.OK(c, episodeAdminView(fresh))
}

func (s *Server) creatorPreviewEpisode(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	ep, _, ok := s.requireCreatorOwnsEpisode(c, id)
	if !ok {
		return
	}
	// v0.13.1：进预览页时也跑一次懒同步（创作者点「预览」= 想看片，正好兜底）
	s.lazySyncEpisodeVOD(ep)
	// 拿一下最新的 video_url（懒同步可能并发改库）
	s.db.First(ep, id)
	if ep.VideoURL == "" {
		response.InvalidParam(c, "剧集尚未生成 video_url，无法预览")
		return
	}
	signedURL, err := s.vod.SignPlayURL(ep.VideoURL)
	if err != nil {
		log.Printf("[creator] preview sign err=%v file_id=%s", err, ep.VODFileID)
		response.Fail(c, response.CodeThirdPartyError, "生成预览 URL 失败")
		return
	}
	response.OK(c, gin.H{
		"episode_id":       ep.ID,
		"vod_file_id":      ep.VODFileID,
		"video_url":        signedURL,
		"duration_seconds": ep.DurationSeconds,
		"status":           ep.Status,
		"signed":           s.vod.PlaySignConfigured(),
	})
}
