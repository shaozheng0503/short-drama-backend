package handler

import (
	"errors"
	"log"
	"net/http"

	"ai-drama-platform/internal/billing"
	"ai-drama-platform/internal/response"

	"github.com/gin-gonic/gin"
)

// webhookCSJReward —— GET /v1/webhooks/csj/reward
//
// 穿山甲（CSJ/Pangle）/ GroMore 聚合 激励视频「服务端奖励验证」回调。
// 官方协议（与微信/支付宝 POST 回调不同，是 GET 查询参数）：
//
//	GET /v1/webhooks/csj/reward?user_id=xxx&trans_id=yyy&reward_name=zzz&reward_amount=1&extra=...&sign=...
//
//	- user_id：App 请求广告时 SDK 透传的字符串（本项目约定 = 广告解锁 ticket_id）
//	- trans_id：平台侧唯一流水号（幂等键；GroMore 下由 GroMore 生成）
//	- sign：sha256(SecurityKey:trans_id) hex 小写（GroMore 的 m-key 与穿山甲 SecurityKey 算法一致）
//	- GroMore 额外参数：mediation_rit（代码位）/ prime_rit（广告位）/ adn_name / ecpm
//	  ecpm = 本次展示的 eCPM（string，无数据时 null/空）→ 单次收益入账 channel_income_daily
//	  （渠道=狼之短剧，batch_no=ad_auto），单位由 CSJ_ECPM_UNIT 控制（默认分）
//
// 响应同时兼容两种协议（JSON 字段冗余返回，各自取各自认的字段）：
//	- 穿山甲直营：{"isValid": true}
//	- GroMore 聚合：{"is_verify": true, "reason": 0}
//	  GroMore 文档明确：格式不对会导致客户端 is_verify=false，奖励发不下去
//
// 安全：验签失败返回 200 + false（不是 401）——平台对 4xx/5xx 会重试，
// 验签失败属于永久性错误（重试也不会变对），直接确认并让平台停止重试，同时日志告警。
func (s *Server) webhookCSJReward(c *gin.Context) {
	// Query() 已做 URL 解码；参数缺失按验签失败处理
	userID := c.Query("user_id")
	transID := c.Query("trans_id")
	rewardName := c.Query("reward_name")
	extra := c.Query("extra")
	sign := c.Query("sign")
	ecpm := c.Query("ecpm")

	// 回调拒绝（永久性，不重试）：两种协议字段都带上
	replyReject := func() {
		c.JSON(http.StatusOK, gin.H{"isValid": false, "is_verify": false, "reason": 1})
	}

	if transID == "" || sign == "" {
		log.Printf("[csj-callback] 缺少必要参数 trans_id/sign，拒绝。user_id=%s extra=%s", userID, extra)
		replyReject()
		return
	}

	result, err := s.billing.HandleCSJRewardCallback(userID, transID, rewardName, extra, sign, ecpm)
	if err != nil {
		switch {
		case errors.Is(err, billing.ErrCSJSignInvalid):
			log.Printf("[csj-callback] 验签失败 trans_id=%s user_id=%s（疑似伪造或 SecurityKey/m-key 不匹配）", transID, userID)
			replyReject()
			return
		case errors.Is(err, billing.ErrTicketNotFound):
			log.Printf("[csj-callback] ticket 不存在 user_id=%s trans_id=%s（凭证过期被清理或伪造）", userID, transID)
			replyReject()
			return
		case errors.Is(err, billing.ErrAdUnlockNotConfig):
			log.Printf("[csj-callback] SecurityKey 未配置，无法验签 trans_id=%s", transID)
			replyReject()
			return
		case errors.Is(err, billing.ErrCSJUserMismatch):
			log.Printf("[csj-callback] user_id 与凭证不匹配 user_id=%s trans_id=%s", userID, transID)
			replyReject()
			return
		default:
			// DB 等临时故障：500 让平台重试
			log.Printf("[csj-callback] 处理失败（将重试）trans_id=%s err=%v", transID, err)
			response.WebhookRetry(c, "处理失败")
			return
		}
	}

	if !result.Valid {
		// 已 expired / duplicate：永久性业务拒绝，确认接收但验证不过
		log.Printf("[csj-callback] 拒绝发奖 trans_id=%s ticket=%s status=%s", transID, result.TicketID, result.Status)
		replyReject()
		return
	}
	log.Printf("[csj-callback] 发奖成功 ticket=%s trans_id=%s status=%s", result.TicketID, transID, result.Status)
	c.JSON(http.StatusOK, gin.H{"isValid": true, "is_verify": true, "reason": 0})
}

