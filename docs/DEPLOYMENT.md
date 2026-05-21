# DramaBackend 部署说明（MVP）

> 本文只提供部署文档和示例配置，不直接修改服务器。

## 一、推荐拓扑

```text
Internet
  -> HTTPS / Nginx
  -> DramaBackend API (:8080)
  -> PostgreSQL
  -> Redis
```

Nginx 负责 HTTPS、反向代理、真实 IP 透传和基础超时控制。API 服务只监听内网端口。

## 二、上线前检查

```bash
go test ./...
go run ./cmd/check-config --prod
go run ./cmd/reconcile
```

`check-config --prod` 必须输出 `status: OK` 才建议启动生产服务。`reconcile` 如果输出 `status: FAILED`，先处理账务异常。

## 三、关键环境变量

```bash
APP_ADDR=:8080
APP_SHUTDOWN_TIMEOUT_SECONDS=10
CORS_ALLOWED_ORIGINS=https://app.example.com,https://admin.example.com

DATABASE_DSN='host=127.0.0.1 user=ai_drama password=*** dbname=ai_drama port=5432 sslmode=disable TimeZone=Asia/Shanghai'

REDIS_ADDR=127.0.0.1:6379
REDIS_PASSWORD=
REDIS_DB=0
IDEMPOTENCY_TTL_SECONDS=1800

RATE_LIMIT_ENABLED=true
RATE_LIMIT_RPS=20
RATE_LIMIT_BURST=40

ALERT_ENABLED=true
ALERT_WEBHOOK_URL=https://ops.example.com/drama-alert
ALERT_TIMEOUT_SECONDS=3

JWT_SECRET=change-to-a-long-random-secret
DATA_ENCRYPTION_KEY='openssl-rand-base64-32-output'

SMS_DEV_MODE=false
PAYMENT_DEV_MODE=false
```

`DATA_ENCRYPTION_KEY` 生成方式：

```bash
openssl rand -base64 32
```

生产环境不要使用默认管理员密码、默认 JWT 密钥或 `CORS_ALLOWED_ORIGINS=*`。

## 四、Nginx HTTPS 示例

```nginx
upstream drama_backend {
    server 127.0.0.1:8080;
    keepalive 32;
}

server {
    listen 80;
    server_name api.example.com;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl http2;
    server_name api.example.com;

    ssl_certificate     /etc/nginx/certs/api.example.com/fullchain.pem;
    ssl_certificate_key /etc/nginx/certs/api.example.com/privkey.pem;

    client_max_body_size 20m;

    location /health {
        proxy_pass http://drama_backend;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location /ready {
        proxy_pass http://drama_backend;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location / {
        proxy_pass http://drama_backend;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Connection "";
        proxy_connect_timeout 5s;
        proxy_send_timeout 60s;
        proxy_read_timeout 60s;
    }
}
```

## 五、systemd 示例

```ini
[Unit]
Description=DramaBackend API
After=network.target postgresql.service redis.service

[Service]
Type=simple
WorkingDirectory=/opt/drama-backend
EnvironmentFile=/opt/drama-backend/.env
ExecStart=/opt/drama-backend/drama-api
Restart=always
RestartSec=3
KillSignal=SIGTERM
TimeoutStopSec=15

[Install]
WantedBy=multi-user.target
```

发布 / 重启时 API 会处理 `SIGTERM`，并在 `APP_SHUTDOWN_TIMEOUT_SECONDS` 内优雅停止 HTTP 服务和后台任务。

## 六、探针与联调

- 存活探针：`GET /health`
- 就绪探针：`GET /ready`
- 前端跨域：把前端测试域名加入 `CORS_ALLOWED_ORIGINS`
- 支付回调域名：
  - 微信：`https://api.example.com/v1/webhooks/wechat/pay`
  - 支付宝：`https://api.example.com/v1/webhooks/alipay/pay`

## 七、基础运维命令

```bash
# 手动关闭过期订单
go run ./cmd/close-expired-orders

# 账务对账
go run ./cmd/reconcile

# 生产配置检查
go run ./cmd/check-config --prod
```

## 九、告警 Webhook

启用 `ALERT_ENABLED=true` 后，服务会把低频但重要的运维事件以 JSON POST 到 `ALERT_WEBHOOK_URL`。

当前已接入事件：

- `expired_orders_closed`：过期订单批量关闭。
- `close_expired_orders_failed`：过期订单关闭任务失败。
- `payment_webhook_failed`：支付回调处理失败。

示例 payload：

```json
{
  "level": "warn",
  "type": "expired_orders_closed",
  "message": "过期订单已关闭",
  "fields": {
    "closed_count": 1,
    "sample_order_nos": ["ORDER_NO"]
  },
  "at": "2026-05-20T16:31:26+08:00"
}
```

Webhook 发送失败只写服务日志，不阻断支付、订单、提现等主业务。

## 十、上线前人工确认

- `/health` 返回 HTTP 200。
- `/ready` 返回 HTTP 200，且 `database=ok`。
- `go run ./cmd/check-config --prod` 输出 `status: OK`。
- `go run ./cmd/reconcile` 输出 `status: OK`。
- Nginx HTTPS 证书有效，HTTP 自动跳转 HTTPS。
- 微信 / 支付宝回调域名已经配置为 HTTPS。
- Redis 可用，订单 / 提现接口 `Idempotency-Key` 生效。
