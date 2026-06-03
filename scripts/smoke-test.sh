#!/usr/bin/env bash
#
# smoke-test.sh —— APP 评论社交 / 消息页 / 创作者封面上传 的端到端冒烟测试。
#
# 覆盖（2026-06 上线批次）：
#   · 评论点赞       POST/DELETE /app/comments/{id}/like
#   · 楼中楼回复      POST /app/dramas/{id}/comments(parent_id) + GET /app/comments/{id}/replies
#   · 评论列表        GET /app/dramas/{id}/comments（顶层 + like_count/reply_count/liked）
#   · 消息页          GET /app/messages + read-all + {id}/read（comment_reply / comment_like 聚合）
#   · 本集评论数      first_episode / 剧集列表 comment_count
#   · 封面规格        GET /creator/config/cover-specs + image-sign 接受 bmp
#
# 用法：
#   ./scripts/smoke-test.sh                         # 默认打 http://localhost:18080/v1
#   BASE_URL=http://localhost:18080/v1 ./scripts/smoke-test.sh
#   ./scripts/smoke-test.sh http://localhost:18080/v1
#   在服务器上跑：ssh ... 'bash -s' < scripts/smoke-test.sh
#
# 依赖：curl、jq。需服务端 SMS_DEV_MODE=true（登录走 dev_code）且存在至少一部已发布短剧。
# 注意：会创建少量测试数据（评论/点赞/消息/测试用户，内容带 [smoke<ts>] 标记），演示前可清理。
#
# 退出码：全部通过=0，有失败=1。
set -uo pipefail

BASE_URL="${1:-${BASE_URL:-http://localhost:18080/v1}}"
CREATOR_PHONE="${CREATOR_PHONE:-13800000001}"
TS=$(date +%s)
# 默认每次跑用 TS 派生的全新 APP 手机号（139+7位+序号=11位），避免跨次运行累积旧消息影响断言。
PSUF=$(printf "%07d" $((TS % 10000000)))
APP_PHONE_A="${APP_PHONE_A:-139${PSUF}1}"
APP_PHONE_B="${APP_PHONE_B:-139${PSUF}2}"
APP_PHONE_D="${APP_PHONE_D:-139${PSUF}3}"
PASS=0; FAIL=0
B="$BASE_URL"

command -v jq >/dev/null 2>&1 || { echo "需要 jq"; exit 2; }

chk(){ if [ "$2" = "$3" ]; then echo "  ✓ $1 = $3"; PASS=$((PASS+1)); else echo "  ✗ $1  期望[$2] 实得[$3]"; FAIL=$((FAIL+1)); fi; }
H(){ echo "Authorization: Bearer $1"; }

# dev 模式登录：send sms 取 dev_code → login 换 token
applogin(){ local p="$1" code
  code=$(curl -sS -m5 -X POST "$B/common/sms/send" -H 'Content-Type: application/json' -d "{\"phone\":\"$p\",\"scene\":\"login\"}" | jq -r '.data.dev_code')
  curl -sS -m5 -X POST "$B/app/auth/login" -H 'Content-Type: application/json' -d "{\"phone\":\"$p\",\"code\":\"$code\"}" | jq -r '.data.token'
}
creatorlogin(){ local p="$1" code
  code=$(curl -sS -m5 -X POST "$B/common/sms/send" -H 'Content-Type: application/json' -d "{\"phone\":\"$p\",\"scene\":\"creator_login\"}" | jq -r '.data.dev_code')
  curl -sS -m5 -X POST "$B/creator/auth/login" -H 'Content-Type: application/json' -d "{\"phone\":\"$p\",\"code\":\"$code\"}" | jq -r '.data.token'
}

echo "== BASE_URL = $B =="

