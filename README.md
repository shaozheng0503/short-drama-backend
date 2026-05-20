# DramaBackend — MVP（含阶段二内容/互动）

短剧 APP MVP 后端，基于 Golang + Gin + PostgreSQL + GORM。
按《MVP项目开发执行文档.md》推进，目前覆盖：

- **Phase 1**：基础工程、三类身份登录与 `/me`、短信验证码。
- **Phase 2**（本轮新增）：APP 内容只读 / 互动接口、播放地址（暂无云点播签名）、观看历史、点赞 / 收藏 / 分享；管理中台分类 CRUD、短剧 CRUD + 上下架、剧集 CRUD。

**仍未实现**：云点播签名 / 回调、支付下单 / 回调 / 解锁、创作者数据汇总与提现、合同。

---

## 一、接口总览

### 1.1 通用

| 接口 | 鉴权 | 说明 |
|---|---|---|
| `GET /health` | 公开 | 健康检查 |
| `POST /v1/common/sms/send` | 公开 | 发送短信验证码（scene=login/creator_login），dev 模式回显 `dev_code` |

### 1.2 APP 端

| 接口 | 鉴权 | 说明 |
|---|---|---|
| `POST /v1/app/auth/login` | 公开 | 手机号 + 验证码，自动注册 |
| `GET /v1/app/me` | APP JWT | 当前用户 |
| `PUT /v1/app/me` | APP JWT | 更新昵称 / 头像 |
| `GET /v1/app/home` | 公开 | 首页（分类 / 推荐 / 热门） |
| `GET /v1/app/dramas` | 公开 | 短剧列表（category_id、sort=hot/new、分页） |
| `GET /v1/app/dramas/:id` | 公开（带 token 时扩展） | 短剧详情，登录后返回点赞、收藏、最近观看 |
| `GET /v1/app/dramas/:id/episodes` | 公开（带 token 时扩展） | 剧集列表，返回 is_free / is_locked |
| `GET /v1/app/search?q=...` | 公开 | 搜索短剧（title / description 模糊） |
| `GET /v1/app/episodes/:id/play` | APP JWT | 播放地址；付费集未解锁返回 42001 |
| `POST /v1/app/play-history` | APP JWT | 上报观看进度 |
| `GET /v1/app/play-history` | APP JWT | 观看历史（分页） |
| `POST/DELETE /v1/app/dramas/:id/like` | APP JWT | 点赞 / 取消 |
| `POST/DELETE /v1/app/dramas/:id/favorite` | APP JWT | 收藏 / 取消 |
| `GET /v1/app/me/favorites` | APP JWT | 我的收藏列表 |
| `POST /v1/app/dramas/:id/share` | APP JWT | 分享埋点 |

### 1.3 创作者端

| 接口 | 鉴权 | 说明 |
|---|---|---|
| `POST /v1/creator/auth/login` | 公开 | 手机号 + 验证码，自动注册 |
| `GET /v1/creator/me` | Creator JWT | 当前创作者基础信息 |

### 1.4 管理中台

| 接口 | 鉴权 | 说明 |
|---|---|---|
| `POST /v1/admin/auth/login` | 公开 | 账号密码登录 |
| `GET /v1/admin/me` | Admin JWT | 当前管理员 |
| `GET / POST /v1/admin/categories` | Admin JWT | 分类列表 / 创建 |
| `PUT /v1/admin/categories/:id` | Admin JWT | 分类更新 |
| `GET /v1/admin/dramas` | Admin JWT | 短剧列表（status / keyword / category_id / creator_id 筛选） |
| `POST /v1/admin/dramas` | Admin JWT | 创建短剧（默认 status=draft） |
| `GET /v1/admin/dramas/:id` | Admin JWT | 短剧详情（含剧集概览） |
| `PUT /v1/admin/dramas/:id` | Admin JWT | 更新短剧 |
| `POST /v1/admin/dramas/:id/publish` | Admin JWT | 上架（至少 1 集 ready） |
| `POST /v1/admin/dramas/:id/offline` | Admin JWT | 下架 |
| `GET / POST /v1/admin/dramas/:id/episodes` | Admin JWT | 剧集列表 / 创建 |
| `PUT /v1/admin/episodes/:id` | Admin JWT | 剧集更新（标题、vod_file_id、video_url、duration、status） |

---

## 二、本地启动

```bash
# 启动 Postgres（docker-compose 用 postgres/postgres；或者直接用本地 brew PG）
docker compose up -d

# 把 .env.example 拷成 .env 按需修改；.env 已在 .gitignore 中
cp .env.example .env

# 加载到当前 shell 后再启动
set -a; source .env; set +a
go run ./cmd/api
```

⚠️ `.env` 永远**不要** commit，腾讯云 / 微信 / 支付宝密钥都只能写在这里或运行环境变量里。
首次启动会 AutoMigrate 全部表，并创建初始管理员 `admin / admin123`（可通过 `ADMIN_INIT_*` 覆盖）。

---

## 三、Apifox 自测顺序

> 管理中台 → 给短剧 / 剧集铺数据 → APP 端浏览 / 播放

### 3.1 准备测试内容

1. `POST /v1/admin/auth/login` 拿 `admin-jwt`
2. `POST /v1/admin/categories` 建几个分类
3. `POST /v1/admin/dramas` 创建短剧（拿到 `drama_id`）
4. `POST /v1/admin/dramas/:id/episodes` 创建剧集
   - 想让前端能播放：`video_url` 填一个能直接播放的 m3u8/mp4，`status` 不填会自动判定为 `ready`
