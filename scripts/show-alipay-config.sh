#!/usr/bin/env bash
#
# show-alipay-config.sh —— 支付宝渠道配置自检。
#
# 用途：
#   联调前跑一次，看 .env 里 alipay 相关配置缺什么、对不对，
#   对照清单补完再起服务。
#
# 用法：
#   ./scripts/show-alipay-config.sh                       # 读 ./env/.env
#   ENV_FILE=/path/to/.env ./scripts/show-alipay-config.sh
#   ./scripts/show-alipay-config.sh /path/to/.env
#
# 检查项(与代码 internal/payment/provider.go + alipay_provider.go 一一对应)：
#   1. ALIPAY_APP_ID       —— 沙箱应用 APPID
#   2. ALIPAY_PRIVATE_KEY  —— 应用私钥 RSA2(PEM 整段，含 BEGIN/END)
#   3. ALIPAY_PUBLIC_KEY   —— 支付宝公钥(PEM 整段，验签用)
#   4. ALIPAY_SANDBOX      —— true=沙箱网关，false=生产
#   5. ALIPAY_NOTIFY_URL   —— 公网可达的异步通知地址
#   6. PAYMENT_DEV_MODE    —— 切真 provider 的总开关(必须 false)
#
# 退出码：全部就绪=0，有缺/有问题=1。
#
# 不打印真实密钥内容，只显示长度 + 前 6 字符 + 是否有 PEM 头尾。
set -uo pipefail

# 取 ENV_FILE：优先位置参数 > ENV_FILE 环境变量 > 默认 ./.env
# 用 if 不用 ${1:-${ENV_FILE:-}}，绕开 set -u 下嵌套默认值的解析坑
if [[ -n "${1:-}" ]]; then
  ENV_FILE="$1"
elif [[ -n "${ENV_FILE:-}" ]]; then
  ENV_FILE="$ENV_FILE"
else
  ENV_FILE="./.env"
fi
if [[ ! -f "$ENV_FILE" ]]; then
  echo "✗ 找不到 $ENV_FILE"
  echo "  从 .env.example 复制：cp .env.example .env"
  exit 1
fi

# 把 .env 读进环境(兼容带引号 / 含空格的 DSN 值)。
set -a
# shellcheck disable=SC1090
source "$ENV_FILE"
set +a

# 状态字符
OK="✓"
NO="✗"
WARN="⚠"

# 通用：判断值是否"非空且非占位"。
is_set() {
  local v="${1:-}"
  [[ -n "$v" && "$v" != "change-me" && "$v" != "your-key-here" ]]
}

