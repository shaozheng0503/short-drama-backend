#!/usr/bin/env bash
# DramaBackend 零停机部署 + 删除接口冒烟（支持 prod 和 sandbox 双环境）
# 目标机：43.143.212.37（同一台，跑两套：drama-backend + drama-backend-sandbox）
# 登录账号：ubuntu（普通账户，sudo 免密）
# 登录方式：SSH 密钥（~/.ssh/id_ed25519_drama_deploy）
#
# 使用方法：
#   ./scripts/deploy-prod.sh                       # 默认部署到生产（api.langzhi.top）
#   ENV=sandbox ./scripts/deploy-prod.sh           # 部署到沙箱（api-dev.langzhi.top）
#   ENV=both    ./scripts/deploy-prod.sh           # 同时部署两边（先沙箱后生产）
#
# 可选环境变量（全部可被命令行 ENV 覆盖）：
#   ENV                prod|sandbox|both  (默认 prod)
#   ADMIN_TOKEN        admin JWT，启用删除接口冒烟
#   DRAFT_ID           draft 状态剧集 id，配合 ADMIN_TOKEN 验证删除
#   SKIP_SMOKE=1       跳过删除冒烟
#   SKIP_GIT_PULL=1    跳过 git fetch（手动拉了代码再用）
#   SKIP_TEST=1        跳过 go test
#   SKIP_CHECKCFG=1    跳过 check-config
#
# 中间任何一步失败立即 exit，零破坏。

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# ======== 公共配置 ========
SSH_HOST="${SSH_HOST:-43.143.212.37}"
SSH_PORT="${SSH_PORT:-22}"
SSH_USER="${SSH_USER:-ubuntu}"
SSH_KEY="${SSH_KEY:-~/.ssh/id_ed25519_drama_deploy}"
SSH_KEY_E="${SSH_KEY/#\~/$HOME}"
[[ -f "$SSH_KEY_E" ]] || { echo "私钥不存在：$SSH_KEY_E" >&2; exit 1; }

# ======== 环境选择 ========
ENV="${ENV:-prod}"
case "$ENV" in
  prod)
    ENVS_TO_DEPLOY=("prod")
    ;;
  sandbox)
    ENVS_TO_DEPLOY=("sandbox")
    ;;
  both)
    ENVS_TO_DEPLOY=("sandbox" "prod")   # 先沙箱后生产（沙箱小风险先试）
    ;;
  *)
    echo "ENV 必须是 prod|sandbox|both，当前：$ENV" >&2; exit 1
    ;;
esac

# 每个环境的配置
get_env_config() {
  case "$1" in
    prod)
      REMOTE_DIR="/opt/drama-backend"
      SERVICE="drama-backend"
      API_BASE="https://api.langzhi.top"
      REMOTE_PORT="18080"
      ENV_LABEL="生产 (api.langzhi.top)"
      ;;
    sandbox)
      REMOTE_DIR="/opt/drama-backend-sandbox"
      SERVICE="drama-backend-sandbox"
      API_BASE="https://api-dev.langzhi.top"
      REMOTE_PORT="18090"
      ENV_LABEL="沙箱 (api-dev.langzhi.top)"
      ;;
    esac
  }

# ======== 颜色 ========
R='\033[0;31m'; G='\033[0;32m'; Y='\033[1;33m'; N='\033[0m'
ok()   { echo -e "${G}[OK]${N} $*"; }
warn() { echo -e "${Y}[WARN]${N} $*"; }
err()  { echo -e "${R}[ERR]${N} $*"; exit 1; }

SSH_OPTS=(-o StrictHostKeyChecking=accept-new -i "$SSH_KEY_E")
SSH=(ssh -p "$SSH_PORT" "${SSH_OPTS[@]}" "${SSH_USER}@${SSH_HOST}")
SCP_OPTS=(-o StrictHostKeyChecking=accept-new -i "$SSH_KEY_E")
SCP=(scp -P "$SSH_PORT" "${SCP_OPTS[@]}")

echo "==> 部署环境: ${ENV}（目标: ${ENVS_TO_DEPLOY[*]}）"

# ======== 预检（一次） ========
echo "==> SSH 连通性 + sudo 探活..."
"${SSH[@]}" 'whoami && sudo -n true && echo SUDO_OK' \
  | tee /tmp/_probe.log | grep -q SUDO_OK || { cat /tmp/_probe.log; err "SSH/sudo 失败"; }
ok "SSH + sudo 均可用"

# ======== 代码自查（一次） ========
if [[ "${SKIP_GIT_PULL:-0}" != "1" ]]; then
  echo "==> git 状态..."
  git status --short
  git fetch --all
  warn "提示：如需更新代码，请先手动 git pull --rebase，然后重跑本脚本"
fi

