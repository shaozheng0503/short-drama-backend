# scripts

运维 / 联调脚本。

## smoke-test.sh

APP 评论社交（点赞 + 楼中楼）、消息页、创作者封面上传的端到端冒烟测试，35 项断言，输出 ✓/✗，全过退出 0、有失败退出 1。

```bash
# 本地/默认（打 localhost:18080）
./scripts/smoke-test.sh

# 指定环境
BASE_URL=http://localhost:18080/v1 ./scripts/smoke-test.sh
./scripts/smoke-test.sh http://localhost:18080/v1

# 在服务器上跑（不落文件）
ssh -p <port> -i <key> root@<host> 'bash -s' < scripts/smoke-test.sh
```

**前置**：`curl`、`jq`；服务端 `SMS_DEV_MODE=true`（登录走回显的 `dev_code`）；至少一部已发布短剧（脚本自动发现，或用 `DRAMA_ID` / `EPISODE_ID` 指定）。

**可调环境变量**：`BASE_URL`、`CREATOR_PHONE`、`APP_PHONE_A/B/D`、`DRAMA_ID`、`EPISODE_ID`。

> ⚠️ 脚本会创建少量测试数据（评论/点赞/消息/测试用户，内容带 `[smoke<ts>]` 标记）。仅在测试/联调环境跑，演示前可清理。
