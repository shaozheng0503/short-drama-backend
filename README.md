# DramaBackend

短剧 APP MVP 后端 · Go 1.x · Gin · PostgreSQL · GORM · Redis · 腾讯云 SMS。

三端：APP（用户）、Creator（创作者）、Admin（管理中台）。

---

## 当前状态

| 模块 | 状态 |
|---|---|
| 三端登录 / JWT 鉴权 / 账号封禁即时失效 | ✅ |
| 短剧 / 剧集 / 分类 CRUD、上下架、搜索、分页 | ✅ |
| 播放、观看历史、点赞 / 收藏 / 分享 | ✅ |
| 商品 / 订单 / 解锁 / 分账 / 提现（含审核状态机） | ✅ |
| 合同列表 / 创建 / 状态变更 | ✅（电子签为 stub） |
| 腾讯云 SMS 真实发送 | ✅（模板已审核通过） |
| 微信 / 支付宝真实下单与回调 | ⚠️ stub，等商户证书 |
| 联调便利：`POST /v1/dev/orders/:order_no/pay` 一键模拟支付 | ✅ |
| 工程：Redis 幂等、Rate Limit、Webhook 告警、操作审计、PII AES-GCM | ✅ |
| CLI 工具：`check-config` / `close-expired-orders` / `reconcile` | ✅ |
| 云点播上传签名 / 回调、图片上传签名、评论 | ❌ 待补，见 `docs/DEV_TEST_LOG.md §5.4` |

> 完整待补清单与优先级见 `docs/DEV_TEST_LOG.md` 第五节。

---

## 快速开始

```bash
# 0. 前置：本机装 Postgres 16、Redis、Go 1.x
brew services start postgresql@16
brew services start redis
createdb ai_drama

# 1. 拷一份 .env（已被 .gitignore 拦住）
cp .env.example .env
# 编辑 .env：把 DATABASE_DSN、JWT_SECRET、DATA_ENCRYPTION_KEY 填上
#   DATA_ENCRYPTION_KEY 用 `openssl rand -base64 32` 生成；多实例必须共用同一份

# 2. 启动
set -a && source .env && set +a
go run ./cmd/api
```

首次启动 AutoMigrate 全表，并按 `ADMIN_INIT_USERNAME` / `ADMIN_INIT_PASSWORD` 创建管理员（默认 `admin/admin123`，**上线必改**）。

启动日志确认：
```
[redis] connected addr=127.0.0.1:6379 db=0
[sms] provider=tencent dev_mode=false      # 或 dev_mode=true
[dev] PAYMENT_DEV_MODE=true，已挂载 POST /v1/dev/orders/:order_no/pay
api server listening on :18080
```

---

## 接口总览（68 条）

完整 OpenAPI 定义见 `../短剧MVP-OpenAPI.yaml`，下面是分组速览：

| 分组 | 路径前缀 | 说明 |
|---|---|---|
| Health | `GET /health`、`GET /ready` | 存活 / 就绪（含 Redis ping） |
| Common | `POST /v1/common/sms/send` | 短信验证码（`scene=login/creator_login`，不含换绑） |
| Creator | `POST /v1/creator/bank-card/send-sms` | 换绑银行卡发验证码（需 JWT，scene=bank_card_change） |
| **APP**（22） | `/v1/app/*` | 登录、内容浏览、播放、互动、下单、解锁 |
| **Creator**（10） | `/v1/creator/*` | 登录、资料、数据看板、收益、提现、合同 |
| **Admin**（28） | `/v1/admin/*` | 内容/分类/创作者/订单/提现/合同的全套 CRUD + 审核 |
| Webhooks | `/v1/webhooks/wechat/pay`、`/alipay/pay` | 支付回调（dev 模式不验签） |
| **Dev only** | `POST /v1/dev/orders/:order_no/pay` | `PAYMENT_DEV_MODE=true` 时挂载，一键模拟支付成功 |

