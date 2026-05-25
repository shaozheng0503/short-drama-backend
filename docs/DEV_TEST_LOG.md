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
| Phase 4 | 联调、监控、上线准备 | ✅ 工程增强完成，自测通过；第三方真实联调待外部资质 |

钱相关链路（执行文档第七节）**全部走通**：下单 → 支付回调 → 解锁 → 分账 → 提现申请 → 审核通过 → 标记打款，每一步都走事务 + 行锁，余额账目在自测中数字对齐。

---

## 二、已实现接口清单

> 共 **52 个**业务接口 + 2 个 webhooks + 2 个健康 / 就绪检查。

### 2.1 通用 / 健康（3）

| Method | Path | 鉴权 | 说明 |
|---|---|---|---|
| GET  | `/health` | - | 存活检查 |
| GET  | `/ready` | - | 就绪检查（数据库 ping） |
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

### 3.10 账务对账命令

- ✅ 新增 `go run ./cmd/reconcile`，只读直连 `DATABASE_DSN`，不触发 AutoMigrate / 种子数据
- ✅ 检查已支付订单是否缺少解锁记录
- ✅ 检查创作者 `total_income_cents` 是否等于已支付订单按 `CREATOR_SHARE_RATE` 计算出的分账收入
- ✅ 检查 `frozen_cents` 是否等于 pending / approved 提现总额
- ✅ 检查余额公式：`total_income = balance + frozen + 已 paid 提现`
- ✅ 检查 `creator_stats_daily.income_cents` 是否与已支付订单按天聚合一致
- ✅ 临时干净库自测：1 笔 600 分已支付订单，50% 分账，输出 `status: OK`
- ✅ 当前历史测试库自测：能识别人工造数导致的 `creator_id=1 total_income_cents=20000` 与订单分账 300 不一致

### 3.11 过期订单关闭

- ✅ 增强后台 `CloseExpiredOrders`：事务内锁定过期 pending 订单后批量关闭
- ✅ 后台 ticker 日志增加：关闭数量、最早过期时间、样例订单号（最多 5 个）
- ✅ 新增一次性命令：`go run ./cmd/close-expired-orders`
- ✅ 临时库自测：`EXPIRED_PENDING_1` → `closed`
- ✅ 临时库自测：`FRESH_PENDING_1` 保持 `pending`，`PAID_EXPIRED_1` 保持 `paid`

### 3.12 上线前配置检查

- ✅ 新增 `go run ./cmd/check-config`：开发模式检查关键配置，默认问题以 warning 输出
- ✅ 新增 `go run ./cmd/check-config --prod`：生产严格检查，关键问题返回 exit code 1
- ✅ 检查项覆盖：`DATABASE_DSN`、`JWT_SECRET`、`DATA_ENCRYPTION_KEY`、默认管理员账号密码、`CREATOR_SHARE_RATE`、提现门槛、订单 TTL
- ✅ 检查短信：`SMS_DEV_MODE=false` 时要求腾讯云短信核心配置齐全
- ✅ 检查支付：`PAYMENT_DEV_MODE=false` 时要求微信 / 支付宝核心配置齐全
- ✅ 自测：默认配置输出 `OK_WITH_WARNINGS`
- ✅ 自测：模拟完整生产配置输出 `status: OK`
- ✅ 自测：生产模式缺配置输出 `status: FAILED` 且退出码为 1

### 3.13 存活 / 就绪检查

- ✅ `/health` 保持轻量存活检查，返回 `status=ok` 和 `uptime_seconds`
- ✅ 新增 `/ready`，使用 2 秒超时 `PingContext` 检查数据库连接
- ✅ 数据库不可用时 `/ready` 返回 HTTP 503 + `status=not_ready`
- ✅ 本地服务自测：`GET /health` → HTTP 200 / `code=0` / `status=ok`
- ✅ 本地服务自测：`GET /ready` → HTTP 200 / `code=0` / `status=ready`

### 3.14 API 服务优雅关闭

- ✅ `cmd/api` 从 Gin `Run` 改为标准 `http.Server`
- ✅ 支持监听 `SIGINT` / `SIGTERM`，收到信号后执行 `httpServer.Shutdown`
- ✅ 新增 `APP_SHUTDOWN_TIMEOUT_SECONDS`，默认 10 秒
- ✅ `go run ./cmd/check-config` 已检查 shutdown timeout 必须大于 0
- ✅ 本地服务自测：启动后 `GET /ready` → HTTP 200 / `status=ready`
- ✅ 本地服务自测：发送 `SIGTERM` 后输出 `api server stopped gracefully`，进程退出码为 0

### 3.15 后台任务停止

- ✅ `StartBackground` 改为 `StartBackground(ctx)`，后台 ticker 跟随服务退出信号停止
- ✅ 过期订单关闭 ticker 收到 context cancel 后输出 `[bg] background tasks stopped`
- ✅ 本地服务自测：启动后 `GET /ready` → HTTP 200 / `status=ready`
- ✅ 本地服务自测：发送 `SIGTERM` 后同时出现 `[bg] background tasks stopped` 和 `api server stopped gracefully`
- ✅ 本地服务自测：进程退出码为 0

### 3.16 CORS 跨域联调

- ✅ 新增 `CORS_ALLOWED_ORIGINS`，逗号分隔配置允许跨域的前端 Origin
- ✅ 默认允许本地开发常用 Origin：`localhost:3000` / `localhost:5173` / `127.0.0.1:3000` / `127.0.0.1:5173`
- ✅ 支持 `OPTIONS` 预检，允许 `Authorization / Content-Type / Idempotency-Key`
- ✅ `go run ./cmd/check-config --prod` 已禁止生产环境使用 `CORS_ALLOWED_ORIGINS=*`
- ✅ 本地服务自测：允许 Origin `http://localhost:5173` 预检返回 HTTP 204
- ✅ 本地服务自测：响应头回显 `Access-Control-Allow-Origin: http://localhost:5173`
- ✅ 本地服务自测：未在白名单内的 Origin 不返回 `Access-Control-Allow-Origin`

### 3.17 Redis 强幂等

