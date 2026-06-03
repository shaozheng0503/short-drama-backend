package handler

import (
	"errors"
	"strings"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const commentMaxLen = 1000

type createCommentRequest struct {
	Content   string  `json:"content"`
	EpisodeID *uint64 `json:"episode_id"` // 选填：传了=对该集发集评，不传=对整剧发剧评（仅顶层评论生效）
	ParentID  *uint64 `json:"parent_id"`  // 选填：传了=楼中楼回复，指向被回复的评论（顶层或回复都可，内部拍平到两级）
}

// appListComments —— 顶层评论列表（楼中楼回复不在此返回，用 GET /app/comments/:id/replies 单拉）。
// 匿名也能看；登录态会带上当前用户对每条评论的 liked 标记。
// 区分剧评 / 集评：带 ?episode_id= 时只返回该集的集评；不带时只返回整剧的剧评（episode_id 为空）。
func (s *Server) appListComments(c *gin.Context) {
	dramaID := parseUint(c.Param("id"))
	if dramaID == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	// 校验剧存在且已上架（防爬未上架剧的评论）
	var drama model.Drama
	if err := s.db.First(&drama, dramaID).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "短剧不存在")
			return
		}
		response.ServerError(c, "查询短剧失败")
		return
	}
	if drama.Status != model.DramaStatusPublished {
		response.NotFound(c, "短剧未上架")
		return
	}

	// 只取顶层评论（parent_id IS NULL）；episode_id 过滤：有值=集评，无值=剧评。
	q := s.db.Model(&model.Comment{}).Where("drama_id = ? AND parent_id IS NULL", dramaID)
	if v := parseUint(c.Query("episode_id")); v > 0 {
		if !s.episodeBelongsToDrama(v, dramaID) {
			response.InvalidParam(c, "episode_id 与 drama_id 不匹配")
			return
		}
		q = q.Where("episode_id = ?", v)
	} else {
		q = q.Where("episode_id IS NULL")
	}

	page, pageSize := paginate(c)
	var total int64
	q.Count(&total)

	var comments []model.Comment
	if err := q.
		Order("created_at desc").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&comments).Error; err != nil {
		response.ServerError(c, "查询评论失败")
		return
	}

	ids := make([]uint64, 0, len(comments))
	authorIDs := make([]uint64, 0, len(comments))
	for _, cm := range comments {
		ids = append(ids, cm.ID)
		authorIDs = append(authorIDs, cm.UserID)
	}
	userMap := s.usersByIDs(authorIDs)
	likedMap := s.likedCommentIDs(optionalAppUserID(c, s), ids)

	views := make([]gin.H, 0, len(comments))
	for _, cm := range comments {
		views = append(views, commentView(cm, userMap[cm.UserID], nil, likedMap[cm.ID]))
	}
	response.OK(c, pageResp(views, page, pageSize, total))
}

// appListReplies —— GET /app/comments/:id/replies，某条顶层评论下的楼中楼回复列表（按时间正序）。
func (s *Server) appListReplies(c *gin.Context) {
	parentID := parseUint(c.Param("id"))
	if parentID == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var parent model.Comment
	if err := s.db.First(&parent, parentID).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "评论不存在")
			return
		}
		response.ServerError(c, "查询评论失败")
		return
	}
	if parent.ParentID != nil {
		response.InvalidParam(c, "该评论本身是回复，没有楼中楼")
		return
	}

	page, pageSize := paginate(c)
	q := s.db.Model(&model.Comment{}).Where("parent_id = ?", parentID)
	var total int64
	q.Count(&total)

	var replies []model.Comment
	if err := q.
		Order("created_at asc").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&replies).Error; err != nil {
		response.ServerError(c, "查询回复失败")
		return
	}

	ids := make([]uint64, 0, len(replies))
	userIDs := make([]uint64, 0, len(replies)*2)
	for _, cm := range replies {
		ids = append(ids, cm.ID)
		userIDs = append(userIDs, cm.UserID)
		if cm.ReplyToUserID != nil {
			userIDs = append(userIDs, *cm.ReplyToUserID)
		}
	}
	userMap := s.usersByIDs(userIDs)
	likedMap := s.likedCommentIDs(optionalAppUserID(c, s), ids)

	views := make([]gin.H, 0, len(replies))
	for _, cm := range replies {
		var replyTo *model.User
		if cm.ReplyToUserID != nil {
			replyTo = userMap[*cm.ReplyToUserID]
		}
		views = append(views, commentView(cm, userMap[cm.UserID], replyTo, likedMap[cm.ID]))
	}
	response.OK(c, pageResp(views, page, pageSize, total))
}

