# Drama Platform Backend

<div align="center">

**A production-grade short-drama streaming platform backend, built with Go.**

[![Go Version](https://img.shields.io/badge/Go-1.25-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev/dl/)
[![Framework](https://img.shields.io/badge/Framework-Gin-009688?style=flat-square)](https://github.com/gin-gonic/gin)
[![Database](https://img.shields.io/badge/Database-PostgreSQL-336791?style=flat-square&logo=postgresql&logoColor=white)](https://www.postgresql.org/)
[![Cache](https://img.shields.io/badge/Cache-Redis-DC382D?style=flat-square&logo=redis&logoColor=white)](https://redis.io/)
[![License](https://img.shields.io/badge/License-Proprietary-lightgrey?style=flat-square)]()

[Features](#features) · [Quick Start](#quick-start) · [Architecture](#architecture) · [API Reference](#api-reference) · [Deployment](#deployment) · [Project Structure](#project-structure)

</div>

---

## Overview

Short-drama (micro-drama) platforms have become a major content format, but building one requires solving hard problems around **payment integrity**, **multi-party revenue settlement**, and **high availability**. This project provides a complete, production-ready backend that powers the full business loop:

**Creators upload** → **Admins review & publish** → **Distributors claim & distribute** → **Users pay to unlock** → **Revenue splits & settles** → **Withdrawals process**.

### At a Glance

| Metric | Value |
|--------|-------|
| Database tables | 45 |
| API endpoints | 289 |
| Client surfaces | 4 (App / Creator / Distributor / Admin) |
| Payment channels | 2 (WeChat Pay V3, Alipay) |
| Deployment downtime | Zero (hot-reload via `SIGHUP`) |

---

## Features

### Multi-Sided Platform

- **App (Users)** — Phone/SMS login, drama browsing & search, episode playback with history, likes & favorites, comments, in-app purchases with WeChat Pay & Alipay.
- **Creator Portal** — KYC verification, drama & episode management, VOD upload signatures, revenue dashboard, semi-monthly settlement, tax-calculated withdrawals with PDF receipts, e-contracts, invoicing.
- **Distributor Portal** — Enterprise verification, drama marketplace, deposit-based platform claiming (tiered pricing by duration + platform count), abandon-claim workflow with symmetric deposit refunds, distribution revenue tracking.
- **Admin Panel** — Role-based access control (super_admin / finance / auditor / claim_audit / distributor_audit), content review (with per-episode sendback reasons), order & refund management, channel income import, settlement management, operation audit logs.

### Payment & Financial Safety

- **Concurrent order safety** — Transaction-level advisory locks (`pg_advisory_xact_lock`) + partial unique indexes prevent duplicate charges from double-clicks.
- **Idempotent callbacks** — Payment notifications are signature-verified inside `SELECT ... FOR UPDATE` transactions; amount/channel mismatches return HTTP 500 to force gateway retries rather than silently failing.
- **Atomic revenue split** — Order payment, episode unlock, and creator revenue crediting commit atomically in a single transaction.
- **Partial & full refunds** — Idempotent refund with proportional balance clawback from creators; supports multiple partial refunds with `GREATEST` guards against negative balances.
- **Reconciliation** — Active order-status queries sync with payment gateways; a standalone `reconcile` command detects inconsistencies and exits non-zero for CI/cron alerts.

### Distributor Deposit System

- **Tiered deposit calculation** — Base deposit by drama duration (≤25min: ¥400, ≥26min: ¥500), with +15% per additional platform.
- **Claim workflow** — Deposit payment → authorization → content review → contract signing → authorized distribution.
- **Abandon-claim flow** — Distributors can relinquish platforms with evidence (screenshots up to 9 images); symmetric refund algorithm returns `original_deposit - remaining_platforms_share`; platforms remain locked during review to prevent claim races.
- **Transaction-safe approval** — Admin approval uses row-level locking in a single transaction to ensure wallet consistency.

### Engineering

- **Zero-downtime deploys** — `cloudflare/tableflip` + systemd `SIGHUP` hot-reload with listener FD inheritance.
- **Four-identity JWT auth** — App/Creator/Distributor/Admin tokens are strictly isolated at the middleware layer; cross-identity calls are rejected.
- **PII encryption** — ID numbers and bank card numbers are stored AES-GCM encrypted; APIs return masked values by default.
- **Provider abstraction** — Payment (`Prepay`/`VerifyAndParse`/`QueryOrder`/`Refund`), SMS, KYC, and storage all use provider interfaces; missing credentials gracefully degrade to safe "unavailable" providers rather than failing open.
- **Rate limiting & brute-force protection** — Token-bucket rate limiting, login lockout (5 attempts / 15min), SMS code cooldown with IP-based throttling.
- **Audit logging** — All admin actions logged with actor/method/path/status/IP/UA; request bodies are never recorded.

---

## Architecture

```mermaid
flowchart TB
    subgraph Clients["Clients"]
        U["App (Users)"]
        C["Creator Portal"]
        D["Distributor Portal"]
        A["Admin Panel"]
    end

    subgraph API["API Layer (Gin)"]
        MW["Middleware<br/>JWT Auth · Rate Limit · Idempotency · Audit Log"]
    end

    subgraph Domains["Domain Modules"]
        B["billing<br/>Orders · Unlocks · Refunds · Reconciliation"]
        P["payment<br/>WeChat Pay · Alipay · Dev Mock"]
        CT["content<br/>Dramas · Episodes · Playback"]
        CR["creator<br/>KYC · Settlement · Withdrawals"]
        DS["distributor<br/>Deposits · Claims · Abandonment"]
        ST["settlement<br/>Cycle-based Settlement Engine"]
        INT["integrations<br/>SMS · COS · VOD · KYC · Alerts"]
    end

    DB[("PostgreSQL<br/>45 tables · Row locks · Advisory locks")]
    RD[("Redis<br/>Idempotency · Rate limits · SMS codes")]
    TC["Tencent Cloud<br/>SMS · COS · VOD · KYC"]
    PAY["Payment Gateways<br/>WeChat Pay V3 · Alipay"]

    U & C & D & A --> MW
    MW --> Domains
    Domains --> DB
    Domains --> RD
    Domains --> TC
    P --> PAY
```

### State Machines

```
Order      : pending → paid → partial_refunded → refunded
             pending → closed (timeout 30min)
             pending → failed

Drama      : draft → reviewing → awaiting_publish → published → offline
             published → draft (sendback with per-episode reasons)

Claim      : deposit_pending → auth_pending → review_pending → contract_pending → authorized
             any stage → rejected

Abandon    : pending → approved (deposit refunded, authorization updated)
             pending → rejected (re-submittable)

Withdrawal : pending → approved → paid
             pending → rejected
```

---

## Quick Start

### Prerequisites

- Go 1.25+
- PostgreSQL 13+
- Redis 6+

### Setup

```bash
# 1. Clone
git clone https://github.com/shaozheng0503/short-drama-backend.git
cd short-drama-backend

# 2. Create database
createdb ai_drama

# 3. Configure
cp .env.example .env
# Edit .env — at minimum set:
#   DATABASE_DSN=postgres://user:pass@localhost:5432/ai_drama?sslmode=disable
#   JWT_SECRET=<random-32-byte-base64>
#   DATA_ENCRYPTION_KEY=$(openssl rand -base64 32)

# 4. Run (dev mode: SMS echoes codes, payments use mock provider)
set -a && source .env && set +a
go run ./cmd/api
```

The server starts on `:8080` by default. Tables are auto-migrated on first startup, and a default admin is created from `ADMIN_INIT_USERNAME` / `ADMIN_INIT_PASSWORD`.

### Development Mode

With `PAYMENT_DEV_MODE=true`, a mock payment endpoint is available:

```bash
# Simulate successful payment for testing
curl -X POST http://localhost:8080/v1/dev/orders/{order_no}/pay
```

Typical dev flow:

```
POST /v1/common/sms/send → POST /v1/app/auth/login → GET /v1/app/dramas
→ GET /v1/app/episodes/:id/play → POST /v1/app/orders (create order)
→ POST /v1/dev/orders/:no/pay (mock pay) → GET /v1/app/episodes/:id/play (get play_url)
```

### Health Checks

```bash
curl http://localhost:8080/health   # liveness
curl http://localhost:8080/ready     # readiness (DB + Redis ping)
```

---

## API Reference

### Response Format

All responses follow the convention:

```json
{
  "code": 0,
  "message": "ok",
  "data": {}
}
```

| Code | HTTP | Meaning |
|-----:|:----:|---------|
| 0 | 200 | Success |
| 40001 | 200 | Invalid parameters / SMS code error |
| 40101 | 200 | Unauthenticated / token expired |
| 40301 | 200 | Forbidden / banned / identity mismatch |
| 40401 | 200 | Resource not found |
| 40901 | 200 | Duplicate operation / rate limit conflict |
| 42001 | 200 | Episode not unlocked |
| 42901 | 429 | Rate limited |
| 50001 | 500 | Server / third-party error |

Business errors return HTTP 200 with a non-zero `code`; only rate limits (429) and server errors (500) use non-200 HTTP statuses.

### Endpoint Overview

| Prefix | Count | Description |
|--------|------:|-------------|
| `/v1/app` | 39 | User-facing APIs |
| `/v1/creator` | 63 | Creator portal APIs |
| `/v1/distributor` | 7 | Distributor auth & profile |
| `/v1/publisher` | 35 | Distributor marketplace, claims, settlement |
| `/v1/admin` | 131 | Admin panel APIs (RBAC-gated) |
| `/v1/common` | 5 | Public utilities (SMS, upload signatures, etc.) |
| `/v1/webhooks` | 3 | Payment & VOD callbacks |
| `/health`, `/ready` | 2 | Health checks |

Full OpenAPI 3.0 specification is maintained in the `docs/` directory and synchronized to Apifox.

---

## Project Structure

```
├── cmd/
│   ├── api/                  # HTTP server entrypoint (graceful shutdown + cron + tableflip)
│   ├── check-config/         # Pre-deploy configuration validation
│   ├── close-expired-orders/ # Expired order sweeper
│   ├── gen-income-template/  # Channel income import template generator
│   ├── publish-scheduled/    # Scheduled drama publisher
│   ├── reconcile/            # Financial reconciliation (exits non-zero on mismatch)
│   ├── seed-langzhi/         # Development seed data
│   ├── setup-cos-referer/    # COS referer anti-hotlink setup
│   ├── test-alipay/          # Alipay connectivity smoke test
│   └── test-refund/          # Refund integration tests (14 scenarios, 54 assertions)
├── internal/
│   ├── config/               # Environment configuration
│   ├── database/             # DB connection, AutoMigrate, index migrations
│   ├── model/                # GORM models (45 tables)
│   ├── middleware/           # JWT auth (4 identities), RBAC
│   ├── handler/              # HTTP handlers (74 files, organized by domain)
│   ├── billing/              # Orders, unlocks, refunds, reconciliation
│   ├── payment/              # Payment provider abstraction (WeChat / Alipay / Dev / Unavailable)
│   ├── sms/                  # SMS provider abstraction
│   ├── kyc/                  # KYC provider abstraction (Tencent Cloud)
│   ├── cos/                  # Tencent COS (object storage) signatures
│   ├── vod/                  # Tencent VOD (video on demand) signatures & callbacks
│   ├── secure/               # AES-GCM encryption for PII fields
│   ├── idempotency/          # Idempotency-Key middleware
│   ├── ratelimit/            # Token-bucket rate limiter
│   ├── alert/                # Async webhook alerts for failures
│   ├── reconcile/            # Financial consistency checks
│   ├── redisclient/          # Redis client
│   ├── response/             # Unified response helpers
│   └── seed/                 # Seed data helpers
├── docs/                     # Documentation & OpenAPI specs
├── scripts/                  # Deployment & migration scripts
├── docker-compose.yml        # Local dev environment
└── go.mod
```

---

## Deployment

### Zero-Downtime Deploy

The service uses `cloudflare/tableflip` for hot-reloading. A `systemctl reload` (which sends `SIGHUP`) forks a new process that inherits the listening socket, serves new connections once ready, and gracefully shuts down the old process — zero dropped connections.

### One-Command Deploy

```bash
# Production
ENV=prod ./scripts/deploy-prod.sh

# Sandbox
ENV=sandbox ./scripts/deploy-prod.sh

# Both (sandbox first, then production)
ENV=both ./scripts/deploy-prod.sh
```

The script handles: compilation → config check → binary upload → backup → reload → readiness probe → HTTPS health check.

### Standalone

```bash
GOOS=linux GOARCH=amd64 go build -o drama-api ./cmd/api
./drama-api
```

See [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) for systemd + Nginx + HTTPS setup details.

### Operational Commands

```bash
go run ./cmd/check-config --prod    # Validate production config
go run ./cmd/close-expired-orders   # Close expired pending orders
go run ./cmd/reconcile              # Financial reconciliation
```

---

## Configuration

All configuration is via environment variables. See `.env.example` for the complete list. Key variables:

| Variable | Required | Description |
|----------|:--------:|-------------|
| `DATABASE_DSN` | Yes | PostgreSQL connection string |
| `JWT_SECRET` | Yes | JWT signing key (≥32 bytes) |
| `DATA_ENCRYPTION_KEY` | Yes | AES-GCM key for PII encryption (`openssl rand -base64 32`) |
| `REDIS_ADDR` | Prod | Redis address (required for idempotency in production) |
| `PAYMENT_DEV_MODE` | No | Set to `true` to enable mock payment provider |
| `SMS_DEV_MODE` | No | Set to `true` to echo SMS codes in logs |
| `ADMIN_INIT_USERNAME` / `ADMIN_INIT_PASSWORD` | First run | Initial admin credentials |

WeChat Pay and Alipay credentials are loaded from file paths (not env vars) to reduce leak surface area.

---

## Data Model

Tables are organized by domain:

| Domain | Tables |
|--------|--------|
| Content | `dramas`, `episodes`, `drama_covers`, `drama_characters`, `categories`, `languages`, `drama_tags` |
| Users | `users`, `play_histories`, `user_actions`, `comments`, `comment_likes`, `app_messages`, `notifications` |
| Transactions | `products`, `orders`, `episode_unlocks`, `creator_stats_daily`, `channel_income_daily`, `channel_income_import_batches` |
| Creators | `creators`, `creator_channel_accounts`, `withdrawals`, `tax_brackets`, `contracts`, `settlements`, `settlement_items`, `invoices` |
| Distributors | `distributors`, `distributor_applications`, `distributor_dramas`, `distributor_abandon_requests`, `distributor_contracts`, `distributor_deposit_transactions`, `distributor_income_daily`, `distributor_settlements`, `distributor_withdrawals`, `distributor_invoices` |
| System | `admins`, `admin_permissions`, `sms_codes`, `operation_logs`, `global_configs`, `state_transitions` |

---

## Testing

- **Unit tests** cover payment signature verification, amount calculations, refund logic, and core financial operations.
- **Refund integration test** (`cmd/test-refund`): 14 scenarios, 54 assertions — partial refunds, full refunds, idempotent re-submission, over-refund rejection, concurrent row locking, revenue clawback, negative-balance guards, and edge cases. Uses snapshot + defer cleanup to avoid polluting data.
- **Alipay smoke test** (`cmd/test-alipay`): Validates credentials, gateway connectivity, and local signing without requiring a database.
- **Pre-deploy validation**: `check-config --prod` verifies critical configuration; `reconcile` exits non-zero on financial inconsistencies.

Run tests:

```bash
go test ./...
```

---

## Tech Stack

| Layer | Technology | Rationale |
|-------|------------|-----------|
| Language | Go 1.25 | Single binary, fast startup, excellent concurrency for IO-heavy APIs |
| Framework | Gin | Minimalist, high-performance HTTP router |
| ORM | GORM | Productive with AutoMigrate; hand-tuned indexes for hot paths |
| Database | PostgreSQL | ACID transactions, row-level locking, advisory locks, partial unique indexes |
| Cache | Redis | Idempotency keys, token-bucket rate limiting, SMS code cooldown |
| Auth | Self-signed JWT | Four-subject isolation without external dependencies |
| SMS | Tencent Cloud SMS | Real delivery in production, dev provider for local testing |
| Storage | Tencent Cloud COS | Direct upload signatures (client uploads, server only signs) |
| Video | Tencent Cloud VOD | Video upload signatures + transcoding callbacks |
| Payments | WeChat Pay V3, Alipay | Provider-abstracted, supports app/H5 scenes, refunds, order queries |
| Deployment | systemd + tableflip | SIGHUP hot-reload for zero-downtime releases |

---

## License

Proprietary. This repository is a redacted copy of a production project for portfolio demonstration purposes. Commercial use or redistribution is not granted without explicit permission.

---

<div align="center">
<sub>Built with Go, PostgreSQL, and a lot of care around money correctness.</sub>
</div>
