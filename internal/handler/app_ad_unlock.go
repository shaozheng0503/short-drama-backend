package handler

import (
	"errors"
	"log"

	"ai-drama-platform/internal/billing"
	"ai-drama-platform/internal/middleware"
	"ai-drama-platform/internal/model"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

// ============ App 端：看广告解锁（穿山甲激励视频） ============
//
// 链路：
//   POST /v1/app/ad-unlock/tickets           创建凭证（App 拿 ticket_id 透传给穿山甲 SDK）
//   GET  /v1/app/ad-unlock/tickets/:ticket   查询结果（广告关闭后轮询，直到 unlocked=true）
//   GET  /v1/webhooks/csj/reward             穿山甲 S2S 回调（见 webhooks_csj.go）
//
// 发奖依据是穿山甲服务端回调验签成功，客户端 onRewardArrived 不作为解锁依据。

type createAdTicketRequest struct {
	DramaID   uint64 `json:"drama_id" binding:"required"`
	EpisodeID uint64 `json:"episode_id" binding:"required"`
}

// appCreateAdUnlockTicket —— POST /v1/app/ad-unlock/tickets
// 入参：drama_id + episode_id（登录态）。出参：ticket_id / status / expire_at + SDK 参数。
func (s *Server) appCreateAdUnlockTicket(c *gin.Context) {
	uid := middleware.CurrentID(c)
	var req createAdTicketRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "drama_id / episode_id 必填")
		return
	}
	ticket, alreadyUnlocked, err := s.billing.CreateAdUnlockTicket(uid, req.DramaID, req.EpisodeID)
	if err != nil {
		switch {
		case errors.Is(err, billing.ErrEpisodeNotFound):
			response.NotFound(c, "剧集不存在")
		case errors.Is(err, billing.ErrEpisodeNotReady):
			response.InvalidParam(c, "剧集尚未就绪")
		case errors.Is(err, billing.ErrDramaNotFound):
			response.NotFound(c, "短剧不存在")
		case errors.Is(err, billing.ErrDramaNotAvailable):
			response.InvalidParam(c, "短剧未上架或已下架")
		case errors.Is(err, billing.ErrOrderEpisodeMatch):
			response.InvalidParam(c, "drama_id 与 episode_id 不匹配")
		case errors.Is(err, billing.ErrEpisodeFree):
			response.InvalidParam(c, "该剧集为免费集，无需解锁")
		case errors.Is(err, billing.ErrAdUnlockDisabled):
			response.Fail(c, response.CodeForbidden, "该剧未开启看广告解锁")
		case errors.Is(err, billing.ErrAdUnlockNotConfig):
			log.Printf("[ad-unlock] SecurityKey 未配置，拒绝发凭证 user=%d", uid)
			response.FailWithData(c, response.CodeThirdPartyError, "看广告解锁暂不可用", nil)
		default:
			log.Printf("[ad-unlock] create ticket err=%v", err)
			response.ServerError(c, "创建广告解锁凭证失败")
		}
		return
	}
	if alreadyUnlocked {
		response.OK(c, gin.H{"already_unlocked": true})
		return
	}
	// ticket_id 即穿山甲 SDK 的 user_id 透传值；App 侧还需要 AppId / 代码位 ID 拉广告
	response.OK(c, gin.H{
		"ticket_id":    ticket.TicketID,
		"status":       ticket.Status,
		"expire_at":    ticket.ExpireAt,
		"drama_id":     ticket.DramaID,
		"episode_id":   ticket.EpisodeID,
		"csj_app_id":   s.cfg.CSJAppID,
		"csj_code_id":  s.cfg.CSJCodeID,
		"reward_name":  s.cfg.CSJRewardName,
		"reward_amount": s.cfg.CSJRewardAmount,
	})
}

// appGetAdUnlockTicket —— GET /v1/app/ad-unlock/tickets/:ticket_id
// 出参：status（pending/rewarded/expired）+ unlocked（是否已解锁，含历史付费解锁）。
func (s *Server) appGetAdUnlockTicket(c *gin.Context) {
	uid := middleware.CurrentID(c)
	ticketID := c.Param("ticket_id")
	if ticketID == "" {
		response.InvalidParam(c, "ticket_id 必填")
		return
	}
	ticket, unlocked, err := s.billing.GetAdUnlockTicket(uid, ticketID)
	if err != nil {
		switch {
		case errors.Is(err, billing.ErrTicketNotFound):
			response.NotFound(c, "广告解锁凭证不存在")
		case errors.Is(err, billing.ErrTicketNotOwned):
			response.Forbidden(c, "广告解锁凭证不属于当前用户")
		default:
			log.Printf("[ad-unlock] get ticket err=%v", err)
			response.ServerError(c, "查询解锁结果失败")
		}
		return
	}
	response.OK(c, gin.H{
		"ticket_id":   ticket.TicketID,
		"status":      ticket.Status,
		"unlocked":    unlocked,
		"drama_id":    ticket.DramaID,
		"episode_id":  ticket.EpisodeID,
		"expire_at":   ticket.ExpireAt,
		"rewarded_at": ticket.RewardedAt,
		"trans_id":    ticket.TransID,
	})
}

// ============ 管理端：看广告解锁开关 ============

// adminSetAdUnlockEnabled —— PUT /v1/admin/dramas/:id/ad-unlock
// 管理员逐剧开启「看广告解锁」；默认关闭（有的剧只能付费，不能白嫖）。
func (s *Server) adminSetAdUnlockEnabled(c *gin.Context) {
	id := parseUint(c.Param("id"))
	if id == 0 {
		response.InvalidParam(c, "id 不合法")
		return
	}
	var drama model.Drama
	if err := s.db.First(&drama, id).Error; err != nil {
		if isNotFound(err) {
			response.NotFound(c, "短剧不存在")
			return
		}
		response.ServerError(c, "查询短剧失败")
		return
	}
	var req struct {
		AdUnlockEnabled bool `json:"ad_unlock_enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidParam(c, "ad_unlock_enabled 必填")
		return
	}
	if err := s.db.Model(&drama).Update("ad_unlock_enabled", req.AdUnlockEnabled).Error; err != nil {
		response.ServerError(c, "更新看广告解锁开关失败")
		return
	}
	s.db.First(&drama, id)
	response.OK(c, dramaAdminView(drama, s.nameOfCategory(drama.CategoryID), s.nameOfCreator(drama.CreatorID)))
}