### 联调主链路（前端）

```
POST /v1/common/sms/send             {phone, scene:"login"}
POST /v1/app/auth/login              {phone, code}                       → app_token
GET  /v1/app/home / dramas / search                                       → 浏览
GET  /v1/app/episodes/:id/play       Bearer app_token                    → 锁/解
POST /v1/app/orders                  {product_id, episode_id, drama_id, payment_method}
                                     Header: Idempotency-Key             → order_no
POST /v1/dev/orders/{order_no}/pay                                       → 一键模拟支付成功
GET  /v1/app/orders/{order_no}                                            → status=paid
GET  /v1/app/episodes/:id/play                                            → 拿 play_url
```

---

## 错误码

| code | HTTP | 含义 |
|---:|:---:|---|
| 0 | 200 | 成功 |
| 40001 | 200 | 参数错误 / 验证码错误 |
| 40101 | 200 | 未登录 / token 无效或过期 |
| 40301 | 200 | 无权限 / 账号封禁 / 身份与接口不匹配 |
| 40401 | 200 | 资源不存在 |
| 40901 | 200 | 重复操作 / 频控冲突 |
| 42001 | 200 | 剧集未解锁 |
| 42002 | 200 | 订单与剧集不匹配 / 状态不可用 |
| 42901 | 429 | 命中 Rate Limit |
| 50001 | 500 | 服务端错误 / 第三方失败 |

响应统一格式：`{ "code": int, "message": string, "data": ... }`。

---

## 目录结构

```
cmd/
├── api/                       HTTP 服务入口（含 graceful shutdown + 后台 cron）
├── check-config/              上线前配置体检：go run ./cmd/check-config --prod
├── close-expired-orders/      过期订单关闭（也由 api 后台 ticker 触发）
└── reconcile/                 账务对账，发现不平直接 exit 1

internal/
├── config/                    环境变量装配
├── database/                  连接 + AutoMigrate + 初始管理员
├── model/                     全部 ORM 模型
├── response/                  统一响应结构
├── middleware/                JWT 鉴权（app/creator/admin）
├── secure/                    AES-GCM 加密（PII 字段）
├── redisclient/               Redis 连接
├── idempotency/               Idempotency-Key 中间件（请求体哈希 + 响应缓存）
├── ratelimit/                 全局 + 路径级令牌桶
├── alert/                     失败事件异步推 webhook
├── sms/                       DevProvider + TencentProvider
├── payment/                   DevProvider / WechatProvider(stub) / AlipayProvider(stub) / UnavailableProvider
├── billing/                   订单 / 解锁 / 分账（事务 + 行锁 + advisory lock）
├── reconcile/                 账务一致性校验逻辑
├── configcheck/               配置 lint
└── handler/                   所有 HTTP handler 按域拆文件 + audit / auth_status / dev / webhooks

docs/
├── DEPLOYMENT.md              Nginx + systemd + HTTPS 部署示例
└── DEV_TEST_LOG.md            自测记录、修复历史、待补功能清单（§5.4 必读）
```

---

## 运维命令

```bash
# 上线前体检
go run ./cmd/check-config --prod

# 关闭过期未支付订单（也由 api 后台 60s 触发）
go run ./cmd/close-expired-orders

# 账务对账，出现 status: FAILED 必须查
go run ./cmd/reconcile
```

---

## 日常发版（零停机）

API 启用了 `cloudflare/tableflip` + systemd PIDFile 模式，**发版用 `systemctl reload`，不要用 `restart`**。

```bash
# 本地交叉编译（必加 -ldflags="-s -w" 把体积砍到 ~35MB）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags="-s -w" -o /tmp/drama-api ./cmd/api

# scp 到临时路径（运行中的二进制 ETXTBSY，不能直接覆盖）
scp /tmp/drama-api root@<server>:/tmp/drama-api.new

# mv 替换 + 权限 + reload（不是 restart）
ssh root@<server> '
  mv -f /tmp/drama-api.new /opt/drama-backend/drama-api &&
  chown drama:drama /opt/drama-backend/drama-api &&
  chmod +x /opt/drama-backend/drama-api &&
  systemctl reload drama-backend
'
```