- ✅ 新增 Redis 配置：`REDIS_ADDR` / `REDIS_PASSWORD` / `REDIS_DB` / `IDEMPOTENCY_TTL_SECONDS`
- ✅ 新增 `internal/redisclient`：服务启动时连接 Redis，未配置时 dev 环境自动停用 Redis 能力
- ✅ 新增 `internal/idempotency`：基于 Redis `SETNX` 锁 + 响应缓存实现 `Idempotency-Key` 强幂等
- ✅ 接入钱相关写接口：`POST /v1/app/orders`、`POST /v1/creator/withdrawals`
- ✅ 同一主体 + 同一路径 + 同一 `Idempotency-Key` 重复请求直接返回第一次响应
- ✅ 同一 `Idempotency-Key` 携带不同请求体时返回 40901
- ✅ `go run ./cmd/check-config --prod` 已要求生产环境配置 `REDIS_ADDR`
- ✅ 临时 Redis + 临时数据库自测：同一 `Idempotency-Key` 重复下单只生成 1 条订单
- ✅ 临时 Redis + 临时数据库自测：同一 `Idempotency-Key` 重复提现只生成 1 条提现

### 3.18 后台操作审计

- ✅ 新增 `operation_logs` 表，AutoMigrate 已纳入
- ✅ 新增后台审计中间件：记录非 GET / OPTIONS 的管理后台操作
- ✅ 审计字段包含：管理员 ID、method、path、full_path、action、resource_type、resource_id、HTTP 状态、业务响应 code、IP、User-Agent、时间
- ✅ 不记录请求体，避免身份证、银行卡、token 等敏感明文落库
- ✅ 财务审核接口已覆盖：`approve` / `reject` / `mark-paid`
- ✅ 临时数据库自测：`POST /v1/admin/categories` 写入 `post.categories`
- ✅ 临时数据库自测：`POST /v1/admin/withdrawals/1/approve` 写入 `post.withdrawals.id.approve`，包含 `actor_id=1`、`resource_type=withdrawals`、`resource_id=1`

### 3.19 API Rate Limit

- ✅ 新增配置：`RATE_LIMIT_ENABLED` / `RATE_LIMIT_RPS` / `RATE_LIMIT_BURST`
- ✅ 新增 `internal/ratelimit`，基于 IP + 路径做 token bucket 限流
- ✅ `/health` / `/ready` / `OPTIONS` 不限流，避免影响探针和 CORS 预检
- ✅ 超限时返回 HTTP 429 + 业务码 `42901`
- ✅ `go run ./cmd/check-config --prod` 已要求生产环境开启 Rate Limit
- ✅ 临时服务自测：`RATE_LIMIT_RPS=0.1`、`RATE_LIMIT_BURST=2` 时，第 3 / 4 次请求返回 HTTP 429
- ✅ 临时服务自测：`GET /health` 不受限流影响

### 3.20 HTTPS / Nginx / 部署文档

- ✅ 新增 `docs/DEPLOYMENT.md`
- ✅ 覆盖推荐拓扑：Nginx HTTPS → API → PostgreSQL / Redis
- ✅ 给出生产关键环境变量、Nginx HTTPS 示例、systemd 示例
- ✅ 说明 `/health` / `/ready` 探针、CORS、支付回调 HTTPS 域名
- ✅ 说明 `cmd/check-config --prod`、`cmd/reconcile`、`cmd/close-expired-orders` 运维命令
- ✅ 模拟生产环境变量自测：`go run ./cmd/check-config --prod` 输出 `status: OK`

### 3.21 Webhook 告警

- ✅ 新增配置：`ALERT_ENABLED` / `ALERT_WEBHOOK_URL` / `ALERT_TIMEOUT_SECONDS`
- ✅ 新增 `internal/alert`，异步发送 JSON Webhook
- ✅ 已接入事件：`expired_orders_closed`、`close_expired_orders_failed`、`payment_webhook_failed`
- ✅ 告警发送失败只写日志，不阻断订单、支付、提现主链路
- ✅ `cmd/close-expired-orders` 已同步触发过期订单关闭告警
- ✅ 本地 HTTP 接收器自测：关闭 1 笔过期订单后收到 `expired_orders_closed`
- ✅ 自测 payload 包含 `closed_count=1` 和 `sample_order_nos=["ALERT_EXPIRED_1"]`

### 3.22 代码 Bug 修复与优化

> 系统扫描后批量修复，已通过临时数据库 + 并发自测验证。

- ✅ `billing.CreateOrReuseOrder` 新增校验：拒绝为非 `published` 短剧或非 `ready` 剧集下单，对应错误 `ErrDramaNotAvailable` / `ErrEpisodeNotReady`。
  - 自测：草稿短剧下单 → 40001 "短剧未上架或已下架"
  - 自测：uploading 剧集下单 → 40001 "剧集尚未就绪"
  - 自测：published + ready 健康路径 → code=0
- ✅ `sms.Verify` 改成事务 + `SELECT FOR UPDATE`，杜绝两个并发 Verify 都用掉同一条验证码。
  - 自测：同一验证码并发 5 次登录，仅 1 次成功，其余 4 次 40001
- ✅ `internal/ratelimit` 增加 `cleanup` 节流：每 1 分钟最多清理一次，避免高并发下每个请求都做 O(n) 扫描。
- ✅ `appUpdateMe` 昵称改用 `utf8.RuneCountInString`，并先 `TrimSpace`，限制 1~32 个字符（按字符数而非字节）。
  - 自测：10 个中文字符的昵称更新成功
  - 自测：33 个中文字符的昵称返回 40001 "1~32 个字符之间"
- ✅ `handler.auditMiddleware` 不再硬编码 `actor_subject="admin"`，改成读 `middleware.CurrentSubject(c)`，并新增 `truncateRune` 按 UTF-8 安全截断 message / user_agent，避免把中文切成半个字节。
- ✅ `corsMiddleware` 在配置了非通配白名单时，无论 origin 是否命中都输出 `Vary: Origin`，避免反向代理 / CDN 把非白名单 origin 的响应错误缓存给白名单 origin。
  - 自测：未命中 origin 不返回 `Access-Control-Allow-Origin`，但仍返回 `Vary: Origin`
  - 自测：命中白名单 origin 同时返回 `Access-Control-Allow-Origin` 与 `Vary: Origin`

### 3.23 第二轮代码 Bug 修复与优化

