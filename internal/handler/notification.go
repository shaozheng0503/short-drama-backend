package handler

import (
	"fmt"
	"log"

	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

// yuanStr 把分格式化成「¥12.34」用于消息文案。
func yuanStr(cents int64) string {
	return fmt.Sprintf("¥%.2f", float64(cents)/100)
}

// sendNotification 给创作者发一条站内消息。MVP 纯文本 + 可选跳转链接。
// best-effort：发送失败只记日志，不阻断主流程（审核 / 提现等业务以主操作成功为准）。
func (s *Server) sendNotification(creatorID uint64, title, content, link string) {
	if creatorID == 0 {
		return
	}
	n := model.Notification{
		CreatorID: creatorID,
		Title:     title,
		Content:   content,
		LinkURL:   link,
	}
	if err := s.db.Create(&n).Error; err != nil {
		log.Printf("[notification] 发送失败 creator_id=%d title=%q err=%v", creatorID, title, err)
	}
}

func notificationView(n model.Notification) gin.H {
	return gin.H{
		"id":         n.ID,
		"title":      n.Title,
		"content":    n.Content,
		"link_url":   n.LinkURL,
		"is_read":    n.IsRead,
		"created_at": n.CreatedAt,
	}
}

// creatorListNotifications —— GET /v1/creator/notifications?unread_only=true
func (s *Server) creatorListNotifications(c *gin.Context) {
	cid := middleware.CurrentID(c)
	page, pageSize := paginate(c)

	q := s.db.Model(&model.Notification{}).Where("creator_id = ?", cid)
	if c.Query("unread_only") == "true" {
		q = q.Where("is_read = ?", false)
	}
	var total int64
	q.Count(&total)
	var items []model.Notification
	q.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items)

	var unread int64
	s.db.Model(&model.Notification{}).Where("creator_id = ? AND is_read = ?", cid, false).Count(&unread)

	list := make([]gin.H, 0, len(items))
	for _, n := range items {
		list = append(list, notificationView(n))
	}
	resp := pageResp(list, page, pageSize, total)
	resp["unread_count"] = unread
	response.OK(c, resp)
}

// creatorMarkNotificationRead —— POST /v1/creator/notifications/:id/read
func (s *Server) creatorMarkNotificationRead(c *gin.Context) {
	cid := middleware.CurrentID(c)
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	res := s.db.Model(&model.Notification{}).
		Where("id = ? AND creator_id = ?", id, cid).
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

// creatorMarkAllNotificationsRead —— POST /v1/creator/notifications/read-all
func (s *Server) creatorMarkAllNotificationsRead(c *gin.Context) {
	cid := middleware.CurrentID(c)
	if err := s.db.Model(&model.Notification{}).
		Where("creator_id = ? AND is_read = ?", cid, false).
		Update("is_read", true).Error; err != nil {
		response.ServerError(c, "操作失败")
		return
	}
	response.OK(c, gin.H{"ok": true})
}
