# DramaBackend 开发 & 自测记录

> 截至 2026-05-20
> 后端负责人：黄少政
> 文档作用：把已实现接口、已跑通的自测路径、未做项 / 阻塞项一次性列清楚，给后续联调和 PM 同步看。

---

## 一、阶段进度总览

| 阶段 | 范围 | 状态 |
|---|---|---|
| Phase 1 | 工程骨架 + 三类身份登录 / `/me` + 短信验证码 | ✅ 完成 + 自测通过 |
| Phase 2 | APP 内容只读 / 互动 + 管理中台分类 / 短剧 / 剧集 CRUD + 播放地址 | ✅ 完成 + 自测通过 |
| Phase 3 | 商品 / 订单 / 支付 / 解锁 / 分账 / 提现 / 审核 / 合同 + 敏感字段加密 + 支付 & 电子签接入位 | ✅ 完成 + 自测通过 |
| Phase 4 | 联调、监控、上线准备 | ⏳ 未开始 |

钱相关链路（执行文档第七节）**全部走通**：下单 → 支付回调 → 解锁 → 分账 → 提现申请 → 审核通过 → 标记打款，每一步都走事务 + 行锁，余额账目在自测中数字对齐。

---

## 二、已实现接口清单

> 共 **52 个**业务接口 + 2 个 webhooks + 1 个健康检查。

### 2.1 通用 / 健康（2）

| Method | Path | 鉴权 | 说明 |
|---|---|---|---|
| GET  | `/health` | - | 健康检查 |
| POST | `/v1/common/sms/send` | - | 短信验证码（scene=login/creator_login），dev 模式回显 `dev_code` |

### 2.2 APP 端（22）

| Method | Path | 鉴权 | 说明 |
|---|---|---|---|
| POST | `/v1/app/auth/login` | - | 手机号 + 验证码，自动注册 |
| GET  | `/v1/app/me` | APP | 当前用户 |
| PUT  | `/v1/app/me` | APP | 更新昵称 / 头像 |
| GET  | `/v1/app/home` | 公开 | 首页（分类 / 推荐 / 热门） |
| GET  | `/v1/app/dramas` | 公开 | 短剧列表（category_id / sort=hot|new / 分页） |
| GET  | `/v1/app/dramas/:id` | 公开 + 软鉴权 | 详情，带 token 时扩展 is_liked / is_favorited / last_watch |
| GET  | `/v1/app/dramas/:id/episodes` | 公开 + 软鉴权 | 剧集列表，带 token 时填 is_locked |
| GET  | `/v1/app/search?q=` | 公开 | 搜索短剧（title / description 模糊） |
| GET  | `/v1/app/products` | 公开 | 商品列表 |
| GET  | `/v1/app/episodes/:id/play` | APP | 播放地址；付费集未解锁返回 42001 |
| POST | `/v1/app/play-history` | APP | 上报观看进度（upsert） |
| GET  | `/v1/app/play-history` | APP | 观看历史分页 |
| POST | `/v1/app/dramas/:id/like` | APP | 点赞 |
| DELETE | `/v1/app/dramas/:id/like` | APP | 取消点赞 |
| POST | `/v1/app/dramas/:id/favorite` | APP | 收藏 |
| DELETE | `/v1/app/dramas/:id/favorite` | APP | 取消收藏 |
| GET  | `/v1/app/me/favorites` | APP | 我的收藏 |
| POST | `/v1/app/dramas/:id/share` | APP | 分享埋点 |
| POST | `/v1/app/orders` | APP | 创建订单（Idempotency-Key 已识别 + 业务幂等） |
| GET  | `/v1/app/orders/:order_no` | APP | 查询订单状态 |
| POST | `/v1/app/episodes/:id/unlock` | APP | 主动解锁（webhook 已写则幂等返回） |

### 2.3 创作者端（10）

| Method | Path | 鉴权 | 说明 |
|---|---|---|---|
| POST | `/v1/creator/auth/login` | - | 手机号 + 验证码，自动注册 |
| GET  | `/v1/creator/me` | Creator | 当前创作者 |
| PUT  | `/v1/creator/me/profile` | Creator | 资料（含身份证 / 银行卡 AES-GCM 加密 + 自动 verified） |
| GET  | `/v1/creator/dashboard` | Creator | 首页汇总（累计收益、可提现、今日收益、今日播放） |
| GET  | `/v1/creator/dramas` | Creator | 我的短剧 + 累计收益 |
| GET  | `/v1/creator/dramas/:id/stats` | Creator | 单部短剧统计（range=7d/30d） |
| GET  | `/v1/creator/income` | Creator | 收益按日汇总 |
| POST | `/v1/creator/withdrawals` | Creator | 提现申请（verified + 金额门槛 + 单 pending） |
| GET  | `/v1/creator/withdrawals` | Creator | 提现列表 |
| GET  | `/v1/creator/contracts` / `:id` | Creator | 合同列表 / 详情 |

