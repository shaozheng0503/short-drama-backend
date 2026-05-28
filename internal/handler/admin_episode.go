package handler

import (
	"errors"
	"log"

	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"
	"ai-drama-platform/internal/vod"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (s *Server) adminListEpisodes(c *gin.Context) {
	dramaID := dramaIDFromPath(c)
	if dramaID == 0 {
		response.InvalidParam(c, "drama_id 不合法")
		return
	}
	if !s.dramaExists(dramaID) {
		response.NotFound(c, "短剧不存在")
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
	}
	response.OK(c, gin.H{"list": views})
}

type episodeCreateRequest struct {
	EpisodeNo       int    `json:"episode_no" binding:"required"`
	Title           string `json:"title"`
	VODFileID       string `json:"vod_file_id"`
	VideoURL        string `json:"video_url"`
	DurationSeconds int    `json:"duration_seconds"`
	Status          string `json:"status"`
}

func (s *Server) adminCreateEpisode(c *gin.Context) {
	dramaID := dramaIDFromPath(c)
	if dramaID == 0 {
		response.InvalidParam(c, "drama_id 不合法")
		return
	}
	if !s.dramaExists(dramaID) {
		response.NotFound(c, "短剧不存在")
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
		if err := tx.Create(&ep).Error; err != nil {
			return err
		}
		return refreshDramaTotalEpisodes(tx, dramaID)
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

type episodeUpdateRequest struct {
	Title           *string `json:"title"`
	VODFileID       *string `json:"vod_file_id"`
	VideoURL        *string `json:"video_url"`
	DurationSeconds *int    `json:"duration_seconds"`
	Status          *string `json:"status"`
}

func (s *Server) adminUpdateEpisode(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var ep model.Episode
	if err := s.db.First(&ep, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "剧集不存在")
			return
		}
		response.ServerError(c, "查询剧集失败")
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
			// ignore
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
		if err := s.db.Model(&ep).Updates(updates).Error; err != nil {
			response.ServerError(c, "更新剧集失败")
			return
		}
	}
	s.db.First(&ep, id)
	response.OK(c, episodeAdminView(ep))
}

type episodeBatchItem struct {
	EpisodeNo       int    `json:"episode_no" binding:"required"`
	Title           string `json:"title"`
	VODFileID       string `json:"vod_file_id"`
	VideoURL        string `json:"video_url"`
	DurationSeconds int    `json:"duration_seconds"`
}

type episodeBatchRequest struct {
	Items []episodeBatchItem `json:"items" binding:"required"`
}

// adminBatchCreateEpisodes —— 一次批量建剧集。
// 前端选多个本地文件全部走 VOD 直传，拿到一组 FileId 后一次性入库，省得 N 次 HTTP。
// 全部成功才入库；只要有一条参数非法/episode_no 撞键就回滚整个批次。
func (s *Server) adminBatchCreateEpisodes(c *gin.Context) {
	dramaID := dramaIDFromPath(c)
	if dramaID == 0 {
		response.InvalidParam(c, "drama_id 不合法")
		return
	}
	if !s.dramaExists(dramaID) {
		response.NotFound(c, "短剧不存在")
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
		// 给了 video_url 就视为已就绪（多见于站外迁移）；否则等 VOD 回调切 ready。
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
		if err := tx.Create(&eps).Error; err != nil {
			return err
		}
		return refreshDramaTotalEpisodes(tx, dramaID)
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

// adminPreviewEpisode —— 后台审片用的临时预览 URL。
// 启用了防盗链（VOD_PLAY_SIGN_ENABLED）时拼短期签名 URL；否则直接回原 video_url。
// 故意单独开接口而不复用 appPlayEpisode：后者会校验用户订阅/解锁，admin 走自己的旁路。
func (s *Server) adminPreviewEpisode(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var ep model.Episode
	if err := s.db.First(&ep, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "剧集不存在")
			return
		}
		response.ServerError(c, "查询剧集失败")
		return
	}
	if ep.VideoURL == "" {
		response.InvalidParam(c, "剧集尚未生成 video_url，无法预览")
		return
	}

	signedURL, err := s.vod.SignPlayURL(ep.VideoURL)
	if err != nil {
		log.Printf("[admin] preview sign err=%v file_id=%s", err, ep.VODFileID)
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

// adminRefreshEpisodeVOD —— webhook 丢失/验签失败时 admin 在后台手动触发：
// 拿 episode.vod_file_id 调腾讯 DescribeMediaInfos 拉最新元信息回写。
func (s *Server) adminRefreshEpisodeVOD(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var ep model.Episode
	if err := s.db.First(&ep, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "剧集不存在")
			return
		}
		response.ServerError(c, "查询剧集失败")
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
		log.Printf("[admin] refresh-vod file_id=%s err=%v", ep.VODFileID, err)
		response.Fail(c, response.CodeThirdPartyError, "调用 VOD 接口失败")
		return
	}

	updates := map[string]interface{}{}
	if info.VideoURL != "" {
		updates["video_url"] = info.VideoURL
	}
	if info.DurationSeconds > 0 {
		updates["duration_seconds"] = info.DurationSeconds
	}
	// 拿到 URL 即视为转码/上传完成，状态切 ready；否则保留原状态等待回调或下次刷新。
	if info.VideoURL != "" && ep.Status != model.EpisodeStatusReady {
		updates["status"] = model.EpisodeStatusReady
	}
	if len(updates) == 0 {
		response.OK(c, gin.H{"updated": false, "episode": episodeAdminView(ep)})
		return
	}
	if err := s.db.Model(&ep).Updates(updates).Error; err != nil {
		response.ServerError(c, "更新剧集失败")
		return
	}
	s.db.First(&ep, id)
	response.OK(c, gin.H{
		"updated":   true,
		"episode":   episodeAdminView(ep),
		"cover_url": info.CoverURL,
		"container": info.Container,
	})
}

// adminRetryEpisode —— 重新上传：清掉 vod_file_id/url/duration，状态回 uploading。
// 配合前端重新走 vod-sign 重传，复用原 episode 行（保留 drama_id + episode_no 不变）。
func (s *Server) adminRetryEpisode(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var ep model.Episode
	if err := s.db.First(&ep, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "剧集不存在")
			return
		}
		response.ServerError(c, "查询剧集失败")
		return
	}
	// 已 ready 的剧集要"换片"也允许：保留 episode_no，让前端重新拿 vod-sign 上传。
	// 但已上架的剧不允许此操作（用户已在看），先在 drama 层保护。
	var drama model.Drama
	if err := s.db.Select("id", "status").First(&drama, ep.DramaID).Error; err != nil {
		response.ServerError(c, "查询所属短剧失败")
		return
	}
	if drama.Status == model.DramaStatusPublished {
		response.Conflict(c, "已上架短剧的剧集不可重传，请先下架")
		return
	}

	updates := map[string]interface{}{
		"vod_file_id":      "",
		"video_url":        "",
		"duration_seconds": 0,
		"status":           model.EpisodeStatusUploading,
	}
	if err := s.db.Model(&ep).Updates(updates).Error; err != nil {
		response.ServerError(c, "重置剧集失败")
		return
	}
	s.db.First(&ep, id)
	response.OK(c, episodeAdminView(ep))
}