> 重点检查财务、支付回调、APP 行为入口和后台配置入口，已通过临时数据库自测。

- ✅ 支付回调新增金额校验：`billing.MarkOrderPaid` 会比较回调 `amount_cents` 与订单金额，不一致返回 `ErrOrderAmountMismatch`。
  - 自测：600 分订单用 1 分回调 → 40901 "支付金额与订单金额不一致"
- ✅ APP 点赞 / 收藏 / 分享增加短剧上架校验，草稿 / 下架短剧不允许互动。
  - 自测：草稿短剧点赞 / 分享 → 40401 "短剧未上架"
- ✅ APP 观看历史增加短剧上架与剧集 ready 校验，避免记录草稿短剧或上传中剧集。
  - 自测：草稿短剧写历史 → 40401
  - 自测：uploading 剧集写历史 → 40001 "剧集尚未就绪"
- ✅ 后台创建 / 更新短剧时校验 `category_id` / `creator_id` 是否存在，避免产生悬挂引用。
  - 自测：不存在分类创建短剧 → 40401 "分类不存在"
  - 自测：不存在创作者更新短剧 → 40401 "创作者不存在"
- ✅ 提现驳回 / 标记打款增加冻结余额保护，脏数据下不会把 `frozen_cents` 扣成负数。
  - 自测：提现金额 1000、创作者 frozen=100 时 `mark-paid` → 40901，且 frozen 仍为 100
- ✅ 全量 `go test ./...` 通过，linter 无报错

### 3.24 第三轮代码 Bug 修复与优化

> 重点检查创作者资料更新、后台封禁、对账命令边界，已通过临时数据库自测。

- ✅ `creatorUpdateProfile` 拒绝已 `verified` 创作者把 `bank_name` 清空，避免出现 `bank_name=""` 但 `bank_card_last4` 仍在的不一致状态；允许改为其他非空值。
  - 自测：verified 创作者传 `bank_name=""` → 40001 "不能清空 bank_name"
  - 自测：verified 创作者传 `bank_name="新银行"` → code=0，新值生效
- ✅ `adminBanCreator` 增加存在性校验：封禁不存在的 creator_id 时返回 40401，避免静默成功。
  - 自测：`POST /v1/admin/creators/999/ban` → 40401 "创作者不存在"
  - 自测：封禁存在的创作者仍返回 code=0 + `status=banned`
- ✅ 清理 `creator_data.go` 中未被使用的 `errUniqueViolation` sentinel 和 `errors` 包导入；统一只通过 `isUniqueViolation` 判定重复键。
- ✅ 删除 `internal/reconcile/reconcile.go` 中未使用的 `withdrawalSumRow` 结构。
- ✅ 临时空库下 `go run ./cmd/reconcile` 仍输出 `status: OK`（paid_orders=0 / withdrawals=0），不会因空集合 panic。

### 3.26 第五轮：端到端回归 + 安全 + 容错（2026-05-21）

> 重点：跨用户隔离、JWT 攻击面、Banned 账号、Rate limit、Alert webhook、Redis 容错、PII 加解密往返、并发下单、内容控制、SMS scene 隔离、分页正确性。

- ✅ **跨用户隔离全绿**：user3 GET user2 订单 → 40301；用别人 order_no unlock → 拒绝（见 3.26 修复 #3）；creator6 看 creator1 的 drama stats → 40301
- ✅ **JWT 攻击面**：过期 / 错 secret / 篡改 payload / `alg=none` / 垃圾 token 全部 40101
- ✅ **Banned 账号即时失效**：creator ban 后既有 token 全 40301，重新登录也拒绝；user ban 同
- ✅ **Operation logs**：admin 写操作落 `operation_logs` 表（actor_subject + method + path + status_code）
- ✅ **Rate limit 实测**：开 `RATE_LIMIT_ENABLED=true RPS=2 BURST=3`，前 3 个 200、之后 429 «请求过于频繁»、1.5s 后令牌补回
- ✅ **Alert webhook 实测**：本地起 listener，金额不一致 / 订单不存在的 webhook 触发 alert，payload 含 level/type/message/fields/timestamp
- ✅ **Redis 挂掉容错**：纯 DB 接口继续 200；下单 fail-closed 50001 «幂等校验失败»；恢复后立即可用
- ✅ **PII 加密往返**：DB 是 AES-GCM 密文（base64），creator 自己接口不含字段，admin 只看 `***5678` 脱敏
- ✅ **并发下单同 episode**：10 个并发 POST + 不同 IK 命中同一个 order_no，DB 只 1 行（`pg_advisory_xact_lock` 生效）
- ✅ **内容控制**：drama offline / draft 对 app 隐藏；episode `status=not_ready` 在 list 被过滤，play/order 都拦
- ✅ **SMS scene 隔离**：login 验证码无法登 creator；creator_login 码反之亦然
- ✅ **分页**：has_more / total / 越界 page 全部正确，重复拉同一 page 结果稳定
- ✅ **HTTP 边界**：坏 JSON / 缺字段 / 5MB body 都安全降级
- ✅ **CORS**：白名单 origin 返完整 ACAO 头；陌生 origin 不返
- ✅ **go build ./...** + **go vet ./...** 全过；**单元测试为零**（已记入 §5.2）

#### 3.26 修复

- 🔴 **过期订单仍能被支付**（旧）：`MarkOrderPaid` 只检 `paid/closed/refunded`，对 pending 但 `expired_at < now` 的订单不拦，与 `close-expired-orders` cron 存在竞态窗口
  - 修：`internal/billing/billing.go` 加 `ErrOrderExpired`，pending 订单若 `expired_at < paidAt` 直接返回；webhook 在 `internal/handler/webhooks.go` 加分支，ack 200 + 异步 alert，叫 ops 人工跟进退款
- 🟡 **负数 `price_cents` / `free_episodes` 被静默归零**：admin create/update drama 接受负数请求且返 200，实际写入 0
  - 修：`internal/handler/admin_drama.go` 抽 `validateDramaNumericFields` 在 create/update 入口拒绝，返 40001
- 🔴 **Admin 登录无暴力防护**：10 次错密码无锁定，可无限尝试
  - 修：`internal/model/model.go` Admin 加 `FailedLoginAttempts` + `LockedUntil`（gorm AutoMigrate 自动建列）；`internal/handler/admin.go` login 入口校验锁，5 次错锁 15min，成功 reset