5. `POST /v1/admin/dramas/:id/publish` 上架

### 3.2 APP 浏览

1. `POST /v1/common/sms/send` `{phone,scene:"login"}`
2. `POST /v1/app/auth/login` 拿 `app-jwt`
3. `GET /v1/app/home`
4. `GET /v1/app/dramas`
5. `GET /v1/app/dramas/:id`、`/episodes`
6. `GET /v1/app/search?q=xx`
7. `GET /v1/app/episodes/:id/play` 拿到 `play_url`
8. `POST /v1/app/play-history` 上报进度
9. `POST /v1/app/dramas/:id/like`、`favorite`

### 3.3 验证业务规则

- 上架前没有 ready 剧集：`publish` 返回 40001
- 同短剧同集号重复创建：返回 40901
- 付费集播放（episode_no > free_episodes 且未解锁）：返回 42001 + `{need_unlock,price_cents}`
- 短信 60 秒内重发：返回 40901
- APP token 调用 `/v1/admin/*`：返回 40301

---

## 四、错误码

| code | 含义 |
|---:|---|
| 0 | 成功 |
| 40001 | 参数错误 / 验证码错误 |
| 40101 | 未登录或 token 无效 |
| 40301 | 无权限 / 身份与接口不匹配 |
| 40401 | 资源不存在 |
| 40901 | 重复操作 |
| 42001 | 剧集未解锁 |
| 50001 | 服务端错误 |

所有响应统一是 `{ "code": int, "message": string, "data": ... }`，HTTP 状态正常情况下 200。

---

## 五、目录结构

```
cmd/api/main.go                 入口
internal/config/                环境变量
internal/database/              连接 + AutoMigrate + 初始管理员
internal/middleware/auth.go     JWT 鉴权（app/creator/admin + 软鉴权 TryAppUserID）
internal/model/                 全部 ORM 模型
internal/response/              统一响应 + 错误码
internal/sms/                   短信验证码（dev 模式）
internal/handler/
├── server.go                   路由注册
├── common.go                   短信 / 健康
├── app.go                      APP 登录 / me
├── app_content.go              APP 首页 / 列表 / 详情 / 搜索
├── app_play.go                 播放地址
├── app_history.go              观看历史
├── app_action.go               点赞 / 收藏 / 分享
├── creator.go                  创作者登录 / me
├── admin.go                    管理员登录 / me
├── admin_category.go           分类 CRUD
├── admin_drama.go              短剧 CRUD + publish/offline
├── admin_episode.go            剧集 CRUD
└── util.go                     分页、view、helper
```

---

## 六、腾讯云短信切换路径

短信模块默认走 `DevProvider`（仅写库 + 日志），生产前需要切换到 `TencentProvider`。**代码里只留了接入位**，真实发短信还差两件事：

### 6.1 腾讯云控制台准备

1. **CAM 子用户**已有，密钥从 [console.cloud.tencent.com/cam/capi](https://console.cloud.tencent.com/cam/capi) 创建后只显示一次，丢了只能重建。
2. **签名管理**：进入「短信 → 国内/海外 → 正文签名管理」，确认要用的签名已审核通过。⚠️ 营业执照主体名要和签名文案完全一致（之前内部记录里出现过「共绩」/「共臻」一字之差，上线前务必核对）。
3. **模板管理**：现有模板都是「业务通知」类目，**不能用于登录验证码**。要在「正文模板管理」新建：
   - 类目：验证码
   - 内容示例：`您的验证码是 {1}，{2} 分钟内有效，请勿向他人泄露。`
   - 等审核通过（通常几小时～1 天），拿 `TemplateID`。

### 6.2 配置切换

把 `.env` 里这些字段填齐，**然后再** `SMS_DEV_MODE=false`：

```bash
TENCENT_SECRET_ID=...
TENCENT_SECRET_KEY=...
TENCENT_REGION=ap-guangzhou
TENCENT_SMS_SDK_APP_ID=...
TENCENT_SMS_SIGN_NAME=...
TENCENT_SMS_TEMPLATE_LOGIN=...
SMS_DEV_MODE=false
```

启动时会日志输出 `[sms] provider=tencent dev_mode=false`。
如果上面任何一项空着，会自动 **fallback 到 DevProvider** 并打 warning，前端拿不到 `dev_code`，但后端日志能看到验证码，方便排查。

### 6.3 代码接入位

`internal/sms/tencent_provider.go` 的 `Send()` 当前是 stub，注释里已经写了完整的 SDK 调用模板。真接入只需两步：

1. `go get github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/sms/v20210111`
2. 把 `Send()` 里的 `log.Printf + return ErrProviderUnavailable` 替换成注释里那段调用代码。

---

## 七、阶段三计划

按执行文档第 3 周内容推进：

1. 商品 / 订单 / 微信 + 支付宝下单 + 回调。
2. 剧集解锁接口（写入 `episode_unlocks`）。
3. 创作者收益分账（`creator_stats_daily` + `creators.balance_cents`）。
4. 提现申请 + 管理中台审核。
5. 合同列表 / 创建（电子签可后置）。
6. 云点播上传签名 + 回调（剧集 `status`、`video_url` 由回调写入）。