echo "############ 创作者：封面上传 ############"
CT=$(creatorlogin "$CREATOR_PHONE")
[ -n "$CT" ] && [ "$CT" != null ] || { echo "creator 登录失败（确认 SMS_DEV_MODE=true）"; exit 2; }
SPEC=$(curl -sS "$B/creator/config/cover-specs" -H "$(H "$CT")")
chk "cover-specs max_count"  5      "$(echo "$SPEC"|jq -r '.data.max_count')"
chk "cover-specs 档数"       2      "$(echo "$SPEC"|jq -r '.data.specs|length')"
chk "spec0 ratio"           7:10    "$(echo "$SPEC"|jq -r '.data.specs[0].ratio')"
chk "spec0 含bmp"           true    "$(echo "$SPEC"|jq -r '.data.specs[0].formats|index("bmp")!=null')"
chk "spec1 ratio"           2:3     "$(echo "$SPEC"|jq -r '.data.specs[1].ratio')"
chk "image-sign bmp 放行"    0      "$(curl -sS -X POST "$B/creator/uploads/image-sign" -H "$(H "$CT")" -H 'Content-Type: application/json' -d '{"scene":"cover","ext":"bmp"}'|jq -r '.code')"
chk "image-sign tiff 拒绝"  40001   "$(curl -sS -X POST "$B/creator/uploads/image-sign" -H "$(H "$CT")" -H 'Content-Type: application/json' -d '{"scene":"cover","ext":"tiff"}'|jq -r '.code')"
chk "cover-specs 无token"   40101   "$(curl -sS "$B/creator/config/cover-specs"|jq -r '.code')"

echo "############ 发现一部已发布短剧 + 一个剧集 ############"
DRAMA="${DRAMA_ID:-$(curl -sS "$B/app/dramas?page=1&page_size=20" | jq -r '.data.list[0].id // empty')}"
[ -n "$DRAMA" ] || { echo "找不到已发布短剧，无法测评论链路"; echo "PASS=$PASS FAIL=$FAIL"; exit 1; }
EP="${EPISODE_ID:-$(curl -sS "$B/app/dramas/$DRAMA/episodes" | jq -r '.data.list[0].id // empty')}"
[ -n "$EP" ] || { echo "短剧 $DRAMA 无剧集"; echo "PASS=$PASS FAIL=$FAIL"; exit 1; }
echo "  drama=$DRAMA episode=$EP"

echo "############ 登录 3 个 APP 用户 ############"
TA=$(applogin "$APP_PHONE_A"); TB=$(applogin "$APP_PHONE_B"); TD=$(applogin "$APP_PHONE_D")
UA=$(curl -sS "$B/app/me" -H "$(H "$TA")"|jq -r '.data.id'); UB=$(curl -sS "$B/app/me" -H "$(H "$TB")"|jq -r '.data.id'); UD=$(curl -sS "$B/app/me" -H "$(H "$TD")"|jq -r '.data.id')
chk "APP 登录拿到 token" true "$([ -n "$TA" ] && [ "$TA" != null ] && echo true || echo false)"
echo "  A=$UA B=$UB D=$UD"

CNT0=$(curl -sS "$B/app/dramas/$DRAMA/episodes" -H "$(H "$TA")"|jq -r ".data.list[]|select(.id==$EP)|.comment_count")
echo "  本集 comment_count 基线 = $CNT0"

echo "############ 评论 + 楼中楼 ############"
C=$(curl -sS -X POST "$B/app/dramas/$DRAMA/comments" -H "$(H "$TA")" -H 'Content-Type: application/json' -d "{\"content\":\"[smoke$TS]A顶层\",\"episode_id\":$EP}")
CID=$(echo "$C"|jq -r '.data.id')
chk "发顶层 parent_id=null" null "$(echo "$C"|jq -r '.data.parent_id')"
chk "发顶层 like_count=0"   0    "$(echo "$C"|jq -r '.data.like_count')"
chk "B看列表 liked=false" false "$(curl -sS "$B/app/dramas/$DRAMA/comments?episode_id=$EP" -H "$(H "$TB")"|jq -r ".data.list[]|select(.id==$CID)|.liked")"
R1=$(curl -sS -X POST "$B/app/dramas/$DRAMA/comments" -H "$(H "$TB")" -H 'Content-Type: application/json' -d "{\"content\":\"[smoke$TS]B回A\",\"parent_id\":$CID}")
R1ID=$(echo "$R1"|jq -r '.data.id')
chk "B回顶层 parent=C"      "$CID" "$(echo "$R1"|jq -r '.data.parent_id')"
chk "B回顶层 reply_to=null" null   "$(echo "$R1"|jq -r '.data.reply_to_user')"
R2=$(curl -sS -X POST "$B/app/dramas/$DRAMA/comments" -H "$(H "$TA")" -H 'Content-Type: application/json' -d "{\"content\":\"[smoke$TS]A回B\",\"parent_id\":$R1ID}")
chk "回复的回复 拍平parent=C" "$CID" "$(echo "$R2"|jq -r '.data.parent_id')"
chk "回复的回复 @B"          "$UB"  "$(echo "$R2"|jq -r '.data.reply_to_user.id')"
chk "顶层 reply_count=2"     2     "$(curl -sS "$B/app/dramas/$DRAMA/comments?episode_id=$EP" -H "$(H "$TA")"|jq -r ".data.list[]|select(.id==$CID)|.reply_count")"
chk "回复列表条数=2"         2     "$(curl -sS "$B/app/comments/$CID/replies" -H "$(H "$TA")"|jq -r '.data.list|length')"