- 🟡 **`/ready` 不探 Redis**：Redis 挂时仍返 ready，但下单接口已经 50001 — LB 不会摘节点
  - 修：`internal/handler/server.go` Server struct 暴露 redis；`internal/handler/common.go` ready 加 redis ping，失败返 503
- 🟡 **跨用户 unlock 文案误导**：用别人 order_no → 报「订单与剧集不匹配」(42002)，实际原因是 ownership
  - 修：`internal/billing/billing.go` 加 `ErrOrderNotOwned`，ownership 校验先于 episode mismatch；`internal/handler/app_pay.go` 映射 → 40301 «订单不属于当前用户»

### 3.27 支付 mock 增强（2026-05-21）

> 目标：前端联调不被微信 / 支付宝真实接入阻塞。

- 新增 `POST /v1/dev/orders/:order_no/pay`（`internal/handler/dev.go`）：复用 `billing.MarkOrderPaid`，前端拿到 `order_no` 调一下就完成「支付成功」，自动解锁 + 分账；仅在 `PAYMENT_DEV_MODE=true` 时挂载（路由在 `server.go` 条件加载）
- `.env` / `.env.example` 显式列 `PAYMENT_DEV_MODE` + wechat/alipay 占位 + 说明，避免运维不知道当前在 mock 模式
- 上线切换路径：填齐微信 / 支付宝密钥 → `PAYMENT_DEV_MODE=false` → 重启 → dev 端点自动消失、provider 自动替换；任一渠道配置不全自动 fallback 到 `UnavailableProvider`（拒绝下单，不会静默走 mock）

### 3.28 测试服务器部署（2026-05-21）

> 目标：先把 MVP 后端部署到腾讯云 Ubuntu 22.04 服务器，供前端和接口联调用；域名 / HTTPS 后续接入现有 80/443 入口。

- ✅ 服务器：`43.132.168.84`，SSH 端口 `8848`，后端监听 `:18080`
- ✅ 已安装并启用 PostgreSQL 14、Redis 6
- ✅ 已创建数据库 `ai_drama`、数据库用户 `ai_drama`，应用启动时 AutoMigrate 成功创建 16 张表
- ✅ 已部署 systemd 服务 `drama-backend.service`
  - 工作目录：`/opt/drama-backend`
  - 配置文件：`/opt/drama-backend/.env`（root/drama 可读，包含随机 DB 密码、JWT_SECRET、DATA_ENCRYPTION_KEY、初始 admin 密码）
  - API 二进制：`/opt/drama-backend/drama-api`
- ✅ 已部署运维二进制：`drama-check-config`、`drama-reconcile`、`drama-close-expired-orders`
- ✅ 自测：
  - `GET http://127.0.0.1:18080/health` → code=0
  - `GET http://127.0.0.1:18080/ready` → database=ok / redis=ok
  - `GET http://43.132.168.84:18080/health` → code=0
  - `GET http://43.132.168.84:18080/ready` → database=ok / redis=ok
  - `./drama-reconcile` → status=OK
  - `./drama-close-expired-orders` → closed_expired_orders=0
- ⚠️ 当前部署为联调 / 测试模式：
  - `SMS_DEV_MODE=true`
  - `PAYMENT_DEV_MODE=true`
  - `CORS_ALLOWED_ORIGINS=*`
  - 真实上线前需要改为正式短信 / 支付配置，并接入域名 HTTPS

### 3.29 线上完整主链路自测（2026-05-21）

> 目标：验证 `43.132.168.84:18080` 测试环境的 MVP 主链路可跑通。

- ✅ 健康 / 就绪：
  - `GET /health` → code=0
  - `GET /ready` → `database=ok` / `redis=ok`
- ✅ 管理端：
  - `POST /v1/admin/auth/login` 登录成功
  - `GET /v1/admin/me` 查询成功
  - 创建分类、创建短剧、创建 2 集剧集、上架短剧成功
- ✅ 创作者端：
  - `POST /v1/common/sms/send` + `POST /v1/creator/auth/login` 登录成功
  - `PUT /v1/creator/me/profile` 填姓名 / 身份证 / 银行卡后自动 `verified`
  - `GET /v1/creator/dashboard` 收益正确增加
  - `GET /v1/creator/income`、`GET /v1/creator/dramas` 查询成功
  - `POST /v1/creator/withdrawals` 提现申请成功
- ✅ APP 端：
  - `POST /v1/common/sms/send` + `POST /v1/app/auth/login` 登录成功
  - 首页、短剧详情、剧集列表可查询
  - 免费集可播放
  - 付费集未解锁时返回 `42001`
  - `POST /v1/app/orders` 下单成功
  - `POST /v1/dev/orders/:order_no/pay` mock 支付成功
  - `POST /v1/app/episodes/:id/unlock` 解锁成功
  - 解锁后付费集可播放
  - `POST /v1/app/play-history` 观看历史写入成功
- ✅ 管理查询：
  - `GET /v1/admin/dashboard`
  - `GET /v1/admin/orders`
  - `GET /v1/admin/withdrawals`
- ✅ 运维脚本：
  - `/opt/drama-backend/drama-reconcile` → `status: OK`
- 测试数据：
  - `creator_id=2`
  - `drama_id=2`
  - 免费集 `episode_id=3`
  - 付费集 `episode_id=4`
  - `order_no=20260521175937911621823`
  - `withdrawal_no=WD2026052117594328554`
- 备注：第一次测试使用 `price_cents=199`，按 50% 分账仅 99 分，低于 `MIN_WITHDRAWAL_CENTS=1000`，提现按预期返回 40001；第二次使用 `price_cents=2000` 后提现链路通过。

### 3.30 创作者认证状态误触发修复（2026-05-22）

> 问题：前端反馈“只上传身份证后，测试账号从未认证变为已认证”。