### 2.4 管理中台（28）

| Method | Path | 说明 |
|---|---|---|
| POST | `/v1/admin/auth/login` | 账号密码登录 |
| GET  | `/v1/admin/me` | 当前管理员 |
| GET  | `/v1/admin/dashboard` | 首页概览（用户 / 创作者 / 短剧 / 今日 / 待处理） |
| GET / POST | `/v1/admin/categories` | 分类列表 / 创建 |
| PUT  | `/v1/admin/categories/:id` | 分类更新 |
| GET  | `/v1/admin/dramas` | 短剧列表（status / keyword / category_id / creator_id 筛选） |
| POST / GET / PUT | `/v1/admin/dramas` `/v1/admin/dramas/:id` | 短剧 CRUD |
| POST | `/v1/admin/dramas/:id/publish` | 上架（至少 1 集 ready） |
| POST | `/v1/admin/dramas/:id/offline` | 下架 |
| GET / POST | `/v1/admin/dramas/:id/episodes` | 剧集列表 / 创建 |
| PUT  | `/v1/admin/episodes/:id` | 剧集更新 |
| GET  | `/v1/admin/creators` | 创作者列表 |
| POST | `/v1/admin/creators` | 补录创作者 |
| GET / PUT | `/v1/admin/creators/:id` | 详情 / 更新 |
| POST | `/v1/admin/creators/:id/ban` | 封禁 |
| GET  | `/v1/admin/orders` | 订单列表（status / payment_method / user_id） |
| GET  | `/v1/admin/orders/:order_no` | 订单详情 |
| GET  | `/v1/admin/withdrawals` | 提现列表 |
| POST | `/v1/admin/withdrawals/:id/approve` | 审核通过 |
| POST | `/v1/admin/withdrawals/:id/reject` | 驳回（frozen 回 balance） |
| POST | `/v1/admin/withdrawals/:id/mark-paid` | 标记打款（扣 frozen + 写流水） |
| GET / POST | `/v1/admin/contracts` | 合同列表 / 创建 |
| GET  | `/v1/admin/contracts/:id` | 合同详情 |
| POST | `/v1/admin/contracts/:id/esign` | 发起电子签（stub，返回 60001 + 提示） |

### 2.5 Webhooks（2）

| Method | Path | 说明 |
|---|---|---|
| POST | `/v1/webhooks/wechat/pay` | 微信支付回调（dev 模式可直接 POST {order_no, amount_cents, paid:true}） |
| POST | `/v1/webhooks/alipay/pay` | 支付宝支付回调 |

---

## 三、已自测路径

> 每条都跑过实际 HTTP 调用，并人工 / SQL 校对了结果。

### 3.1 三端登录 + 鉴权隔离

- ✅ 短信发送 dev 模式回显 `dev_code`，60s 频控 → 40901
- ✅ APP 登录 / 创作者登录自动注册；admin 用 username+password
- ✅ APP token 调 `/admin/me` → 40301（"身份与接口不匹配"）
- ✅ APP token 调 `/creator/me` → 40301
- ✅ admin token 调 `/app/me` → 40301
- ✅ 无 Bearer / 错误 token → 40101

### 3.2 内容管理 → 浏览 → 播放

- ✅ admin 建分类 / 短剧 / 3 集剧集（带 video_url 自动 ready）
- ✅ 上架前没 ready 剧集 → publish 拒绝 40001
- ✅ 上架后 APP `GET /v1/app/home` 看到推荐 / 热门
- ✅ 同一短剧重复 episode_no → 40901
- ✅ 匿名 / 登录两种方式调详情、剧集列表
- ✅ 剧集列表 `is_free` / `is_locked` 按 `free_episodes` 正确划分
- ✅ 免费集播放：返回 play_url + next_episode_id；play_count + 1
- ✅ 付费集未解锁播放 → 42001 + `{need_unlock:true, price_cents:600}`

### 3.3 互动

- ✅ like / unlike 幂等（重复点赞计数不变）
- ✅ favorite / unfavorite
- ✅ 软鉴权：带 APP token 的详情接口返回真实 `is_liked / is_favorited / last_watch`
- ✅ `/me/favorites` 分页列表
- ✅ share 埋点接口

### 3.4 支付主链路（核心）

