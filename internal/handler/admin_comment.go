package handler

import (
	"strings"

	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

func (s *Server) adminListComments(c *gin.Context) {
	page, pageSize := paginate(c)
	q := s.db.Model(&model.Comment{})
	if v := parseUint(c.Query("drama_id")); v > 0 {
		q = q.Where("drama_id = ?", v)
	}
	if v := parseUint(c.Query("user_id")); v > 0 {
		q = q.Where("user_id = ?", v)
	}
	if v := strings.TrimSpace(c.Query("keyword")); v != "" {
		q = q.Where("content LIKE ?", "%"+v+"%")
	}
	var total int64
	q.Count(&total)
	var comments []model.Comment
	if err := q.Order("created_at desc").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&comments).Error; err != nil {
		response.ServerError(c, "查询评论失败")
		return
	}
	userMap := s.collectCommentUsers(comments)
	dramaTitles := s.attachDramaTitlesForComments(comments)
	list := make([]gin.H, 0, len(comments))
	for _, cm := range comments {
		view := commentView(cm, userMap[cm.UserID])
		view["drama_id"] = cm.DramaID
		view["drama_title"] = dramaTitles[cm.DramaID]
		list = append(list, view)
	}
	response.OK(c, pageResp(list, page, pageSize, total))
}

func (s *Server) attachDramaTitlesForComments(comments []model.Comment) map[uint64]string {
	ids := make([]uint64, 0)
	seen := map[uint64]bool{}
	for _, cm := range comments {
		if !seen[cm.DramaID] {
			ids = append(ids, cm.DramaID)
			seen[cm.DramaID] = true
		}
	}
	titles := map[uint64]string{}
	if len(ids) == 0 {
		return titles
	}
	var rows []struct {
		ID    uint64
		Title string
	}
	s.db.Table("dramas").Select("id, title").Where("id IN ?", ids).Scan(&rows)
	for _, r := range rows {
		titles[r.ID] = r.Title
	}
	return titles
}

func (s *Server) adminDeleteComment(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	res := s.db.Delete(&model.Comment{}, id)
	if res.Error != nil {
		response.ServerError(c, "删除评论失败")
		return
	}
	if res.RowsAffected == 0 {
		response.NotFound(c, "评论不存在")
		return
	}
	response.OK(c, gin.H{"id": id, "deleted": true})
}