- 原因：旧逻辑会按“资料最终是否齐全”判断自动认证；如果账号之前已保存姓名 / 银行名 / 银行卡，再单独补身份证，就会触发 `verify_status=verified`。
- 修复：`creatorUpdateProfile` 改为只有**同一次请求完整提交** `name`、`id_card_no`、`bank_name`、`bank_card_no` 时，才自动置为 `verified`；分步上传身份证 / 银行卡只保存资料，不自动认证。
- OpenAPI 已同步说明该测试环境认证规则。
- 已部署到测试服务器 `43.132.168.84:18080`。
- 自测：
  - 分步先提交姓名 + 银行，再单独提交身份证 → `verify_status=pending`
  - 同一次完整提交姓名 + 身份证 + 银行名 + 银行卡 → `verify_status=verified`

### 3.31 SMS dev_code 模式 + Apifox 文档同步（2026-05-25）

> 目标：腾讯短信测试号 `19703092478` 日发量被 `LimitExceeded.PhoneNumberDailyLimit` 顶死，前端联调拿不到验证码。MVP 期间统一切到 dev 模式，验证码在响应里回显。

- 服务器 `/opt/drama-backend/.env`：`SMS_DEV_MODE=false → true`；本地 `.env` / `.env.example` 同步更新注释。
- 服务启动日志 `[sms] provider=dev dev_mode=true` 表示已切换；腾讯路径整体绕开。
- `短剧MVP-OpenAPI.yaml` `/common/sms/send` 响应 schema 加回 `data.dev_code` 字段，description 改回 dev 模式说明。
- `MVP后端设计-API接口.md` 响应示例同步追加 `dev_code` 字段说明。
- SMS 业务范围核定：当前只有 APP 登录（`scene=login`）+ 创作者登录（`scene=creator_login`）。

### 3.32 APP 首页视频流结构调整（2026-05-25）

> 目标：APP 首页改成类似抖音 / 红果短剧的视频流形式，前端直接渲染推荐短剧列表。

- ✅ `GET /v1/app/home` 不再返回 `categories` / `hot_dramas`
- ✅ 响应只保留 `recommend_dramas`
- ✅ 每个推荐短剧新增 `first_episode`
  - `id`
  - `episode_no`
  - `title`
  - `play_url`
  - `duration_seconds`
- ✅ `first_episode` 默认取该短剧最靠前的 `ready` 剧集（通常是第 1 集）
- ✅ OpenAPI 已同步新增 `HomeFeedDramaItem`
- ✅ 已部署到测试服务器 `43.132.168.84:18080`
- ✅ 自测：`GET /v1/app/home` 返回 top-level `data` 仅含 `recommend_dramas`；首条数据包含 `first_episode.play_url`
- 联调注意：60 秒重发频控 + 错码 5 次锁 15 分钟 + 单 IP RPS=0.2 burst=3 在 dev 模式下**全部仍然生效**，不会因为 dev 就放开。
- 上线前回切：把服务器 `.env` 改回 `SMS_DEV_MODE=false`，腾讯模板 + 签名补齐，重启即可；`dev_code` 字段就不再出现在响应里。

### 3.32 mock seed + COS 视频迁移（2026-05-25）

> 目标：测试服务器 DB 只有几条自测残留，前端联调没像样的剧 / 集 / 订单数据；同时把外网 mp4 搬上腾讯云 COS，避免国内网络拉外站 mp4 卡顿。

- 触发 `POST /v1/dev/seed`，seeder 写入：
  - 6 部 mock 短剧（5 部 published + 1 部 draft `重回 2008`），分别 8–20 集
  - 84 条分类（红果 4 维：theme=31 / setting=42 / background=11 / audience=2），42 条 drama_tag
  - 3 个 mock 创作者（`13800000001 顾导演` 等）、3 个 mock 用户、若干订单 / 解锁 / 提现 / 合同
- mock 短剧封面用 `picsum.photos/seed/dramaN/600/900` 占位图；6 张 HEAD 全部 200。
- 真实视频源：8 支公开 mp4（`media.w3.org` / `vjs.zencdn.net` / `test-videos.co.uk` / `blender.org`），按 `(drama_id*31 + ep_no) % 8` 确定性分给每集。
- 视频迁到 COS：
  - 服务器 `wget` 下 8 支共 57 MB 到 `/tmp/mock-videos/`
  - Python 复刻 `internal/cos/cos.go` 的 HMAC-SHA1 + `x-cos-acl=public-read` 签名 PUT 上 `duanju-1318683367.cos.ap-guangzhou.myqcloud.com/videos/mock/`
  - UPDATE 73 条 mock episodes 的 `video_url`，从外网域名替换为 COS 公网 URL；剩 4 条 `example.com/*` 是早期自测残留没动
- 验证：抽 3 集打 `/v1/app/episodes/:id/play`，返回 COS URL，HEAD 200 + `Content-Type: video/mp4`。
- 同时确认付费集 API 鉴权：免费集 ✅、未解锁付费集 `42001` ✅、匿名 `40101` ✅；但**VOD URL 本身没做防盗链**，URL 泄露即可白嫖（见 3.34 P0）。

### 3.33 VOD 完整链路真打通（2026-05-25）

> 目标：把"VOD 上传 + 转码 + webhook → DB 自动更新 episode"这条链路用真实腾讯 API 跑一遍。

- 服务器装 `tencentcloud-sdk-python` + `cos-python-sdk-v5`（VOD 没现成的 PyPI 上传包，用 `ApplyUpload` + 临时密钥 COS PUT + `CommitUpload` 三步手拼）。
- 4 次真实上传成功，4 个 FileId 都拿到 + MediaUrl 都拿到：
  - `5145403727999033095` (oceans.mp4) — 绑给 ep_id=8
  - `5145403727898572930` (bunny-trailer.mp4)
  - `5145403727898713341` (sintel-trailer.mp4)
  - `5145403728002149553` (movie-300.mp4) — 绑给 ep_id=9