if [[ "${SKIP_TEST:-0}" != "1" ]]; then
  echo "==> go test ./..."
  go test ./... 2>&1 | tail -50
fi

if [[ "${SKIP_CHECKCFG:-0}" != "1" ]]; then
  echo "==> check-config --prod (本地，本地 .env 缺密钥是预期)..."
  go run ./cmd/check-config --prod 2>&1 | tail -20
fi

# ======== 编译一次 ========
echo "==> 交叉编译 linux/amd64 ..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags="-s -w" -o /tmp/drama-api ./cmd/api
ls -lh /tmp/drama-api
ok "编译完成"

# ======== 部署循环（先沙箱后生产 / 单一环境 / 沙箱后生产） ========
TS="$(date +%Y%m%d-%H%M%S)"
BACKUP_TS=()

for ENV_NAME in "${ENVS_TO_DEPLOY[@]}"; do
  get_env_config "$ENV_NAME"
  echo
  echo "================================================================"
  echo "==> 部署到: ${ENV_LABEL}"
  echo "    REMOTE_DIR=${REMOTE_DIR}  SERVICE=${SERVICE}  PORT=${REMOTE_PORT}"
  echo "================================================================"

  # 上传
  echo "==> 上传二进制到 /tmp/drama-api.new ..."
  "${SCP[@]}" /tmp/drama-api "${SSH_USER}@${SSH_HOST}:/tmp/drama-api.new"

  # 备份 + 替换 + reload
  echo "==> 备份 + 替换 + reload ..."
  BACKUP_TS+=("${TS}_${ENV_NAME}")
  "${SSH[@]}" sudo bash -s <<EOF
set -euo pipefail
if [[ -f ${REMOTE_DIR}/drama-api ]]; then
  cp -a ${REMOTE_DIR}/drama-api ${REMOTE_DIR}/drama-api.bak.${TS}_${ENV_NAME}
fi
mv -f /tmp/drama-api.new ${REMOTE_DIR}/drama-api
chown drama:drama ${REMOTE_DIR}/drama-api
chmod +x ${REMOTE_DIR}/drama-api
systemctl reload ${SERVICE}
sleep 1
systemctl show -p MainPID -p ActiveState -p NRestarts --value ${SERVICE}
EOF
  ok "reload 完成（${ENV_NAME}）"

  # /ready 探活（最多 30 秒：reload 后 tableflip 切进程，新进程需要 1-2 秒起来）
  # 注意：不能用 sudo bash -c "curl ..."，SSH 传参时会剥掉双引号导致 curl 丢失 URL
  #       必须把整个命令作为单引号字符串传给 SSH，让远程 shell 原样执行
  echo "==> /ready 探活（内网 127.0.0.1:${REMOTE_PORT}，最多 30s）..."
  READY=0
  for i in $(seq 1 15); do
    if "${SSH[@]}" "sudo curl -fsS http://127.0.0.1:${REMOTE_PORT}/ready" >/dev/null 2>&1; then
      ok "/ready 通（第 ${i} 次，≈ $((i*2))s）"
      READY=1
      break
    fi
    sleep 2
  done
  [[ "${READY:-0}" == "1" ]] || err "/ready 失败（先看 systemctl status ${SERVICE} && journalctl -u ${SERVICE} -n 50 --no-pager）"

  # 公网 /health
  echo "==> 公网 HTTPS 探活 ${API_BASE}/health ..."
  curl -fsS "${API_BASE}/health" && echo

  # 删除接口冒烟（只对生产做；沙箱跳过，因为 SMS 是 dev 模式拿不到真 token）
  if [[ "$ENV_NAME" == "prod" && "${SKIP_SMOKE:-0}" != "1" && -n "${ADMIN_TOKEN:-}" && -n "${DRAFT_ID:-}" ]]; then
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
    if [[ "$ENV_NAME" == "prod" ]]; then
      warn "跳过删除冒烟（未设 ADMIN_TOKEN / DRAFT_ID，或 SKIP_SMOKE=1）"
    else
      warn "跳过删除冒烟（沙箱 SMS 是 dev 模式，token 取不到）"
    fi
  fi
done

echo
ok "全部完成 (${ENV})"
echo "回滚（如有需要，按环境分别回滚）："
for i in "${!ENVS_TO_DEPLOY[@]}"; do
  ENV_NAME="${ENVS_TO_DEPLOY[$i]}"
  get_env_config "$ENV_NAME"
  BAK_TS="${BACKUP_TS[$i]}"
  echo "  # 回滚 ${ENV_NAME}（备份 .bak.${BAK_TS}）"
  echo "  ssh -i ${SSH_KEY_E} ${SSH_USER}@${SSH_HOST} \"sudo mv -f ${REMOTE_DIR}/drama-api.bak.${BAK_TS} ${REMOTE_DIR}/drama-api && sudo systemctl reload ${SERVICE}\""
  echo
done