| 步骤 | 验证内容 | 结果 |
|---|---|---|
| 创建付费订单 | 600 cents，wechat dev provider 返回 stub pay_params | ✅ |
| 重复下单同 episode | 返回**同一个 order_no**（业务幂等：复用 pending） | ✅ |
| 模拟 webhook | `POST /v1/webhooks/wechat/pay {order_no, paid:true}` → ack | ✅ |
| 订单状态 | `GET /v1/app/orders/:order_no` → status=paid, paid_at, platform_trade_no | ✅ |
| 自动写解锁 | `episode_unlocks` 表里 user_id + episode_id 唯一行 | ✅ |
| 主动 unlock 接口 | 幂等：再次调用不创建新行 | ✅ |
| 播放原付费集 | 返回 play_url，不再 42001 | ✅ |
| 回调幂等 | 重复 POST webhook → ack，不重复分账 | ✅ |

### 3.5 创作者收益分账

- 配置：`CREATOR_SHARE_RATE=0.5`
- 一笔 600 分订单支付成功后：
  - ✅ `creators.total_income_cents += 300`
  - ✅ `creators.balance_cents += 300`
  - ✅ `creator_stats_daily.income_cents += 300`（按 creator+drama+today 聚合）
- 重复回调时分账**不会重复执行**（订单已 paid 直接 return nil）

### 3.6 敏感字段加密 + 实名认证

- ✅ 创作者 verify_status=pending 时直接提现 → 40301
- ✅ PUT `/v1/creator/me/profile` 同时传 name/id_card_no/bank_name/bank_card_no
- ✅ 数据库里：`bank_card_no_enc` 是 base64 密文（长 60 字符），`bank_card_last4=7890`，`verify_status=verified`
- ✅ 接口返回脱敏值 `bank_card_no_masked: "***7890"`
- ✅ 服务启动时 `DATA_ENCRYPTION_KEY` 缺失会打 warning 并拒绝写资料

### 3.7 提现状态机

| 步骤 | 验证 | 结果 |
|---|---|---|
| 金额 < `MIN_WITHDRAWAL_CENTS` (1000) | 40001 "最低提现门槛" | ✅ |
| 提现 10000 | 成功，返回 withdrawal_no + bank_card_no_snapshot | ✅ |
| 同时已有 pending 再申请 | 40901 | ✅ |
| 行锁 + 余额变更 | `balance_cents 20000 → 10000`、`frozen_cents 0 → 10000` | ✅ |
| admin approve | status=approved，余额不动 | ✅ |
| admin mark-paid | status=paid + transaction_no + paid_at，frozen → 0 | ✅ |
| total_income_cents 不变 | 提现不扣累计收益 | ✅（仍 20000） |

整体余额公式 `total_income = balance + frozen + 已 paid 提现` 在自测里数字对齐：`20000 = 10000 + 0 + 10000`。

### 3.8 后台 dashboard

- ✅ admin dashboard：user_count / creator_count / drama_count / pending_drama_count / pending_withdrawal_count
- ✅ creator dashboard：total_income / balance / frozen / drama_count / today_income / today_play

### 3.9 合同

- ✅ admin 创建合同（创作者 + 短剧校验）
- ✅ creator 列表 / 详情（权限隔离：他人合同 403）
- ✅ admin 发起电子签 → 60001 + "电子签 SDK 尚未接入" 提示（stub）

---

## 四、关键设计与原则落地点

### 4.1 钱相关（执行文档第七节）

- `creator_amount = order.amount_cents * CREATOR_SHARE_RATE`，**向下取整到分** —— `int64(float64(amount) * rate)` 自动截断
- 所有改余额的操作都在 `db.Transaction` + `clause.Locking{Strength: "UPDATE"}` 行锁内
- 回调先 SELECT FOR UPDATE 查订单，已 paid 则直接 return nil（幂等）
- 重复回调测试：第二次 webhook 不会改任何状态
- pending 默认 30 分钟（`ORDER_PENDING_TTL_SECONDS`），后台 ticker 每 60 秒扫一次过期 → closed

### 4.2 三类身份 JWT 隔离

- claim 里写 `subject=app/creator/admin` + `subject_id`
- 中间件 `RequireApp/RequireCreator/RequireAdmin` 校验 subject
- 软鉴权 `TryAppUserID(cfg)`：仅在合法 APP token 时返回 user_id，匿名 / 错误 token 都返回 0；用于 APP 详情可匿名 + 登录后扩展

### 4.3 第三方接入位（设计模式：可替换 Provider）

