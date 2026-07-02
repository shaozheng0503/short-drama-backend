#!/bin/bash
# DramaBackend 一键推送到 GitHub
# 用法：
#   1. 第一次先创建 PAT 文件（只需一次）：
#      echo "ghp_你的token" > ~/.github_pat && chmod 600 ~/.github_pat
#   2. 跑这个脚本：
#      ./push-to-github.sh

set -euo pipefail

REPO_DIR="/Users/Admin/Downloads/短剧/DramaBackend"
PAT_FILE="$HOME/.github_pat"
GITHUB_USER="Gongji-hub"
GITHUB_REPO="DramaBackend"

# 1. 检查 PAT 文件
if [ ! -f "$PAT_FILE" ]; then
  echo "❌ 找不到 $PAT_FILE"
  echo ""
  echo "  请先创建 PAT 文件（一次性操作）："
  echo "    1. 打开 https://github.com/settings/tokens"
  echo "    2. 点 'Generate new token' → 选 'Tokens (classic)'"
  echo "    3. 勾选 public_repo 权限 → 生成"
  echo "    4. 复制 token（ghp_xxxx 格式，只显示一次！）"
  echo "    5. 跑："
  echo "       echo 'ghp_你的token' > $PAT_FILE"
  echo "       chmod 600 $PAT_FILE"
  exit 1
fi

PAT=$(cat "$PAT_FILE")
if [ -z "$PAT" ]; then
  echo "❌ $PAT_FILE 是空的，请写入你的 PAT"
  exit 1
fi

# 2. 切到仓库
cd "$REPO_DIR"

# 3. 检查本地待 push commit
PENDING=$(git log --oneline origin/main..HEAD 2>/dev/null | wc -l | tr -d ' ')
echo "📊 待 push 的本地 commit：$PENDING 个"
if [ "$PENDING" -eq 0 ]; then
  echo "✅ 已是最新，无需 push"
  exit 0
fi

# 4. 列出待 push commit
echo ""
echo "  待 push commit 列表："
git log --oneline origin/main..HEAD | sed 's/^/    /'

# 5. push
echo ""
echo "🚀 推送中..."
PUSH_URL="https://${GITHUB_USER}:${PAT}@github.com/${GITHUB_USER}/${GITHUB_REPO}.git"
if git push "$PUSH_URL" main 2>&1 | tail -20; then
  echo ""
  echo "✅ push 成功！"
  echo "🔗 https://github.com/${GITHUB_USER}/${GITHUB_REPO}/commits/main"
else
  echo ""
  echo "❌ push 失败，详见上面错误"
  exit 1
fi
