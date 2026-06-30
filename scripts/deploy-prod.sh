#!/usr/bin/env bash
# DramaBackend 零停机部署 + 删除接口冒烟
# 目标机：api.langzhi.top 生产机 43.143.212.37
# 登录账号：ubuntu（普通账户，sudo 免密）
# 登录方式：SSH 密钥（~/.ssh/id_ed25519_drama_deploy）
#
# 使用方法：
#   export ADMIN_TOKEN='eyJ...'        # 从 /v1/admin/auth/login 拿
#   export DRAFT_ID=999                # 一个 status=draft 的剧集 id
#   ./scripts/deploy-prod.sh
#
# 中间任何一步失败立即 exit，零破坏。

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# ======== 配置 ========
SSH_HOST="${SSH_HOST:-43.143.212.37}"
SSH_PORT="${SSH_PORT:-22}"
SSH_USER="${SSH_USER:-ubuntu}"
SSH_KEY="${SSH_KEY:-~/.ssh/id_ed25519_drama_deploy}"   # 已验证可用
REMOTE_DIR="${REMOTE_DIR:-/opt/drama-backend}"
SERVICE="${SERVICE:-drama-backend}"
API_BASE="${API_BASE:-https://api.langzhi.top}"
ADMIN_TOKEN="${ADMIN_TOKEN:-}"
DRAFT_ID="${DRAFT_ID:-}"
# ======================

R='\033[0;31m'; G='\033[0;32m'; Y='\033[1;33m'; N='\033[0m'
ok()   { echo -e "${G}[OK]${N} $*"; }
warn() { echo -e "${Y}[WARN]${N} $*"; }
err()  { echo -e "${R}[ERR]${N} $*"; exit 1; }

SSH_KEY_E="${SSH_KEY/#\~/$HOME}"
[[ -f "$SSH_KEY_E" ]] || err "私钥不存在：$SSH_KEY_E"

SSH_OPTS=(-o StrictHostKeyChecking=accept-new -p "$SSH_PORT" -i "$SSH_KEY_E")
SSH=(ssh "${SSH_OPTS[@]}" "${SSH_USER}@${SSH_HOST}")
SCP=(scp "${SSH_OPTS[@]}")

ok "目标: ${SSH_USER}@${SSH_HOST}:${REMOTE_DIR} (service=${SERVICE})"

# ---------- 1. SSH + sudo 探活 ----------
echo "==> SSH 连通性 + sudo 探活..."
"${SSH[@]}" 'whoami && sudo -n true && echo SUDO_OK' \
  | tee /tmp/_probe.log | grep -q SUDO_OK || { cat /tmp/_probe.log; err "SSH/sudo 失败"; }
ok "SSH + sudo 均可用"

# ---------- 2. 代码 + 单测 + check-config ----------
echo "==> git 状态..."
git status --short
git fetch --all
warn "提示：如需更新代码，请先手动 git pull --rebase，然后重跑本脚本"

echo "==> go test ./..."
go test ./... 2>&1 | tail -50

echo "==> check-config --prod..."
go run ./cmd/check-config --prod 2>&1 | tail -20

# ---------- 3. 编译 ----------
echo "==> 交叉编译 linux/amd64 ..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags="-s -w" -o /tmp/drama-api ./cmd/api
ls -lh /tmp/drama-api
ok "编译完成"

# ---------- 4. 上传 + 备份 + 替换 + reload ----------
TS="$(date +%Y%m%d-%H%M%S)"
echo "==> 上传二进制..."
"${SCP[@]}" /tmp/drama-api "${SSH_USER}@${SSH_HOST}:/tmp/drama-api.new"

echo "==> 备份 + 替换 + reload（远程走 sudo）..."
"${SSH[@]}" sudo bash -s <<EOF
set -euo pipefail
if [[ -f ${REMOTE_DIR}/drama-api ]]; then
  cp -a ${REMOTE_DIR}/drama-api ${REMOTE_DIR}/drama-api.bak.${TS}
fi
mv -f /tmp/drama-api.new ${REMOTE_DIR}/drama-api
chown drama:drama ${REMOTE_DIR}/drama-api
chmod +x ${REMOTE_DIR}/drama-api
systemctl reload ${SERVICE}
sleep 1
systemctl show -p MainPID -p ActiveState -p NRestarts --value ${SERVICE}
EOF
ok "reload 完成"

# ---------- 5. /ready 探活 ----------
echo "==> /ready 探活（内网 127.0.0.1:8080）..."
"${SSH[@]}" sudo bash -c 'curl -fsS http://127.0.0.1:8080/ready' >/dev/null \
  || err "/ready 失败（先看 systemctl status drama-backend）"
ok "/ready 通"

echo "==> 公网 HTTPS 探活..."
curl -fsS "${API_BASE}/health" && echo

# ---------- 6. 删除接口冒烟（可选） ----------
if [[ -n "$ADMIN_TOKEN" && -n "$DRAFT_ID" ]]; then
  echo "==> 删除接口冒烟：DELETE ${API_BASE}/v1/admin/dramas/${DRAFT_ID}"
  HTTP_CODE=$(curl -sS -o /tmp/delete-resp.json -w "%{http_code}" \
    -X DELETE "${API_BASE}/v1/admin/dramas/${DRAFT_ID}" \
    -H "Authorization: Bearer ${ADMIN_TOKEN}")
  cat /tmp/delete-resp.json; echo
  case "$HTTP_CODE" in
    200) ok "删除成功（接口可用）" ;;
    401) err "401 未登录：token 无效/过期，重新走 /v1/admin/auth/login" ;;
    403) err "403 无权限：当前 admin 角色不能删，请确认是 AdminRoleOps/Auditor 之一" ;;
    404) err "404 剧集不存在（id=${DRAFT_ID}），换一个 draft 状态的 id 再试" ;;
    409) err "409 仅草稿可删：当前剧不是 draft 状态（先 /v1/admin/dramas/:id/offline + 改 status=draft）" ;;
    *)   err "HTTP ${HTTP_CODE}：未知错误，贴日志到控制台" ;;
  esac
else
  warn "跳过删除冒烟（未设 ADMIN_TOKEN / DRAFT_ID）"
fi

echo
ok "全部完成"
echo "回滚（如有需要）："
echo "  ssh -i ${SSH_KEY_E} ${SSH_USER}@${SSH_HOST} 'sudo ls -lt ${REMOTE_DIR}/drama-api.bak.* | head -3'"
echo "  ssh -i ${SSH_KEY_E} ${SSH_USER}@${SSH_HOST} \"sudo mv -f ${REMOTE_DIR}/drama-api.bak.<ts> ${REMOTE_DIR}/drama-api && sudo systemctl reload ${SERVICE}\""
