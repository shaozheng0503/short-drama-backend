package handler

import (
	"errors"
	"log"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// emitCommentReplyMessage 楼中楼回复 → 给被回复者发一条 comment_reply 消息（best-effort，失败只记日志不阻断发评论）。
// recipientID=被回复评论的作者，repliedCommentID=被回复的那条评论，replyCommentID=新回复。
func (s *Server) emitCommentReplyMessage(recipientID, actorID, repliedCommentID, replyCommentID uint64) {
	if recipientID == 0 || recipientID == actorID { // 不给自己发
		return
	}
	m := model.AppMessage{
		RecipientID: recipientID,
		Type:        model.AppMessageTypeCommentReply,
		CommentID:   repliedCommentID,
		ReplyID:     &replyCommentID,
		ActorID:     actorID,
	}
	if err := s.db.Create(&m).Error; err != nil {
		log.Printf("[app_message] reply msg 发送失败 recipient=%d err=%v", recipientID, err)
	}
}

// emitCommentLikeMessage 评论点赞 → 给评论作者发/更新一条 comment_like 聚合消息（best-effort）。
// 同一 (收信人,评论) 只一条：已存在则更新最近触发者 + 置未读 + 顶上来（UpdatedAt 自动刷新），即「某时间段内多赞归一条」。
func (s *Server) emitCommentLikeMessage(recipientID, actorID, commentID uint64) {
	if recipientID == 0 || recipientID == actorID {
		return
	}
	var existing model.AppMessage
	err := s.db.Where("recipient_id = ? AND type = ? AND comment_id = ?",
		recipientID, model.AppMessageTypeCommentLike, commentID).First(&existing).Error
	switch {
	case err == nil:
		if e := s.db.Model(&existing).Updates(map[string]interface{}{
			"actor_id": actorID,
			"is_read":  false,
		}).Error; e != nil {
			log.Printf("[app_message] like msg 更新失败 recipient=%d err=%v", recipientID, e)
		}
	case errors.Is(err, gorm.ErrRecordNotFound):
		m := model.AppMessage{
			RecipientID: recipientID,
			Type:        model.AppMessageTypeCommentLike,
			CommentID:   commentID,
			ActorID:     actorID,
		}
		if e := s.db.Create(&m).Error; e != nil {
			log.Printf("[app_message] like msg 创建失败 recipient=%d err=%v", recipientID, e)
		}
	default:
		log.Printf("[app_message] like msg 查询失败 recipient=%d err=%v", recipientID, err)
	}
}

// appListMessages —— GET /v1/app/messages?type=comment_reply|comment_like&unread_only=true
// 按 updated_at 倒序（点赞聚合被刷新会顶上来）；展示字段读时 join。
func (s *Server) appListMessages(c *gin.Context) {
	uid := middleware.CurrentID(c)
	page, pageSize := paginate(c)

	q := s.db.Model(&model.AppMessage{}).Where("recipient_id = ?", uid)
	if t := c.Query("type"); t == model.AppMessageTypeCommentReply || t == model.AppMessageTypeCommentLike {
		q = q.Where("type = ?", t)
	}
	if c.Query("unread_only") == "true" {
		q = q.Where("is_read = ?", false)
	}

	var total int64
	q.Count(&total)
	var items []model.AppMessage
	q.Order("updated_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items)

	var unread int64
	s.db.Model(&model.AppMessage{}).Where("recipient_id = ? AND is_read = ?", uid, false).Count(&unread)

	resp := pageResp(s.appMessageViews(items), page, pageSize, total)
	resp["unread_count"] = unread
	response.OK(c, resp)
}

// appMarkMessageRead —— POST /v1/app/messages/:id/read
func (s *Server) appMarkMessageRead(c *gin.Context) {
	uid := middleware.CurrentID(c)
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	res := s.db.Model(&model.AppMessage{}).
		Where("id = ? AND recipient_id = ?", id, uid).
		Update("is_read", true)
	if res.Error != nil {
		response.ServerError(c, "操作失败")
		return
	}
	if res.RowsAffected == 0 {
		response.NotFound(c, "消息不存在")
		return
	}
	response.OK(c, gin.H{"id": id, "is_read": true})
}

// appMarkAllMessagesRead —— POST /v1/app/messages/read-all
func (s *Server) appMarkAllMessagesRead(c *gin.Context) {
	uid := middleware.CurrentID(c)
	if err := s.db.Model(&model.AppMessage{}).
		Where("recipient_id = ? AND is_read = ?", uid, false).
		Update("is_read", true).Error; err != nil {
		response.ServerError(c, "操作失败")
		return
	}
	response.OK(c, gin.H{"ok": true})
}

// appMessageViews 批量富化消息列表：触发者、被操作评论、剧集封面/名称/集数；点赞类附总赞数+最近2个点赞者。
func (s *Server) appMessageViews(items []model.AppMessage) []gin.H {
	if len(items) == 0 {
		return []gin.H{}
	}

	commentIDs := make([]uint64, 0, len(items)*2)
	actorIDs := make([]uint64, 0, len(items))
	likeCommentIDs := make([]uint64, 0, len(items))
	for _, m := range items {
		commentIDs = append(commentIDs, m.CommentID)
		if m.ReplyID != nil {
			commentIDs = append(commentIDs, *m.ReplyID)
		}
		actorIDs = append(actorIDs, m.ActorID)
		if m.Type == model.AppMessageTypeCommentLike {
			likeCommentIDs = append(likeCommentIDs, m.CommentID)
		}
	}

	commentMap := s.commentsByIDs(commentIDs)
	dramaIDs := make([]uint64, 0, len(commentMap))
	episodeIDs := make([]uint64, 0, len(commentMap))
	for _, cm := range commentMap {
		dramaIDs = append(dramaIDs, cm.DramaID)
		if cm.EpisodeID != nil {
			episodeIDs = append(episodeIDs, *cm.EpisodeID)
		}
	}
	dramaMap := s.dramasBriefByIDs(dramaIDs)
	episodeMap := s.episodesBriefByIDs(episodeIDs)

	recentLikers := s.recentLikersByComment(likeCommentIDs, 2)
	for _, uids := range recentLikers {
		actorIDs = append(actorIDs, uids...)
	}
	userMap := s.usersByIDs(actorIDs)

	views := make([]gin.H, 0, len(items))
	for _, m := range items {
		// 上下文评论：优先用「我的那条被操作评论」，缺失（被删）则退到回复评论，用于取剧集归属。
		ctx, ctxOK := commentMap[m.CommentID]
		if !ctxOK && m.ReplyID != nil {
			ctx, ctxOK = commentMap[*m.ReplyID]
		}

		v := gin.H{
			"id":         m.ID,
			"type":       m.Type,
			"is_read":    m.IsRead,
			"actor":      userBrief(m.ActorID, userMap[m.ActorID]),
			"drama":      nil,
			"episode":    nil,
			"created_at": m.CreatedAt,
			"updated_at": m.UpdatedAt,
		}

		if tc, ok := commentMap[m.CommentID]; ok {
			v["target_comment"] = gin.H{"id": tc.ID, "content": tc.Content}
		} else {
			v["target_comment"] = gin.H{"id": m.CommentID, "content": ""}
		}

		if ctxOK {
			if d, ok := dramaMap[ctx.DramaID]; ok {
				v["drama"] = gin.H{"id": d.ID, "title": d.Title, "cover_url": d.CoverURL}
			}
			if ctx.EpisodeID != nil {
				if e, ok := episodeMap[*ctx.EpisodeID]; ok {
					v["episode"] = gin.H{"id": e.ID, "episode_no": e.EpisodeNo, "title": e.Title}
				}
			}
		}

		switch m.Type {
		case model.AppMessageTypeCommentReply:
			if m.ReplyID != nil {
				if rc, ok := commentMap[*m.ReplyID]; ok {
					v["reply_content"] = rc.Content
				} else {
					v["reply_content"] = ""
				}
			}
		case model.AppMessageTypeCommentLike:
			if tc, ok := commentMap[m.CommentID]; ok {
				v["like_count"] = tc.LikeCount
			} else {
				v["like_count"] = 0
			}
			actors := make([]gin.H, 0, 2)
			for _, lid := range recentLikers[m.CommentID] {
				actors = append(actors, userBrief(lid, userMap[lid]))
			}
			v["recent_actors"] = actors
		}

		views = append(views, v)
	}
	return views
}

// commentsByIDs 批量取评论（含软删过滤），返回 id → Comment。
func (s *Server) commentsByIDs(ids []uint64) map[uint64]model.Comment {
	out := map[uint64]model.Comment{}
	if len(ids) == 0 {
		return out
	}
	var rows []model.Comment
	if err := s.db.Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return out
	}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}

// dramasBriefByIDs 批量取剧的展示字段（标题/封面）。
func (s *Server) dramasBriefByIDs(ids []uint64) map[uint64]model.Drama {
	out := map[uint64]model.Drama{}
	if len(ids) == 0 {
		return out
	}
	var rows []model.Drama
	if err := s.db.Select("id, title, cover_url").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return out
	}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}