`reload` 走 SIGHUP → tableflip fork+exec 新进程继承 listener fd → 新进程 Ready 后老进程 graceful exit，MainPID 通过 PIDFile 切换，客户端零感知。

只有改 unit 文件 / `.env` / 进程卡死时才用 `systemctl restart`（有 ~1-2 秒拒接窗口）。详见 `docs/DEPLOYMENT.md §五`。

---

## 关键设计

### 钱相关防护

- 订单创建：`pg_advisory_xact_lock(user_id, episode_id)` + 部分唯一索引，杜绝并发产生多笔 pending
- 支付回调：webhook 必须 (订单存在 ∧ pending ∧ 未过期 ∧ 金额一致 ∧ 渠道一致) 才标 paid；失败发 alert + 业务异常 HTTP 500 让渠道重试，仅幂等成功 ack 200
- 过期订单：MarkOrderPaid 主动拒绝 `expired_at < now` 的 pending 单（与 close-expired cron 的竞态兜底），ack 200 + alert 让 ops 人工跟进退款
- 解锁：`(user_id, episode_id)` UNIQUE，webhook 自动解锁；用户手动调 unlock 接口幂等
- 分账：`creators` 行锁 + `total_income / balance` 原子增 + `creator_stats_daily` UPSERT，全部在一个事务里
- 提现：`balance → frozen` (申请) → `balance + frozen 双扣` (mark-paid) / `frozen → balance` (reject)

### 安全

- 三类身份独立 JWT subject（app/creator/admin），中间件拒绝跨身份调用
- 账号封禁即时失效（每次鉴权查库 `status=active`）
- Admin 暴破锁定：5 次错密码锁 15 分钟，DB 落盘
- SMS：连续错码 ≥5 次锁 15 分钟 + IP 级限流 + 60s 重发冷却（事务行锁防并发）
- PII：creator 银行卡 / 身份证 AES-GCM 加密落库，接口默认脱敏，admin 也只看 `***5678`
- CORS：白名单 + `Vary: Origin`，禁止 `*` + credentials

### 联调便利

- `PAYMENT_DEV_MODE=true`（默认）→ 所有支付走 DevProvider + 暴露 `POST /v1/dev/orders/:order_no/pay` 一键模拟
- `SMS_DEV_MODE=true` → 不调腾讯云，响应里回显 `dev_code`
- 任一第三方配置缺失自动 fallback 到 dev / unavailable，启动日志打 warning

---

## 上线 checklist

1. `cp .env.example .env` → 填齐全部密钥（特别是 `DATA_ENCRYPTION_KEY`、`JWT_SECRET`、`ADMIN_INIT_PASSWORD`）
2. `PAYMENT_DEV_MODE=false` + 微信 / 支付宝商户号配齐 → `/v1/dev/*` 自动消失
3. `SMS_DEV_MODE=false` + 腾讯云模板审核通过
4. `RATE_LIMIT_ENABLED=true` + `ALERT_ENABLED=true` + `ALERT_WEBHOOK_URL` 填飞书 / 钉钉机器人
5. `go run ./cmd/check-config --prod` 全绿
6. `go run ./cmd/reconcile` 全绿
7. 按 `docs/DEPLOYMENT.md` 配 systemd + Nginx + HTTPS
8. 健康检查：LB 配 `/ready`（含 Redis 探活），`/health` 给 systemd

---

## 协作

- 改动前先看 `docs/DEV_TEST_LOG.md §5.4` 待补清单，避免重复实现
- commit 用 `feat: / fix: / docs:` 前缀
- 不要在任何 commit 里带 `.env` / 真实密钥 / `DATA_ENCRYPTION_KEY`