// appCreateComment —— 发评论 / 回复，要求 APP 用户登录态。
// 传 parent_id 即为楼中楼回复：回复顶层评论→挂在该顶层下；回复某条回复→拍平到同一顶层 + @该回复作者。
func (s *Server) appCreateComment(c *gin.Context) {
	dramaID := parseUint(c.Param("id"))
	if dramaID == 0 {
		response.InvalidParam(c, "id 不合法")
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
	if drama.Status != model.DramaStatusPublished {
		response.NotFound(c, "短剧未上架")
		return
	}

	var req createCommentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "请求体不合法")
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		response.InvalidParam(c, "content 不能为空")
		return
	}
	// 用 rune 计长度而非 byte，中文按字符算。
	if rl := len([]rune(content)); rl > commentMaxLen {
		response.InvalidParam(c, "content 过长（最多 1000 字）")
		return
	}

	var episodeID *uint64
	var parentID *uint64
	var replyToUserID *uint64
	// 回复消息所需：被回复评论的作者 + 被回复评论 id（txn 成功后发站内信用）。
	var notifyRecipientID, repliedCommentID uint64

	if req.ParentID != nil && *req.ParentID > 0 {
		// 回复：校验父评论存在且属于本剧；两级拍平。
		var parent model.Comment
		if err := s.db.First(&parent, *req.ParentID).Error; err != nil {
			if isNotFound(err) {
				response.NotFound(c, "回复的评论不存在")
				return
			}
			response.ServerError(c, "查询父评论失败")
			return
		}
		if parent.DramaID != dramaID {
			response.InvalidParam(c, "parent_id 与 drama_id 不匹配")
			return
		}
		// 收信人=被回复的那条评论的作者（点了回复的那条，不一定是顶层）。
		notifyRecipientID = parent.UserID
		repliedCommentID = parent.ID
		if parent.ParentID == nil {
			// 回复顶层评论：直接挂在其下，不带 @（UI 已嵌套在顶层下）。
			top := parent.ID
			parentID = &top
		} else {
			// 回复某条回复：拍平到同一顶层，@被回复者。
			parentID = parent.ParentID
			rt := parent.UserID
			replyToUserID = &rt
		}
		// 回复继承父评论的集/剧归属，忽略客户端 episode_id。
		episodeID = parent.EpisodeID
	} else if req.EpisodeID != nil && *req.EpisodeID > 0 {
		// 顶层集评：校验该集属于本剧。
		if !s.episodeBelongsToDrama(*req.EpisodeID, dramaID) {
			response.InvalidParam(c, "episode_id 与 drama_id 不匹配")
			return
		}
		episodeID = req.EpisodeID
	}

	uid := middleware.CurrentID(c)
	cm := model.Comment{
		DramaID:       dramaID,
		EpisodeID:     episodeID,
		ParentID:      parentID,
		ReplyToUserID: replyToUserID,
		UserID:        uid,
		Content:       content,
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&cm).Error; err != nil {
			return err
		}
		if parentID != nil {
			// 顶层评论 reply_count +1（用于「查看 N 条回复」）。
			return tx.Model(&model.Comment{}).Where("id = ?", *parentID).
				UpdateColumn("reply_count", gorm.Expr("reply_count + ?", 1)).Error
		}
		return nil
	})
	if err != nil {
		response.ServerError(c, "发表评论失败")
		return
	}

	// 楼中楼回复 → 给被回复者发站内信（best-effort，不影响发评论返回）。
	if parentID != nil {
		s.emitCommentReplyMessage(notifyRecipientID, uid, repliedCommentID, cm.ID)
	}

	// 回填作者 / 被回复者，前端可直接渲染不用再拉一次。
	var author model.User
	_ = s.db.Select("id, nickname, avatar").First(&author, uid).Error
	var replyTo *model.User
	if replyToUserID != nil {
		var u model.User
		if err := s.db.Select("id, nickname, avatar").First(&u, *replyToUserID).Error; err == nil {
			replyTo = &u
		}
	}
	response.OK(c, commentView(cm, &author, replyTo, false))
}

// appLikeComment / appUnlikeComment —— 评论点赞 / 取消（维护 comments.like_count 冗余列）。
func (s *Server) appLikeComment(c *gin.Context)   { s.appToggleCommentLike(c, true) }
func (s *Server) appUnlikeComment(c *gin.Context) { s.appToggleCommentLike(c, false) }