- VOD 控制台「事件通知」配 URL `http://43.132.168.84:18080/v1/webhooks/vod`，通知方式选「普通回调」；勾「视频上传完成 + 任务流状态变更」事件。**腾讯控制台没有「节点回调（含签名）」选项**，只有普通回调和可靠回调，普通回调不带 Sign。
- 服务端验签逻辑对应：`internal/vod/vod.go` 设计上 `VOD_CALLBACK_KEY` 非空才校验 Sign，所以**普通回调时 `VOD_CALLBACK_KEY` 必须为空**才能放行。服务器 `.env` 保持 `VOD_CALLBACK_KEY=`。
- 实际效果：上传完成后 ~5 秒，腾讯主动 POST `/v1/webhooks/vod`（payload 约 2.3 KB，比手工 mock body 大 10 倍）。
- **代码 bug 发现 + 修复**：`internal/handler/uploads.go::vodCallbackEnvelope` 原本期望 `FileUploadEvent.MediaUrl`，但腾讯真实 payload 是 `FileUploadEvent.MediaBasicInfo.MediaUrl`。增加 `MediaBasicInfo` 嵌套结构，`handleVODFileUpload` 优先取 `MediaBasicInfo.MediaUrl`，老路径作 fallback；`url_set` 日志字段改用解析后的 `mediaURL` 变量。
- 闭环验证：重置 ep_id=9 → 上传 → 绑 FileId → 腾讯回调 → ep_id=9 自动变成 `status=ready`、`duration=300`、`video_url=https://1318683367.vod-qcloud.com/.../5uY8…mp4`，**没碰任何 SQL**。
- 部署：linux/amd64 cross-compile + scp 到 `/opt/drama-backend/drama-api`，备份原二进制为 `drama-api.bak.<ts>`，systemctl restart。
- 真实用户流程对照：
  - **播放**（APP 用户 / 创作者预览 / admin 审片）：跟我们测的一模一样，前端 GET `/v1/app/episodes/:id/play` 拿 `play_url` 喂 `<video>`。
  - **上传**（运营 / admin）：浏览器端用 `vod-js-sdk-v6` SDK 直传，签名走 `POST /v1/admin/uploads/vod-sign`；底层调的还是 ApplyUpload / COS / CommitUpload，业务等价于我们今天用 Python SDK 跑的，只是 SDK 形态不同。
  - **创作者目前不能自助上传**（MVP 设计：创作者只看分账 / 提现，admin 负责上传发布）。要做 UGC 得在 `creatorAuth` 路由组复制一份 admin 那套上传接口。

### 3.34 VOD / 视频链路上线前 P0 遗留（2026-05-25）

> 测试链路全部跑通，但生产前以下四点必补。当前都不影响联调，记下来排期。
>
> ⚠️ 其中第 3 / 4 项的**代码侧**已在 3.35 落地，env flag 默认关；控制台配齐 + 翻开关后即生效。

1. **可靠回调模式适配**：控制台两种回调里我们用了「普通回调」（push），但腾讯推荐生产用「可靠回调」（事件入消息队列，业务方拉）。现在代码不支持拉取，要切到可靠回调需要新增 puller。普通回调缺点：弱网 / 服务端宕机时事件直接丢，没重试。
2. **HTTPS**：当前 `http://43.132.168.84:18080` 是裸 HTTP。腾讯云回调对 HTTPS 没强制（已实测能通），但 iOS App Store 审核 + 自身安全考量必须接入。等 Nginx + 证书。
3. **`VOD_PROCEDURE_NAME=` 空 → 不自动转码**：代码侧已在 3.35 修补完整（`handleVODProcedureStateChanged` 现在会从 `MediaProcessResultSet.TranscodeTask.Output.Url` 拿转码后 URL 回填 video_url）；激活只差控制台建模板 + `.env::VOD_PROCEDURE_NAME` 填名字 + 重启。
4. **VOD URL 防盗链**：代码侧已在 3.35 落地 `SignPlayURL` + `appPlayEpisode` 集成；激活只差控制台开 Key 防盗链 + `.env::VOD_PLAY_SIGN_ENABLED=true` + `VOD_PLAY_SIGN_KEY=<32位>` + 重启。

### 3.35 落 P0 #3/#4 + P1 #6 代码（env flag 默认关）（2026-05-25）

> 三件事都加 env flag、默认 OFF，部署到服务器也保持 OFF，联调收尾后翻开关即可激活。

**P0 #3 — VOD 转码输出回填**：
- `vodCallbackEnvelope.ProcedureStateChangeEvent` 补全 `ErrCode` / `Message` / `MediaProcessResultSet.TranscodeTask.Output.{Url,Container,Bitrate,Width,Height,Duration}` 字段。
- 新增 `pickTranscodeOutput`：优先选 `container=hls` 的转码输出 → 否则任意 mp4 → 都没有就只置 status=ready。**避免运营配了转码模板但 video_url 还停在原始 mp4**。
- `handleVODProcedureStateChanged` 在 FINISH 时 UPDATE 同时写 `status=ready` + 新 `video_url`；ERROR 时打日志带 `ErrCode` + `Message`，方便排查转码失败具体原因。
- 激活步骤：
  1. 腾讯 VOD 控制台 → 视频处理 → 任务流模板 → 新建（例如 `LongVideoPreset`，配 HLS 自适应输出）
  2. 服务器 `.env`：`VOD_PROCEDURE_NAME=LongVideoPreset`
  3. `systemctl restart drama-backend.service`
  4. 此后 `NewFileUpload` 不再立刻 ready，要等 `ProcedureStateChanged FINISH` 才 ready，video_url 自动切到 HLS m3u8。

**P0 #4 — VOD URL Key 防盗链签名**：
- 新增 `internal/vod/vod.go::SignPlayURL`：按腾讯官方算法 `sign = md5(KEY + Dir + t + exper + rlimit + us)` 拼一次性 token URL；本地手算对账 32 hex 一致。
- `internal/handler/app_play.go::appPlayEpisode`：`vod.PlaySignConfigured()` 为 true 时把 `play_url` 走签名包一层，`expire_seconds` 同步改为签名 TTL；签名失败退回裸链 + error log（不阻塞播放）。
- 新增 5 项 config：`VOD_PLAY_SIGN_ENABLED` / `VOD_PLAY_SIGN_KEY` / `VOD_PLAY_SIGN_EXPIRE_SECONDS` / `VOD_PLAY_SIGN_EXPER` / `VOD_PLAY_SIGN_RLIMIT`，`.env.example` 同步补注释。
- **默认关**：当前 `.env.example` 和服务器 `.env` 都是 `VOD_PLAY_SIGN_ENABLED=false`，行为与昨天一致；自测 ep_id=8/9/20 三集 `/play` 仍返回裸链 ✅
- 激活步骤：
  1. 腾讯 VOD 控制台 → 分发播放 → Key 防盗链「启用」→ 拿到 32 位 KEY
  2. 服务器 `.env`：`VOD_PLAY_SIGN_ENABLED=true` + `VOD_PLAY_SIGN_KEY=<32位KEY>`
  3. `systemctl restart`
  4. 此后 `/v1/app/episodes/:id/play` 返回的 `play_url` 会带 `?t=&exper=&rlimit=&us=&sign=` 五参数，TTL = `VOD_PLAY_SIGN_EXPIRE_SECONDS`。

