# AI Drama Platform API

Golang + PostgreSQL MVP backend for the AI short-drama platform described in `AI短剧平台建设规划 (1).pdf`.

## Features

- APP: register/login, drama list/detail/search, episodes, like/favorite/share, comments, watch history, check-in points, mock orders, notifications.
- Creator: profile and identity data, dashboard, drama and episode upload metadata, contracts, revenue records, withdrawals.
- Admin: dashboard, users, creator verification, drama publishing, finance, withdrawal review, contract status management.
- Persistence: PostgreSQL through GORM auto migrations.
- Auth: JWT bearer tokens with `user`, `creator`, and `admin` roles.

## Run

```powershell
cd D:\personalProject\ai-drama-platform
docker compose up -d
$env:DATABASE_DSN="host=localhost user=postgres password=postgres dbname=ai_drama port=5432 sslmode=disable TimeZone=Asia/Shanghai"
$env:JWT_SECRET="local-dev-secret"
go run .\cmd\api
```

The API listens on `http://localhost:8080` by default.

## Quick API Examples

Register an admin:

```powershell
Invoke-RestMethod -Method Post http://localhost:8080/api/v1/auth/register `
  -ContentType 'application/json' `
  -Body '{"phone":"13800000000","password":"123456","nickname":"admin","role":"admin"}'
```

Register a creator:

```powershell
Invoke-RestMethod -Method Post http://localhost:8080/api/v1/auth/register `
  -ContentType 'application/json' `
  -Body '{"phone":"13800000001","password":"123456","nickname":"creator","role":"creator"}'
```

Create a published drama as admin:

```powershell
Invoke-RestMethod -Method Post http://localhost:8080/api/v1/admin/dramas `
  -Headers @{Authorization="Bearer <token>"} `
  -ContentType 'application/json' `
  -Body '{"title":"逆袭人生","description":"都市短剧","cover_url":"https://example.com/cover.jpg","category":"都市","region":"CN","language":"zh-CN","status":"published"}'
```

## Main Routes

- `POST /api/v1/auth/register`
- `POST /api/v1/auth/login`
- `GET /api/v1/dramas`
- `GET /api/v1/dramas/:id`
- `GET /api/v1/dramas/:id/episodes`
- `GET /api/v1/search?q=keyword`
- `POST /api/v1/dramas/:id/like`
- `POST /api/v1/dramas/:id/favorite`
- `POST /api/v1/dramas/:id/share`
- `POST /api/v1/dramas/:id/comments`
- `PUT /api/v1/watch-history`
- `POST /api/v1/checkins`
- `POST /api/v1/orders`
- `POST /api/v1/orders/:id/pay`
- `POST /api/v1/creator/profile`
- `GET /api/v1/creator/dashboard`
- `POST /api/v1/creator/dramas`
- `POST /api/v1/creator/dramas/:id/episodes`
- `POST /api/v1/creator/contracts`
- `POST /api/v1/creator/withdrawals`
- `GET /api/v1/admin/dashboard`
- `PUT /api/v1/admin/creators/:id/verify`
- `POST /api/v1/admin/dramas`
- `PUT /api/v1/admin/dramas/:id/status`
- `GET /api/v1/admin/finance/orders`
- `GET /api/v1/admin/finance/withdrawals`
- `GET /api/v1/admin/contracts`

External systems in the PDF, such as SMS, cloud VOD, WeChat/Alipay, and Tencent E-Sign, are represented as persisted business states and mock transitions. Their adapters can be added behind the existing order, episode, and contract routes.