- SMS：`sms.Provider` 接口 + `DevProvider` / `TencentProvider`（stub）；`SelectProvider()` 按 `SMS_DEV_MODE` + 配置完整性选
- Payment：`payment.Registry`，按 method 路由到 `WechatProvider` / `AlipayProvider`（均为 stub）；dev 模式或配置不齐时退回 `DevProvider`
- ESign：`adminEsignContract` 是 stub，写在 handler 里，等 SDK 接入后改函数体
- 全部留好 TODO + 注释里贴了 SDK 调用样板（见 `internal/sms/tencent_provider.go`、`internal/payment/wechat_provider.go`、`internal/payment/alipay_provider.go`）

### 4.4 数据库

- AutoMigrate 15 张表：users / sms_codes / admins / creators / categories / dramas / episodes / play_histories / user_actions / products / orders / episode_unlocks / contracts / withdrawals / creator_stats_daily
- 首次启动种 `admin/admin123` + 默认商品「单集解锁」
- 唯一索引：`(drama_id, episode_no)`、`(user_id, episode_id)` × 2（unlock & history）、`(user_id, drama_id, action)`、`(creator_id, drama_id, stat_date)`

---

## 五、已知未做项 / 阻塞

### 5.1 第三方真实接入（卡点都在外部）

| 项目 | 阻塞 |
|---|---|
| 腾讯云 SMS 真实发送 | 需要新建「验证码」类目模板等审核；当前内部短信模板全是业务通知。已在执行文档摘要中提示「共绩」/「共臻」公司名核对 |
| 微信支付 V3 | 需要商户号 + APIv3 key + 平台证书 + 回调域名 HTTPS |
| 支付宝 OpenAPI | 需要 AppID + 应用私钥 + 支付宝公钥 |
| 腾讯云点播 | 上传签名 / 回调还没做，目前剧集 video_url 由 admin 直接填 |
| 腾讯电子签 | 需要 ess 模板 ID + 子用户权限 |
| 实人认证 / 银行卡四要素 | 资料更新接口当前是「字段齐全直接 verified」，真接入后改逻辑 |

### 5.2 工程未做项

- ❌ Redis 强幂等（当前 Idempotency-Key 只 log，依赖"复用 pending + 已 paid 直返"业务幂等）
- ❌ 对账脚本（执行文档 7.8）
- ❌ 退款（MVP 范围外，文档 7.2 明确「refunded 仅运营手工标记，不开放 API」）
- ❌ 评论接口（API 文档 4.14 / 4.15，按"可砍"清单暂未做）
- ❌ 图片上传签名 `POST /v1/common/uploads/image-sign`（依赖 COS / 云点播账号）
- ❌ 操作日志、消息中心、VIP、Banner、推荐算法等：MVP 文档第十四节明确不做

### 5.3 安全 & 部署

- `DATA_ENCRYPTION_KEY` 当前用 `openssl rand -base64 32` 本地生成；上线必须放密钥管理服务，并保证多实例共用一份
- `.env` 已被 `.gitignore` 拦住，但请确认部署环境用环境变量注入而不是文件分发
- 没接 HTTPS / 反向代理 / Rate Limit / 接口审计日志，留给联调阶段

---

## 六、自测环境快照

| 项 | 值 |
|---|---|
| Postgres | brew install postgresql@16，库 `ai_drama` |
| Go | 见 `go.mod`（gin v1.12 / gorm v1.31） |
| API 监听 | `:18080`（避开本地 8080） |
| 启动命令 | 见下 |

```bash
DATABASE_DSN='host=localhost user=huangshaozheng dbname=ai_drama port=5432 sslmode=disable TimeZone=Asia/Shanghai' \
JWT_SECRET='dev-secret' \
APP_ADDR=':18080' \
SMS_DEV_MODE=true \
PAYMENT_DEV_MODE=true \
DATA_ENCRYPTION_KEY="$(openssl rand -base64 32)" \
CREATOR_SHARE_RATE=0.5 \
MIN_WITHDRAWAL_CENTS=1000 \
go run ./cmd/api
```

---

## 七、下一阶段建议

按执行文档第五节进入第 4 周「联调、测试、上线准备」：

1. 把上面 5.1 阻塞项一项一项推动（SMS 模板审核、商户证书申请、电子签开通）。每一项审核 / 申请到达后只改一个 Provider 文件的 stub。
2. 真实联调时把 `SMS_DEV_MODE=false` 和 `PAYMENT_DEV_MODE=false`，缺哪一项 provider 会自动 fallback 到 dev + warning，方便定位。
3. 起一台测试 Postgres + HTTPS + Nginx 反代，把所有 webhooks 域名配上。
4. 用执行文档第十二节「验收主链路」14 条人工跑一遍，与本文件第三节对照。
5. 准备好对账脚本（7.8）和过期订单关闭日志的报警。