echo "############ 评论点赞（含聚合） ############"
chk "B点赞 like_count=1" 1 "$(curl -sS -X POST "$B/app/comments/$CID/like" -H "$(H "$TB")"|jq -r '.data.like_count')"
chk "D点赞 like_count=2" 2 "$(curl -sS -X POST "$B/app/comments/$CID/like" -H "$(H "$TD")"|jq -r '.data.like_count')"
chk "B看 liked=true"  true "$(curl -sS "$B/app/dramas/$DRAMA/comments?episode_id=$EP" -H "$(H "$TB")"|jq -r ".data.list[]|select(.id==$CID)|.liked")"

echo "############ 消息页 ############"
MA=$(curl -sS "$B/app/messages" -H "$(H "$TA")")
chk "A未读数=2" 2 "$(echo "$MA"|jq -r '.data.unread_count')"
chk "A有reply消息" true "$(echo "$MA"|jq -r '[.data.list[]|select(.type=="comment_reply")]|length>=1')"
LK=$(echo "$MA"|jq -c ".data.list[]|select(.type==\"comment_like\" and .target_comment.id==$CID)")
chk "A点赞消息 like_count=2" 2 "$(echo "$LK"|jq -r '.like_count')"
chk "A点赞消息 聚合2人"     2 "$(echo "$LK"|jq -r '.recent_actors|length')"
chk "B收到A的回复消息" "$UA" "$(curl -sS "$B/app/messages" -H "$(H "$TB")"|jq -r '[.data.list[]|select(.type=="comment_reply")][0].actor.id')"
chk "类型筛选 only like" true "$(curl -sS "$B/app/messages?type=comment_like" -H "$(H "$TA")"|jq -r '[.data.list[].type]|all(.=="comment_like")')"
MID=$(echo "$MA"|jq -r '.data.list[0].id')
chk "单条已读" true "$(curl -sS -X POST "$B/app/messages/$MID/read" -H "$(H "$TA")"|jq -r '.data.is_read')"
curl -sS -X POST "$B/app/messages/read-all" -H "$(H "$TA")" >/dev/null
chk "全部已读后 unread=0" 0 "$(curl -sS "$B/app/messages" -H "$(H "$TA")"|jq -r '.data.unread_count')"

echo "############ 本集评论数应 +3 ############"
CNT1=$(curl -sS "$B/app/dramas/$DRAMA/episodes" -H "$(H "$TA")"|jq -r ".data.list[]|select(.id==$EP)|.comment_count")
chk "comment_count 基线+3" "$((CNT0+3))" "$CNT1"

echo "############ 取消点赞 + 边界 ############"
chk "B取消点赞 like_count=1" 1 "$(curl -sS -X DELETE "$B/app/comments/$CID/like" -H "$(H "$TB")"|jq -r '.data.like_count')"
chk "对回复发replies 400" 40001 "$(curl -sS "$B/app/comments/$R1ID/replies" -H "$(H "$TA")"|jq -r '.code')"
chk "回复不存在评论 404" 40401 "$(curl -sS -X POST "$B/app/dramas/$DRAMA/comments" -H "$(H "$TA")" -H 'Content-Type: application/json' -d '{"content":"x","parent_id":999999999}'|jq -r '.code')"
chk "点赞不存在评论 404" 40401 "$(curl -sS -X POST "$B/app/comments/999999999/like" -H "$(H "$TA")"|jq -r '.code')"

echo "################################################"
echo "##  结果：PASS=$PASS  FAIL=$FAIL"
echo "################################################"
[ "$FAIL" -eq 0 ]