type episodeReorderItem struct {
	EpisodeID uint64 `json:"episode_id" binding:"required"`
	EpisodeNo int    `json:"episode_no" binding:"required"`
}

type episodeReorderRequest struct {
	Items []episodeReorderItem `json:"items" binding:"required"`
}

// adminReorderEpisodes —— 批量调整 episode_no。
// 仅 draft 短剧允许重排，已上架的剧改 episode_no 会让用户购买/解锁记录指向错位。
//
// (drama_id, episode_no) 有唯一键（model.go:203），直接 UPDATE 会和已有行撞键。
// 实现：事务里第一阶段把所有目标行置成 -id（负值不会和正常 episode_no 撞），
// 第二阶段再写最终值；这样不需要 DEFERRABLE 约束也能完成原子重排。
func (s *Server) adminReorderEpisodes(c *gin.Context) {
	dramaID := dramaIDFromPath(c)
	if dramaID == 0 {
		response.InvalidParam(c, "drama_id 不合法")
		return
	}
	var drama model.Drama
	if err := s.db.First(&drama, dramaID).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "短剧不存在")
			return
		}
		response.ServerError(c, "查询短剧失败")
		return
	}
	if drama.Status != model.DramaStatusDraft {
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

// adminDeleteEpisode —— 仅 draft 状态短剧可删除其剧集；已上架的剧涉及用户购买/解锁，禁止物理删。
// 删后同事务刷新 dramas.total_episodes。
func (s *Server) adminDeleteEpisode(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var ep model.Episode
	if err := s.db.First(&ep, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "剧集不存在")
			return
		}
		response.ServerError(c, "查询剧集失败")
		return
	}
	var drama model.Drama
	if err := s.db.Select("id", "status").First(&drama, ep.DramaID).Error; err != nil {
		response.ServerError(c, "查询所属短剧失败")
		return
	}
	if drama.Status != model.DramaStatusDraft {
		response.Conflict(c, "仅草稿短剧的剧集可删除")
		return
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&model.Episode{}, id).Error; err != nil {
			return err
		}
		return refreshDramaTotalEpisodes(tx, ep.DramaID)
	})
	if err != nil {
		response.ServerError(c, "删除剧集失败")
		return
	}
	response.OK(c, gin.H{"deleted": true, "id": id, "drama_id": ep.DramaID})
}

func (s *Server) dramaExists(id uint64) bool {
	var cnt int64
	s.db.Model(&model.Drama{}).Where("id = ?", id).Count(&cnt)
	return cnt > 0
}

func refreshDramaTotalEpisodes(tx *gorm.DB, dramaID uint64) error {
	var cnt int64
	if err := tx.Model(&model.Episode{}).
		Where("drama_id = ?", dramaID).
		Count(&cnt).Error; err != nil {
		return err
	}
	return tx.Model(&model.Drama{}).
		Where("id = ?", dramaID).
		Update("total_episodes", cnt).Error
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// PostgreSQL unique_violation: SQLSTATE 23505
	return errors.Is(err, gorm.ErrDuplicatedKey) ||
		containsAny(msg, "duplicate key", "SQLSTATE 23505", "unique constraint")
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub != "" && indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