**P1 #6 — COS Referer 白名单工具**：
- 新增 `cmd/setup-cos-referer/main.go`：从 `.env::COS_REFERER_WHITELIST`（逗号分隔，支持通配符）读取白名单，调腾讯 COS `PutBucketReferer` 写桶级 Referer 防盗链规则。
- 加 `--disable` 关闭、`--empty-allow` 允许无 Referer 请求（联调期专用，curl/Apifox 默认不带 Referer）。
- **不在 drama-api 主进程里执行**：是独立二进制 `drama-setup-cos-referer`，你/运维想生效就 SSH 进服务器跑一次；桶级配置立即生效，无需重启 drama-api。
- 二进制已 cross-compile 上传到 `/opt/drama-backend/drama-setup-cos-referer`，权限 +x。
- 激活步骤：
  1. 服务器 `.env` 追加：`COS_REFERER_WHITELIST=*.shoplazza.com,localhost,apifox.com,<前端域名>`
  2. 跑一次：`/opt/drama-backend/drama-setup-cos-referer --empty-allow`（联调期）或 不带参数（生产）
  3. 回滚：`/opt/drama-backend/drama-setup-cos-referer --disable`

**部署**：linux/amd64 cross-compile + scp 到 `/opt/drama-backend/`，老 `drama-api` 备份到 `drama-api.bak.20260525-165028`，systemctl restart 后服务 active 无错误。

**新依赖**：`github.com/tencentyun/cos-go-sdk-v5 v0.7.73` + 4 个二级依赖（mxj / mapstructure / go-querystring / go-httpheader）。

### 3.25 第四轮代码 Bug 修复与优化

> 重点：支付回调 HTTP 语义、金额/渠道校验、账号封禁即时生效、SMS 防刷、并发下单、运营校验补全。

- ✅ **支付 webhook 返回正确 HTTP 状态**：验签失败 → HTTP 401；订单不存在 / 状态非法 / 金额不一致 / 渠道不一致 → HTTP 500（触发平台重试 + webhook 告警）。仅幂等成功或非 paid 事件返回 HTTP 200 ack。
  - 自测：closed 订单回调 → HTTP 500 + `订单状态非法，无法标记已支付`
  - 自测：金额不一致 → HTTP 500；微信订单走支付宝回调 → HTTP 500
- ✅ **`MarkOrderPaid` 强制金额完全一致**：`amount_cents <= 0` 或缺失不再跳过校验；新增 `payment_method` 与订单一致性校验（`ErrPaymentMethodMismatch`）。
- ✅ **生产环境支付配置缺失不再退回无验签 DevProvider**：改为 `UnavailableProvider`，预支付与回调均拒绝。
- ✅ **封禁/禁用账号 JWT 即时失效**：`requireActiveApp/Creator/Admin` 中间件在每次鉴权请求查库校验 `status=active`。
  - 自测：封禁创作者后 `GET /v1/creator/me` → 40301
- ✅ **SMS 安全增强**：
  - 验证码连续错误 ≥5 次锁定 15 分钟（`SMS_MAX_VERIFY_ATTEMPTS` / `SMS_VERIFY_LOCK_SECONDS`）
  - 发送接口 IP 级限流（`SMS_SEND_IP_RPS` / `SMS_SEND_IP_BURST`）
  - 发送频控改为事务 + 行锁，避免并发产生多条有效验证码
  - 自测：连续 5 次错误验证码 → 42901 `验证码尝试次数过多`
- ✅ **并发下单保护**：`CreateOrReuseOrder` 使用 `pg_advisory_xact_lock(user_id, episode_id)` + DB 部分唯一索引 `idx_orders_user_episode_pending`。
- ✅ **商品校验**：下单时 `product_id` 必须存在、`status=active`、`type=episode_unlock`。
- ✅ **运营校验补全**：
  - 上架：存在付费集时 `price_cents > 0`
  - 剧集 ready 必须提供 `video_url` 或 `vod_file_id`
  - 创建合同：若绑定 drama，须 `drama.creator_id == creator_id`
  - 观看历史：付费未解锁集不可写入
- ✅ **中间件优化**：webhook 路径排除全局限流；CORS `*` 白名单不再设置 `Allow-Credentials`（避免浏览器规范冲突）。
- ✅ **对账增强**：新增同一 `user_id + episode_id` 多笔 paid 订单检测（`duplicate_paid_orders`）。
- ✅ 全量 `go build ./...` + `go test ./...` 通过。

---

## 四、关键设计与原则落地点

### 4.1 钱相关（执行文档第七节）

- `creator_amount = order.amount_cents * CREATOR_SHARE_RATE`，**向下取整到分** —— `int64(float64(amount) * rate)` 自动截断
- 所有改余额的操作都在 `db.Transaction` + `clause.Locking{Strength: "UPDATE"}` 行锁内
- 回调先 SELECT FOR UPDATE 查订单，已 paid 则直接 return nil（幂等）
- 重复回调测试：第二次 webhook 不会改任何状态
- pending 默认 30 分钟（`ORDER_PENDING_TTL_SECONDS`），后台 ticker 每 60 秒扫一次过期 → closed；也可用 `go run ./cmd/close-expired-orders` 手动执行一次

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

- AutoMigrate 16 张表：users / sms_codes / admins / creators / categories / dramas / episodes / play_histories / user_actions / products / orders / episode_unlocks / contracts / withdrawals / creator_stats_daily / operation_logs
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

