# short-drama-backend

<div align="center">

**生产级短剧平台后端 | A production-grade short-drama streaming platform backend, built with Go.**

[![Go Version](https://img.shields.io/badge/Go-1.25-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev/dl/)
[![Framework](https://img.shields.io/badge/Framework-Gin-009688?style=flat-square)](https://github.com/gin-gonic/gin)
[![Database](https://img.shields.io/badge/Database-PostgreSQL-336791?style=flat-square&logo=postgresql&logoColor=white)](https://www.postgresql.org/)
[![Cache](https://img.shields.io/badge/Cache-Redis-DC382D?style=flat-square&logo=redis&logoColor=white)](https://redis.io/)
[![License](https://img.shields.io/badge/License-Proprietary-lightgrey?style=flat-square)]()

[English Summary](#english-summary) · [项目概述](#项目概述) · [功能特性](#功能特性) · [快速开始](#快速开始) · [系统架构](#系统架构) · [API 文档](#api-文档) · [部署运维](#部署运维) · [目录结构](#目录结构)

</div>

---

## English Summary

**short-drama-backend** is a production-grade backend for a micro-drama (短剧) streaming platform, built solo from scratch in Go. It powers the complete business loop — *creators upload → admins review → distributors claim → users pay to unlock → revenue splits & settles → withdrawals process* — across **four independently-authenticated surfaces** (App / Creator / Distributor / Admin), spanning **45 tables** and **289 endpoints**.

The engineering focus is on the two things a payment backend should take most seriously: **money correctness** and **uptime**.

- **Payment integrity** — Concurrent ordering is serialized with Postgres transaction-level advisory locks plus partial unique indexes; payment callbacks are signature-verified and idempotent inside `SELECT … FOR UPDATE` transactions; order payment, unlock records, and revenue splits commit atomically; refunds support partial/idempotent flows with proportional balance clawback; active order-status queries reconcile state if callbacks are lost.
- **Distributor deposit & claim system** — Distributors place tiered deposits to claim dramas for specific platforms (¥400 base for ≤25min, ¥500 for ≥26min, +15% per additional platform); the abandon-claim flow supports symmetric refund algorithms within single transactions with row-level locking.
- **Payment-channel abstraction** — A unified `Provider` interface routes WeChat Pay V3 and Alipay across app & H5 scenes; missing credentials degrade to a safe "unavailable" provider.
- **Settlement engine** — Semi-monthly settlement cycles for both creators and distributors with tax-previewed withdrawals, PDF receipt generation, and invoice tracking.
- **Zero-downtime deploys** — `cloudflare/tableflip` + systemd enables hot-restarts via `SIGHUP` with listener FD inheritance.

> The sections below are in Chinese (the project's primary language).

---

## 项目概述

短剧是近年来快速增长的内容形态，但搭建一套短剧平台需要解决**支付资金安全**、**多方分账结算**、**高可用部署**等一系列硬问题。本项目提供了一套完整的、生产可用的后端服务，打通了从内容生产到资金结算的全链路：

**创作者上传** → **管理员审核上架** → **发行商押金认领发行** → **用户付费解锁观看** → **收益自动分账** → **半月度结算提现**。

### 数据概览

| 指标 | 数值 |
|------|------|
| 数据库表 | 45 张 |
| API 路由 | 289 条 |
| 客户端身份 | 4 端（App / 创作者 / 发行商 / 管理后台） |
| 支付渠道 | 2 个（微信支付 V3、支付宝） |
| 部署停机时间 | 0（`SIGHUP` 热重启） |

---

## 功能特性

### 四端业务

- **App 端（用户）** — 手机号验证码登录（未注册自动注册）、剧集浏览搜索、播放与观看历史、点赞/收藏、评论与楼中楼回复、消息中心、微信/支付宝双渠道付费解锁单集。
- **创作者端** — 创作者入驻、实名认证（腾讯云 KYC 活体检测）、短剧/剧集 CRUD、云点播上传签名 + 封面直传签名、收益看板、半月度结算、个税预览与算税提现、PDF 结算单、电子合同、发票管理。
- **发行商端** — 企业认证、剧集广场浏览筛选、**押金认领**（保证金按时长 × 平台数阶梯计算）、已认领剧集管理、**放弃认领**（支持上传截图证据、对称退还算法）、发行收益看板、结算与提现。
- **管理后台** — RBAC 细粒度权限控制（超管/财务/审核/认领审核/发行商审核）、内容审核（含**打回分集原因**）、创作者/发行商审核、订单与退款管理、渠道收入批量导入、结算单管理、操作审计日志。

### 支付与资金安全

- **并发下单防护** — 事务级咨询锁（`pg_advisory_xact_lock`）+ 部分唯一索引双保险，杜绝狂点导致的重复扣款；已解锁直接拦截，未过期 pending 订单复用，30 分钟 TTL 自动关单。
- **回调幂等与校验** — 支付回调在 `SELECT ... FOR UPDATE` 事务内验签处理，必须同时满足金额一致、渠道一致、订单状态 pending 才标记支付；校验失败返回 HTTP 500 强制渠道重试，绝不静默吞掉。
- **原子分账** — 订单标记已付、写入解锁记录、创作者收益累加、当日统计 UPSERT 在同一事务内完成，要么全成要么全滚。
- **退款（部分/全额/幂等）** — 以退款单号为幂等键，支持多次部分退款；按分成比例从创作者余额与当日统计回退，`GREATEST` 防护防写负。
- **对账兜底** — 60s ticker 关过期单、主动查单 `SyncOrderStatus` 回写状态、独立 `reconcile` 命令发现账务不平即 `exit 1`（可挂 CI/定时巡检）。

### 发行商押金与认领体系

- **阶梯押金算法** — 基础押金按时长分档（≤25 分钟 ¥400，≥26 分钟 ¥500），每增加一个平台 +15%。
- **认领流程** — 押金缴纳 → 授权确认 → 内容审核 → 合同签署 → 已授权，任意环节可驳回。
- **放弃认领** — 发行商可上传最多 9 张截图作为放弃证据；退还金额 = 原始押金 − 剩余平台应收押金（与认领算法对称）；审核期间平台锁定防抢认；事务行锁保证并发安全。
- **管理端审核** — 通过：事务内更新授权记录 + 解冻押金 + 记录流水；驳回：仅更新申请状态，发行商可重新申请。

### 工程能力

- **零停机发版** — `cloudflare/tableflip` + systemd `SIGHUP` 热重启，新进程继承监听 fd，Ready 后老进程优雅退出。
- **四身份 JWT 鉴权** — App/Creator/Distributor/Admin 四端 token 严格隔离，中间件拒绝跨身份调用；账号封禁每次请求查库即时生效。
- **PII 加密** — 身份证号、银行卡号 `AES-GCM` 密文落库，接口默认脱敏只回尾号。
- **Provider 抽象** — 支付、短信、KYC、存储均使用 Provider 接口，缺密钥时安全降级为 UnavailableProvider，拒绝裸跑。
- **限流与防爆破** — 令牌桶限流、Admin 密码错 5 次锁 15 分钟、短信错码 ≥5 次锁定 + IP 限流 + 60s 冷却。
- **审计日志** — 记录 `actor/method/path/action/status/IP/UA`，不记请求体避免敏感信息泄漏。

---

## 快速开始

### 前置依赖

- Go 1.25+
- PostgreSQL 13+
- Redis 6+

### 启动步骤

```bash
# 1. 克隆项目
git clone https://github.com/shaozheng0503/short-drama-backend.git
cd short-drama-backend

# 2. 创建数据库
createdb ai_drama

# 3. 准备配置
cp .env.example .env
# 至少配置以下字段：
#   DATABASE_DSN=postgres://user:pass@localhost:5432/ai_drama?sslmode=disable
#   JWT_SECRET=<随机 32 字节 base64>
#   DATA_ENCRYPTION_KEY=$(openssl rand -base64 32)

# 4. 启动（开发模式：短信回显验证码、支付走 mock）
set -a && source .env && set +a
go run ./cmd/api
```

服务默认监听 `:8080`。首次启动会自动建表（AutoMigrate），并按 `ADMIN_INIT_USERNAME` / `ADMIN_INIT_PASSWORD` 创建初始管理员。

### 开发模式

设置 `PAYMENT_DEV_MODE=true` 时挂载模拟支付接口，方便前端联调：

```bash
# 一键模拟支付成功
curl -X POST http://localhost:8080/v1/dev/orders/{order_no}/pay
```

典型联调链路：

```
POST /v1/common/sms/send → POST /v1/app/auth/login → GET /v1/app/dramas
→ GET /v1/app/episodes/:id/play → POST /v1/app/orders（下单）
→ POST /v1/dev/orders/:no/pay（模拟支付）→ GET /v1/app/episodes/:id/play（获取播放地址）
```

### 健康检查

```bash
curl http://localhost:8080/health   # 存活探针
curl http://localhost:8080/ready     # 就绪探针（DB + Redis 连通性）
```

---

## 系统架构

```mermaid
flowchart TB
    subgraph Clients["客户端 Clients"]
        U["App · 用户"]
        C["Creator · 创作者"]
        D["Publisher · 发行商"]
        A["Admin · 管理后台"]
    end

    subgraph API["API 层 · Gin"]
        MW["中间件链<br/>JWT 鉴权 · 令牌桶限流 · 幂等 · 操作审计"]
    end

    subgraph Domains["领域模块 Domain Modules"]
        B["billing<br/>订单/解锁/分账/退款/对账"]
        P["payment<br/>微信支付V3 · 支付宝 · Dev Mock"]
        CT["content<br/>剧集/播放/评论"]
        CR["creator<br/>实名/结算/提现/合同"]
        DS["distributor<br/>押金/认领/放弃"]
        ST["settlement<br/>半月度结算引擎"]
        INT["integrations<br/>短信 · COS · VOD · KYC · 告警"]
    end

    DB[("PostgreSQL<br/>45 表 · 事务+行锁+咨询锁")]
    RD[("Redis<br/>幂等/限流/验证码")]
    TC["腾讯云<br/>SMS · COS · VOD · KYC"]
    PAY["支付渠道<br/>微信支付V3 · 支付宝"]

    U & C & D & A -->|"HTTPS + JWT，四身份隔离"| MW
    MW --> Domains
    Domains --> DB
    Domains --> RD
    Domains --> TC
    P --> PAY
```

### 关键状态机

```
Order（订单）: pending → paid → partial_refunded → refunded
              pending → closed（30min 超时）
              pending → failed

Drama（剧集）: draft → reviewing → awaiting_publish → published → offline
              published → draft（打回 sendback，支持总体原因+分集原因）

Claim（认领）: deposit_pending → auth_pending → review_pending → contract_pending → authorized
              任意环节 → rejected

Abandon（放弃）: pending → approved（押金退还，授权记录变更）
                pending → rejected（可重新申请）

Withdrawal（提现）: pending → approved → paid
                   pending → rejected
```

---

## API 文档

### 统一响应格式

所有接口均返回以下格式：

```json
{
  "code": 0,
  "message": "ok",
  "data": {}
}
```

### 错误码

| code | HTTP | 含义 |
|-----:|:----:|------|
| 0 | 200 | 成功 |
| 40001 | 200 | 参数错误 / 验证码错误 |
| 40101 | 200 | 未登录 / token 失效 |
| 40301 | 200 | 无权限 / 账号封禁 / 身份不匹配 |
| 40401 | 200 | 资源不存在 |
| 40901 | 200 | 重复操作 / 频控冲突 |
| 42001 | 200 | 剧集未解锁 |
| 42901 | 429 | 命中限流 |
| 50001 | 500 | 服务端 / 第三方错误 |

业务错误统一返回 HTTP 200 + 非零 `code`，仅限流（429）和服务端错误（500）使用非 200 HTTP 状态码。

### 路由分布

| 前缀 | 数量 | 说明 |
|------|-----:|------|
| `/v1/app` | 39 | 用户端接口 |
| `/v1/creator` | 63 | 创作者端接口 |
| `/v1/distributor` | 7 | 发行商认证/资料 |
| `/v1/publisher` | 35 | 发行商广场/认领/放弃/结算 |
| `/v1/admin` | 131 | 管理后台接口（RBAC 鉴权） |
| `/v1/common` | 5 | 公共接口（短信、上传签名等） |
| `/v1/webhooks` | 3 | 支付/VOD 回调 |
| `/health` `/ready` | 2 | 健康检查 |

完整 OpenAPI 3.0 文档维护在 `docs/` 目录，并同步至 Apifox。

---

## 数据模型

45 张表按业务域划分：

| 域 | 表名 |
|----|------|
| 内容 | `dramas`, `episodes`, `drama_covers`, `drama_characters`, `categories`, `languages`, `drama_tags` |
| 用户与行为 | `users`, `play_histories`, `user_actions`, `comments`, `comment_likes`, `app_messages`, `notifications` |
| 交易 | `products`, `orders`, `episode_unlocks`, `creator_stats_daily`, `channel_income_daily`, `channel_income_import_batches` |
| 创作者与结算 | `creators`, `creator_channel_accounts`, `withdrawals`, `tax_brackets`, `contracts`, `settlements`, `settlement_items`, `invoices` |
| 发行商 | `distributors`, `distributor_applications`, `distributor_dramas`, `distributor_abandon_requests`, `distributor_contracts`, `distributor_deposit_transactions`, `distributor_income_daily`, `distributor_settlements`, `distributor_withdrawals`, `distributor_invoices` |
| 系统 | `admins`, `admin_permissions`, `sms_codes`, `operation_logs`, `global_configs`, `state_transitions` |

---

## 目录结构

```
├── cmd/
│   ├── api/                  HTTP 服务入口（graceful shutdown + 后台 cron + tableflip 零停机）
│   ├── check-config/         上线前配置体检
│   ├── close-expired-orders/ 过期订单关闭（api 后台 ticker 也会触发）
│   ├── gen-income-template/  渠道收入导入模板生成
│   ├── publish-scheduled/    定时发布到点短剧
│   ├── reconcile/            账务对账，不平则 exit 1
│   ├── seed-langzhi/         种子数据（开发/测试环境）
│   ├── setup-cos-referer/    COS Referer 防盗链配置
│   ├── test-alipay/          支付宝凭据/网关连通性烟测（不依赖 DB）
│   └── test-refund/          退款集成测试（14 场景 / 54 断言）
├── internal/
│   ├── config/               环境变量装配
│   ├── database/             连接 + AutoMigrate + 索引迁移 + 初始管理员
│   ├── model/                GORM 模型定义（45 张表）
│   ├── middleware/           JWT 鉴权（四端隔离）、RBAC
│   ├── handler/              HTTP handler（74 个文件，按业务域拆分）
│   ├── billing/              订单/解锁/分账/退款/对账（事务+行锁+咨询锁）
│   ├── payment/              支付 Provider 抽象（微信/支付宝/Dev/Unavailable）
│   ├── sms/                  短信 Provider 抽象
│   ├── kyc/                  实名认证 Provider（腾讯云 KYC）
│   ├── cos/                  腾讯云 COS 对象存储签名
│   ├── vod/                  腾讯云 VOD 云点播签名与回调
│   ├── secure/               AES-GCM 加密（PII 字段）
│   ├── idempotency/          Idempotency-Key 中间件
│   ├── ratelimit/            令牌桶限流
│   ├── alert/                失败事件异步 webhook 告警
│   ├── reconcile/            账务一致性校验
│   ├── redisclient/          Redis 客户端
│   ├── response/             统一响应格式
│   └── seed/                 种子数据辅助
├── docs/                     文档与 OpenAPI 规范
├── scripts/                  部署与迁移脚本
├── docker-compose.yml        本地开发环境
└── go.mod
```

---

## 部署运维

### 零停机发版

服务基于 `cloudflare/tableflip` 实现热重启：`systemctl reload` 发送 `SIGHUP` → fork+exec 新进程继承监听 fd → 新进程 Ready 后老进程优雅退出，连接不断、客户端零感知。

### 一键部署

```bash
# 生产环境
ENV=prod ./scripts/deploy-prod.sh

# 沙箱环境
ENV=sandbox ./scripts/deploy-prod.sh

# 双环境（先沙箱后生产）
ENV=both ./scripts/deploy-prod.sh
```

部署脚本流程：编译 → 配置体检 → 二进制上传 → 备份 → reload → ready 探针 → HTTPS 健康检查。

### 运维命令

```bash
go run ./cmd/check-config --prod    # 上线前配置体检
go run ./cmd/close-expired-orders   # 关闭过期未支付订单
go run ./cmd/reconcile              # 账务对账
```

systemd + Nginx + HTTPS 完整部署说明见 [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md)。

---

## 配置说明

所有配置通过环境变量注入，详见 `.env.example`。关键配置项：

| 变量 | 必填 | 说明 |
|------|:----:|------|
| `DATABASE_DSN` | 是 | PostgreSQL 连接串 |
| `JWT_SECRET` | 是 | JWT 签名密钥（≥32 字节） |
| `DATA_ENCRYPTION_KEY` | 是 | AES-GCM 加密密钥（`openssl rand -base64 32` 生成） |
| `REDIS_ADDR` | 生产 | Redis 地址（幂等/限流依赖） |
| `PAYMENT_DEV_MODE` | 否 | 设为 `true` 启用 mock 支付 |
| `SMS_DEV_MODE` | 否 | 设为 `true` 短信验证码回显到日志 |
| `ADMIN_INIT_USERNAME` / `ADMIN_INIT_PASSWORD` | 首次 | 初始管理员账号 |

微信支付、支付宝密钥通过文件路径注入（不进环境变量/不进 commit），降低泄漏面。

---

## 技术栈

| 层 | 技术选型 | 选型理由 |
|----|----------|----------|
| 语言 | Go 1.25 | 单二进制部署、启动快、并发模型契合 IO 密集型服务 |
| Web 框架 | Gin | 轻量高性能 HTTP 路由 |
| ORM | GORM | AutoMigrate 高效开发，热点路径手写索引优化 |
| 数据库 | PostgreSQL | ACID 事务、行锁、咨询锁、部分唯一索引 |
| 缓存 | Redis | 幂等键、令牌桶限流、短信验证码冷却 |
| 鉴权 | 自签 JWT | 四身份隔离，无外部依赖 |
| 短信 | 腾讯云 SMS | 生产真实下发，Dev Provider 本地调试 |
| 存储 | 腾讯云 COS | 客户端直传签名，服务端只签不存 |
| 视频 | 腾讯云 VOD | 上传签名 + 转码回调 |
| 支付 | 微信支付 V3 + 支付宝 | Provider 抽象，支持 app/H5 场景、退款、查单 |
| 部署 | systemd + tableflip | SIGHUP 热重启实现零停机发版 |

---

## 测试

- **单元测试**：覆盖支付验签、金额换算、退款逻辑等核心资金操作。
- **退款集成测试**（`cmd/test-refund`）：14 个场景 / 54 个断言，包含部分退/全退/同号幂等/超额拒/5 goroutine 并发行锁/分账回退/防写负/边界场景，使用快照 + defer 清理不污染数据。
- **支付宝烟测**（`cmd/test-alipay`）：验证凭据、网关连通性、本地签名，不依赖数据库。
- **上线前体检**：`check-config --prod` 校验关键配置；`reconcile` 账务不平即 `exit 1`。

运行测试：

```bash
go test ./...
```

---

## License

本仓库为真实生产项目的脱敏副本，仅作个人作品展示。未授予商用或二次分发许可。

Proprietary. This repository is a redacted copy of a production project for portfolio demonstration. Commercial use or redistribution is not granted without explicit permission.

---

<div align="center">
<sub>Built with Go, PostgreSQL, and a lot of care around money correctness.</sub>
</div>
