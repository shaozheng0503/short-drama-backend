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

# 订单关单（销毁待支付）时长，必须 > PAYMENT_EXPIRE_SECONDS，防"已关单但渠道仍可支付"资损
ORDER_PENDING_TTL_SECONDS=2700
# 预下单传给微信/支付宝的支付有效期，必须 < ORDER_PENDING_TTL_SECONDS（启动时 check-config 会校验）
PAYMENT_EXPIRE_SECONDS=1800

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

## 五、systemd 示例（含零停机升级支持）

API 进程通过 `github.com/cloudflare/tableflip` 实现 fork+exec 继承 listener 的零停机重启，配合 systemd 的 `PIDFile=` 跟踪 MainPID。**此 unit 中所有 `KillMode=process`、`PIDFile=`、`ExecReload=` 都是必填项**，缺一个零停机就会破：

```ini
[Unit]
Description=DramaBackend API
After=network.target postgresql.service redis.service

[Service]
Type=simple
User=drama
Group=drama
WorkingDirectory=/opt/drama-backend
EnvironmentFile=/opt/drama-backend/.env
ExecStart=/opt/drama-backend/drama-api
ExecReload=/bin/kill -HUP $MAINPID
PIDFile=/run/drama-api/pid
RuntimeDirectory=drama-api
Restart=on-failure
RestartSec=3
KillSignal=SIGTERM
KillMode=process
TimeoutStopSec=60

[Install]
WantedBy=multi-user.target
```

关键点说明：
- `PIDFile=/run/drama-api/pid` + `RuntimeDirectory=drama-api`：systemd 自动建 `/run/drama-api/`（属主 drama），tableflip 在 Ready 时把当前 PID 写入，systemd 据此重新跟踪升级后的 MainPID。
- `ExecReload=/bin/kill -HUP $MAINPID`：`systemctl reload` 触发 SIGHUP，tableflip 收到后 fork 新进程接管 listener fd，新进程 Ready 后老进程 graceful exit。
- `KillMode=process`：**必加**。默认 `control-group` 会在老进程退出/timeout 时把整个 cgroup（包括 tableflip 派生的新进程）一锅端，零停机失效。
- `Restart=on-failure`：升级中老进程 exit(0) 不视为失败，systemd 不会误触发重启；崩溃才兜底重启。
- API 进程同时也响应 `SIGTERM`（`systemctl stop/restart` 路径），在 `APP_SHUTDOWN_TIMEOUT_SECONDS` 内优雅停服。

### 零停机部署流程（日常推荐）

```bash
# 1. 本地交叉编译（stripped 减小传输体积）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags="-s -w" -o /tmp/drama-api ./cmd/api

# 2. scp 到临时路径（运行中的二进制不能直接覆盖，会 ETXTBSY）
scp /tmp/drama-api root@server:/tmp/drama-api.new

# 3. mv 替换 inode（老进程仍持有老 inode 继续跑）+ 权限
ssh root@server '
  mv -f /tmp/drama-api.new /opt/drama-backend/drama-api &&
  chown drama:drama /opt/drama-backend/drama-api &&
  chmod +x /opt/drama-backend/drama-api
'

# 4. 触发零停机升级（不是 restart！）
ssh root@server 'systemctl reload drama-backend'

# 5. 验证：MainPID 应已切换，NRestarts 仍为 0，/ready 通 200
ssh root@server '
  systemctl show -p MainPID -p NRestarts --value drama-backend
  curl -sS http://localhost:18080/ready
'
```

每次部署 `reload` 完成时间约 50ms～几秒（取决于 inflight 请求 graceful 时间），客户端零感知；MainPID 切换后老 PID 自动清理。

### 何时仍要用 `systemctl restart`

- 改了 `EnvironmentFile`（环境变量）/ unit 文件后；
- 数据库 schema 大变更需要全停服迁移；
- 服务已经卡死、reload 无响应。

`restart` 会有 ~1-2 秒接口拒接窗口，非日常发版场景才用。

### 回滚

```bash
# 备份在 /opt/drama-backend/drama-api.bak.<timestamp>，挑最近一份
ssh root@server '
  ls -lt /opt/drama-backend/drama-api.bak.* | head -3
  mv -f /opt/drama-backend/drama-api.bak.<ts> /opt/drama-backend/drama-api &&
  chown drama:drama /opt/drama-backend/drama-api &&
  chmod +x /opt/drama-backend/drama-api &&
  systemctl reload drama-backend
'
```

回滚同样走 `reload`，零停机。

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