// episodesBriefByIDs 批量取剧集的展示字段（集数/标题）。
func (s *Server) episodesBriefByIDs(ids []uint64) map[uint64]model.Episode {
	out := map[uint64]model.Episode{}
	if len(ids) == 0 {
		return out
	}
	var rows []model.Episode
	if err := s.db.Select("id, episode_no, title").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return out
	}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}

// recentLikersByComment 每条评论取最近 limit 个点赞者（用窗口函数一次查出），返回 comment_id → [user_id]（最近优先）。
func (s *Server) recentLikersByComment(commentIDs []uint64, limit int) map[uint64][]uint64 {
	out := map[uint64][]uint64{}
	if len(commentIDs) == 0 {
		return out
	}
	var rows []struct {
		CommentID uint64
		UserID    uint64
	}
	s.db.Raw(`
		SELECT comment_id, user_id FROM (
			SELECT comment_id, user_id,
			       ROW_NUMBER() OVER (PARTITION BY comment_id ORDER BY created_at DESC, id DESC) AS rn
			FROM comment_likes
			WHERE comment_id IN ?
		) t WHERE rn <= ?
		ORDER BY comment_id, rn
	`, commentIDs, limit).Scan(&rows)
	for _, r := range rows {
		out[r.CommentID] = append(out[r.CommentID], r.UserID)
	}
	return out
}
