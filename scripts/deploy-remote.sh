#!/usr/bin/env bash
# 零停机部署 drama-api 到远程服务器（systemctl reload drama-backend）
#
# 用法：
#   SSH_HOST=43.143.212.37 SSH_PORT=22 SSH_KEY=~/.ssh/your_key \
#     ./scripts/deploy-remote.sh
#
# 可选：SSH_USER（默认 root）、REMOTE_DIR（默认 /opt/drama-backend）
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

SSH_HOST="${SSH_HOST:?请设置 SSH_HOST（如 43.143.212.37）}"
SSH_PORT="${SSH_PORT:-22}"
SSH_USER="${SSH_USER:-root}"
SSH_KEY="${SSH_KEY:-}"
REMOTE_DIR="${REMOTE_DIR:-/opt/drama-backend}"
SERVICE="${SERVICE:-drama-backend}"
LOCAL_BIN="${LOCAL_BIN:-/tmp/drama-api}"

SSH_OPTS=(-o StrictHostKeyChecking=accept-new -p "$SSH_PORT")
[[ -n "$SSH_KEY" ]] && SSH_OPTS+=(-i "$SSH_KEY")
SSH=(ssh "${SSH_OPTS[@]}" "${SSH_USER}@${SSH_HOST}")
SCP=(scp "${SSH_OPTS[@]}")

echo "==> 交叉编译 linux/amd64 ..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags="-s -w" -o "$LOCAL_BIN" ./cmd/api
ls -lh "$LOCAL_BIN"

TS="$(date +%Y%m%d-%H%M%S)"
echo "==> 上传二进制 ..."
"${SCP[@]}" "$LOCAL_BIN" "${SSH_USER}@${SSH_HOST}:/tmp/drama-api.new"

echo "==> 备份 + 替换 + reload ..."
"${SSH[@]}" bash -s <<EOF
set -euo pipefail
if [[ -f ${REMOTE_DIR}/drama-api ]]; then
  cp -a ${REMOTE_DIR}/drama-api ${REMOTE_DIR}/drama-api.bak.${TS}
fi
mv -f /tmp/drama-api.new ${REMOTE_DIR}/drama-api
chown drama:drama ${REMOTE_DIR}/drama-api
chmod +x ${REMOTE_DIR}/drama-api
systemctl reload ${SERVICE}
systemctl show -p MainPID -p ActiveState -p NRestarts --value ${SERVICE}
curl -sS http://127.0.0.1:18080/ready || curl -sS http://127.0.0.1/ready
EOF

echo "==> 部署完成"