# 取值并打印摘要：长度 + 前 6 字符(密钥不外泄)
fingerprint() {
  local v="$1"
  local n=${#v}
  if (( n <= 8 )); then
    echo "${n}B 全部"
  else
    local head="${v:0:6}"
    echo "${n}B 头=${head}…"
  fi
}

# 检查 PEM 格式
# 支付宝后台返的公钥常常是裸 base64(没 BEGIN/END 头尾),
# 所以格式检查兼容两种:有 PEM 头 → 完美;裸 base64 且长度合理 → 也 OK。
check_pem() {
  local v="$1"
  local label="$2"
  local stripped="${v// /}"  # 去空白
  if [[ "$v" == *"BEGIN "*"PRIVATE KEY"* && "$v" == *"END "*"PRIVATE KEY"* ]]; then
    echo "PEM 格式正确(PRIVATE KEY 头)"
  elif [[ "$v" == *"BEGIN "*"PUBLIC KEY"* && "$v" == *"END "*"PUBLIC KEY"* ]]; then
    echo "PEM 格式正确(PUBLIC KEY 头)"
  elif [[ "$v" == *"BEGIN "*"RSA "* && "$v" == *"END "*"RSA "* ]]; then
    echo "PEM 格式正确(RSA 头)"
  elif [[ ${#stripped} -ge 200 && "$stripped" =~ ^[A-Za-z0-9+/=]+$ ]]; then
    # 裸 base64,且长度 ≥200 字节(2048-bit RSA 公/私钥序列化后大约 216-452 字节)
    if [[ "$label" == "私钥" ]]; then
      echo "裸 base64(2048-bit RSA 私钥长度合理)"
    else
      echo "裸 base64(2048-bit RSA 公钥长度合理,支付宝后台常见格式)"
    fi
  else
    echo "$WARN 格式异常:既不是 PEM 也不是合理长度的裸 base64——确认从后台复制完整"
  fi
}

# --- 累加失败项 ---
MISSING=()
WARNINGS=()

echo "=== 支付宝配置自检(DramaBackend: $ENV_FILE)==="
echo

# 1) PAYMENT_DEV_MODE
echo "[总开关]"
if is_set "${PAYMENT_DEV_MODE:-}"; then
  if [[ "${PAYMENT_DEV_MODE}" == "true" ]]; then
    echo "$OK PAYMENT_DEV_MODE=true"
    echo "  当前两渠道(wechat/alipay)都走 DevProvider stub。"
    echo "  $WARN 沙箱联调 alipay 前必须切 false，否则配齐密钥也不生效。"
    WARNINGS+=("PAYMENT_DEV_MODE=true：alipay 真 provider 不启用")
  else
    echo "$OK PAYMENT_DEV_MODE=false(真 provider 启用)"
  fi
else
  echo "$WARN PAYMENT_DEV_MODE 未设置，默认 true(DevProvider 模式)"
  WARNINGS+=("PAYMENT_DEV_MODE 未显式设置")
fi
echo

# 2) ALIPAY_APP_ID
echo "[alipay 渠道]"
if is_set "${ALIPAY_APP_ID:-}"; then
  echo "$OK ALIPAY_APP_ID=$(fingerprint "$ALIPAY_APP_ID")"
else
  echo "$NO ALIPAY_APP_ID=空"
  echo "  来源：open.alipay.com → 沙箱应用 → 应用信息 → APPID"
  MISSING+=("ALIPAY_APP_ID")
fi

# 3) ALIPAY_PRIVATE_KEY
if is_set "${ALIPAY_PRIVATE_KEY:-}"; then
  echo "$OK ALIPAY_PRIVATE_KEY=$(fingerprint "$ALIPAY_PRIVATE_KEY")"
  PEM_NOTE=$(check_pem "$ALIPAY_PRIVATE_KEY" "私钥")
  if [[ "$PEM_NOTE" == "$WARN"* ]]; then
    echo "  $PEM_NOTE"
    WARNINGS+=("ALIPAY_PRIVATE_KEY PEM 头尾不全")
  else
    echo "  $PEM_NOTE"
  fi
else
  echo "$NO ALIPAY_PRIVATE_KEY=空"
  echo "  本地生成：openssl genrsa -out app_private_key.pem 2048"
  echo "  整段(含 BEGIN/END)贴入 .env，代码读 string 不读路径。"
  MISSING+=("ALIPAY_PRIVATE_KEY")
fi

# 4) ALIPAY_PUBLIC_KEY
if is_set "${ALIPAY_PUBLIC_KEY:-}"; then
  echo "$OK ALIPAY_PUBLIC_KEY=$(fingerprint "$ALIPAY_PUBLIC_KEY")"
  PEM_NOTE=$(check_pem "$ALIPAY_PUBLIC_KEY" "公钥")
  if [[ "$PEM_NOTE" == "$WARN"* ]]; then
    echo "  $PEM_NOTE"
    WARNINGS+=("ALIPAY_PUBLIC_KEY PEM 头尾不全")
  else
    echo "  $PEM_NOTE"
  fi
else
  echo "$NO ALIPAY_PUBLIC_KEY=空"
  echo "  来源：沙箱应用 → 应用公钥(上传你的应用公钥后)→ 下载返回的「支付宝公钥」"
  echo "  注意：填的是「支付宝公钥」，不是「应用公钥」——这是反直觉的"
  MISSING+=("ALIPAY_PUBLIC_KEY")
fi

# 5) ALIPAY_SANDBOX
if [[ -z "${ALIPAY_SANDBOX+x}" ]]; then
  echo "$WARN ALIPAY_SANDBOX 未设置"
  echo "  代码默认 true(沙箱网关 openapi.alipaydev.com)。生产部署必须显式 false。"
  WARNINGS+=("ALIPAY_SANDBOX 未显式设置")
elif [[ "$ALIPAY_SANDBOX" == "true" ]]; then
  echo "$OK ALIPAY_SANDBOX=true(沙箱网关 openapi.alipaydev.com)"
else
  echo "$WARN ALIPAY_SANDBOX=false(生产网关 openapi.alipay.com)"
  echo "  切生产前确认密钥 + notify URL 都已换成生产值。"
  WARNINGS+=("ALIPAY_SANDBOX=false：当前走生产网关")
fi

# 6) ALIPAY_NOTIFY_URL
if is_set "${ALIPAY_NOTIFY_URL:-}"; then
  if [[ "$ALIPAY_NOTIFY_URL" == https://* ]]; then
    echo "$OK ALIPAY_NOTIFY_URL=$ALIPAY_NOTIFY_URL"
  elif [[ "$ALIPAY_NOTIFY_URL" == http://localhost* || "$ALIPAY_NOTIFY_URL" == http://127.0.0.1* ]]; then
    echo "$WARN ALIPAY_NOTIFY_URL=$ALIPAY_NOTIFY_URL"
    echo "  沙箱/支付宝回调必须公网可达，本地地址收不到通知。"
    echo "  临时方案：ngrok http 18080 → 用 https://<隧道域名> 替换 localhost"
    WARNINGS+=("ALIPAY_NOTIFY_URL 仍指向本地")
  else
    echo "$WARN ALIPAY_NOTIFY_URL=$ALIPAY_NOTIFY_URL"
    echo "  非 https 沙箱可能可用，生产必须 https。"
    WARNINGS+=("ALIPAY_NOTIFY_URL 非 https")
  fi
else
  echo "$NO ALIPAY_NOTIFY_URL=空"
  echo "  公网回调地址，格式：https://<域名或 ngrok 隧道>/v1/webhooks/alipay/pay"
  echo "  本地无公网域：起 ngrok 隧道 → ngrok http 18080"
  MISSING+=("ALIPAY_NOTIFY_URL")
fi
echo

# 后台侧还要做的
echo "[支付宝沙箱后台侧 — 跟代码无关但联调前要确认]"
echo "  ☐ 接口加签方式 = 公钥模式(不是证书)—— 沙箱应用 → 应用信息"
echo "  ☐ 应用公钥已上传(你说已经有了 → 后台"应用公钥"那栏不是空)"
echo "  ☐ 沙箱账号 → 买家信息(拿一个买家账号 + 支付密码，沙箱 App 登录用)"
echo "  ☐ 可选(短剧用不到)：应用网关 / 异步通知接收地址 / 授权回调地址"
echo

# 总结
echo "[总结]"
if (( ${#MISSING[@]} == 0 )); then
  if (( ${#WARNINGS[@]} == 0 )); then
    echo "$OK 配置齐全，可以起服务跑联调。"
    echo "  PAYMENT_DEV_MODE=false 状态下：APP_ID+PRIVATE_KEY+PUBLIC_KEY 三项齐 → 启用 AlipayProvider 真 provider。"
    exit 0
  else
    echo "$WARN 必填项都齐了，但有 ${#WARNINGS[@]} 个提醒："
    for w in "${WARNINGS[@]}"; do echo "  - $w"; done
    exit 0
  fi
else
  echo "$NO 还差 ${#MISSING[@]} 项必填："
  for m in "${MISSING[@]}"; do echo "  - $m"; done
  if (( ${#WARNINGS[@]} > 0 )); then
    echo
    echo "另有 ${#WARNINGS[@]} 个提醒："
    for w in "${WARNINGS[@]}"; do echo "  - $w"; done
  fi
  echo
  echo "补完后再跑一次本脚本。补完前下单会被 ProviderRegistry 拒绝(503)。"
  exit 1
fi