func (s *Server) appToggleCommentLike(c *gin.Context, enable bool) {
	uid := middleware.CurrentID(c)
	commentID := parseUint(c.Param("id"))
	if commentID == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var cm model.Comment
	if err := s.db.First(&cm, commentID).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "评论不存在")
			return
		}
		response.ServerError(c, "查询评论失败")
		return
	}

	var newLike bool // 本次是否产生了新点赞（用于事务成功后发站内信，幂等重复点赞不重发）
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if enable {
			rec := model.CommentLike{CommentID: commentID, UserID: uid}
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rec)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected > 0 {
				newLike = true
				return tx.Model(&model.Comment{}).Where("id = ?", commentID).
					UpdateColumn("like_count", gorm.Expr("like_count + ?", 1)).Error
			}
		} else {
			res := tx.Where("comment_id = ? AND user_id = ?", commentID, uid).
				Delete(&model.CommentLike{})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected > 0 {
				return tx.Model(&model.Comment{}).Where("id = ? AND like_count > 0", commentID).
					UpdateColumn("like_count", gorm.Expr("like_count - ?", 1)).Error
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		response.ServerError(c, "操作失败")
		return
	}

	// 新点赞 → 给评论作者发/更新聚合站内信（best-effort）。
	if newLike {
		s.emitCommentLikeMessage(cm.UserID, uid, commentID)
	}

	var refreshed model.Comment
	s.db.Select("like_count").First(&refreshed, commentID)
	response.OK(c, gin.H{"liked": enable, "like_count": refreshed.LikeCount})
}

// usersByIDs 批量拉用户（去重），返回 user_id → *User（避免 nil 解引用）。
func (s *Server) usersByIDs(ids []uint64) map[uint64]*model.User {
	out := map[uint64]*model.User{}
	if len(ids) == 0 {
		return out
	}
	seen := map[uint64]bool{}
	uniq := make([]uint64, 0, len(ids))
	for _, id := range ids {
		if id != 0 && !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	var users []model.User
	if err := s.db.Select("id, nickname, avatar").Where("id IN ?", uniq).Find(&users).Error; err != nil {
		return out
	}
	for i := range users {
		out[users[i].ID] = &users[i]
	}
	return out
}

// likedCommentIDs 返回当前用户在给定评论里点过赞的集合（uid=0 或空集合时返回空）。
func (s *Server) likedCommentIDs(uid uint64, commentIDs []uint64) map[uint64]bool {
	out := map[uint64]bool{}
	if uid == 0 || len(commentIDs) == 0 {
		return out
	}
	var rows []model.CommentLike
	if err := s.db.Select("comment_id").
		Where("user_id = ? AND comment_id IN ?", uid, commentIDs).
		Find(&rows).Error; err != nil {
		return out
	}
	for _, r := range rows {
		out[r.CommentID] = true
	}
	return out
}

// episodeBelongsToDrama 校验剧集存在且归属该剧。
func (s *Server) episodeBelongsToDrama(episodeID, dramaID uint64) bool {
	var cnt int64
	s.db.Model(&model.Episode{}).Where("id = ? AND drama_id = ?", episodeID, dramaID).Count(&cnt)
	return cnt > 0
}

// commentView 渲染一条评论。author=评论作者，replyTo=被回复者（仅回复且 @某人时非空），liked=当前用户是否点过赞。
func commentView(cm model.Comment, author, replyTo *model.User, liked bool) gin.H {
	v := gin.H{
		"id":          cm.ID,
		"drama_id":    cm.DramaID,
		"episode_id":  cm.EpisodeID, // null=剧评，有值=集评
		"parent_id":   cm.ParentID,  // null=顶层评论，有值=回复
		"user":        userBrief(cm.UserID, author),
		"content":     cm.Content,
		"like_count":  cm.LikeCount,
		"reply_count": cm.ReplyCount, // 仅顶层评论有意义
		"liked":       liked,
		"created_at":  cm.CreatedAt,
	}
	if cm.ParentID != nil {
		// 回复：带上被回复者（直接回复顶层评论时为 null）。
		if cm.ReplyToUserID != nil {
			v["reply_to_user"] = userBrief(*cm.ReplyToUserID, replyTo)
		} else {
			v["reply_to_user"] = nil
		}
	}
	return v
}

func userBrief(id uint64, u *model.User) gin.H {
	b := gin.H{"id": id, "nickname": "", "avatar": ""}
	if u != nil {
		b["nickname"] = u.Nickname
		b["avatar"] = u.Avatar
	}
	return b
}