- ✅ Redis 强幂等：订单 / 提现已接入 `Idempotency-Key` 响应缓存和请求体校验
- ✅ 对账脚本：`go run ./cmd/reconcile`（执行文档 7.8，已完成基础账务一致性检查）
- ✅ Rate Limit：已完成可配置全局限流，超限返回 HTTP 429 + `42901`
- ✅ Webhook 告警：已接入过期订单关闭 / 后台任务异常 / 支付回调失败事件
- ✅ HTTPS / Nginx / 部署脚本：已完成 `docs/DEPLOYMENT.md` 示例文档；真实服务器配置待联调 / 上线时执行
- ❌ 退款（MVP 范围外，文档 7.2 明确「refunded 仅运营手工标记，不开放 API」）
- ❌ 评论接口（API 文档 4.14 / 4.15，按"可砍"清单暂未做）
- ❌ 图片上传签名 `POST /v1/common/uploads/image-sign`（依赖 COS / 云点播账号）
- ✅ 后台操作日志：已完成 `operation_logs`，消息中心、VIP、Banner、推荐算法等仍按 MVP 文档第十四节不做

### 5.3 安全 & 部署

- `DATA_ENCRYPTION_KEY` 当前用 `openssl rand -base64 32` 本地生成；上线必须放密钥管理服务，并保证多实例共用一份
- `.env` 已被 `.gitignore` 拦住，但请确认部署环境用环境变量注入而不是文件分发
- ✅ 上线前配置检查：`go run ./cmd/check-config --prod`
- ✅ HTTPS / 反向代理已补部署文档；真实服务器配置留给联调 / 上线阶段执行

### 5.4 待补充功能清单（2026-05-21 复盘）

按优先级排，每项给清晰落点便于后续认领。

#### A. 代码骨架在，内部是 stub（`PAYMENT_DEV_MODE=false` 或对应渠道被选用就 down）

| 模块 | 文件 | 当前行为 | 接入提示 |
|---|---|---|---|
| 🔴 微信支付 V3 | `internal/payment/wechat_provider.go` | `Prepay` / `VerifyAndParse` 均返 `ErrProviderUnavailable` | 文件内注释已写：用 `wechatpay-go` SDK + 商户证书 + APIv3 key |
| 🔴 支付宝 OpenAPI | `internal/payment/alipay_provider.go` | 同上 | 文件内注释：`smartwalle/alipay/v3` + RSA2 |
| 🟡 腾讯电子签 | `internal/handler/contract.go:212 adminEsignContract` | 返 `60001 «电子签 SDK 尚未接入»` | 用 `tencentcloud-sdk-go/ess`；签完走 esign webhook 回写 |

#### B. OpenAPI 定义但完全未实现的路由

| 路由 | 用途 | 影响 |
|---|---|---|
| 🔴 `POST /v1/common/uploads/image-sign` | COS/OSS 图片上传临时签名 | admin 后台传不了封面图 |
| 🔴 `POST /v1/admin/uploads/vod-sign` | 云点播视频上传临时签名 | admin 后台传不了剧集视频（现在 `video_url` 只能手填） |
| 🔴 `POST /v1/webhooks/vod` | 云点播转码完成回调 | 转码完无法自动 mark episode `ready`（要 SQL 手改） |
| 🟡 `POST /v1/webhooks/esign` | 电子签签署完成回调 | 与 A 表第 3 项配套 |
| ⚪ `GET /v1/app/dramas/:id/comments` | 评论列表 | 整块评论功能缺，db 也无 `comments` 表（OpenAPI 已注明 MVP 范围外） |

#### C. 业务流程小空缺（不影响主链路）

- ⚪ **创作者实名审核中间态**：`PUT /v1/creator/me/profile` 写入即 `verify_status=verified`，无 admin review 流程（接入实人认证后必改）
- ⚪ **剧集审核**：drama 只有 `draft → published`，无 admin review 中间态
- ⚪ **退款 / 取消订单**：无 refund API（OpenAPI 明确 MVP 不做）
- ⚪ **数据导出**：无 export 接口

#### D. 代码残留

- ⚪ `internal/handler/app_pay.go:49` 注释 «TODO: 上 Redis 后做强幂等» 已过时——`idempotencyMiddleware` 早已接 Redis，删一行即可

#### 推荐落地顺序

1. **B 表前 3 项（image-sign + vod-sign + vod webhook）**——运营进得来的前置，不接入连内容都上传不了
2. **A 表前 2 项（微信 + 支付宝）**——上线必修，否则只能 dev mode
3. **A 表第 3 项 + B 表 esign webhook**——OpenAPI 已标 P1，可线下签合同先
4. **C 表创作者实名审核**——合规需要
5. C / D 其余项随后续迭代

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
REDIS_ADDR='127.0.0.1:6379' \
SMS_DEV_MODE=true \
PAYMENT_DEV_MODE=true \
DATA_ENCRYPTION_KEY="$(openssl rand -base64 32)" \
CREATOR_SHARE_RATE=0.5 \
MIN_WITHDRAWAL_CENTS=1000 \
APP_SHUTDOWN_TIMEOUT_SECONDS=10 \
go run ./cmd/api
```

---

## 七、下一阶段建议

按执行文档第五节进入第 4 周「联调、测试、上线准备」：

1. 把上面 5.1 阻塞项一项一项推动（SMS 模板审核、商户证书申请、电子签开通）。每一项审核 / 申请到达后只改一个 Provider 文件的 stub。
2. 真实联调时把 `SMS_DEV_MODE=false` 和 `PAYMENT_DEV_MODE=false`，缺哪一项 provider 会自动 fallback 到 dev + warning，方便定位。
3. 起一台测试 Postgres + HTTPS + Nginx 反代，把 `/health` 作为存活探针、`/ready` 作为就绪探针，并验证发布 / 重启时 `SIGTERM` 能让 API 服务和后台任务都优雅退出。
4. 联调前按前端测试域名配置 `CORS_ALLOWED_ORIGINS`，不要在生产环境使用 `*`。
5. 用执行文档第十二节「验收主链路」14 条人工跑一遍，与本文件第三节对照。
6. 联调 / 上线前跑 `go run ./cmd/reconcile`；如输出 `status: FAILED`，先处理异常账目再继续发布。
7. 部署前跑 `go run ./cmd/check-config --prod`；如输出 `status: FAILED`，先补齐环境变量再启动服务。
8. 配置 `ALERT_ENABLED=true` 和 `ALERT_WEBHOOK_URL`，确认线上能收到 `expired_orders_closed` / `payment_webhook_failed` 告警。
