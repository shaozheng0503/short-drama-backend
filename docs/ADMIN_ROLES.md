# 管理后台角色权限边界

> 本文档由 `internal/handler/server.go` 的实际路由守卫推导而来，是角色权限的权威说明。
> 改了路由上的 `requireAdminRole(...)` 记得同步本文件。

## 1. 角色与初始账号

`admins.role` 取值（见 `internal/model/model.go`）：

| role | 名称 | 来源 | 初始密码 |
|---|---|---|---|
| `admin` | 超级管理员（放行一切） | 启动自动补齐 `admin` | `ADMIN_INIT_PASSWORD` |
| `finance` | 财务 | 启动自动补齐 `finance` | 同 `ADMIN_INIT_PASSWORD` |
| `auditor` | 审核 | 启动自动补齐 `auditor` | 同 `ADMIN_INIT_PASSWORD` |
| `region_admin` | 地区管理员 | **超管在后台手工创建**，无初始账号 | 创建时由超管指定 |

- 前三个账号由 `internal/database/database.go` 在启动时自动补齐（`ensureInitialAdmin` + `ensureRoleAdmins`），**幂等**：账号已存在则跳过，不覆盖已改过的密码。
- `finance` / `auditor` 的初始密码与 `admin` 同源，都取 `ADMIN_INIT_PASSWORD`（生产该值若为 `123456`，三个账号初始密码就都是 `123456`）。
- `region_admin` 不做种子补齐，完全由超管通过 `POST /v1/admin/admins` 创建（必须指定 `region`，精确到市，如 `安徽省蚌埠市`），支持账号密码 + 备注（`remark`，最长 255 字）。
- 登录入口统一 `POST /v1/admin/auth/login`，返回 JWT；之后请求带 `Authorization: Bearer <token>`。

## 2. 鉴权机制（怎么生效的）

请求经过四层（`server.go` 的 `adminAuth` 组）：

1. `RequireAdmin` —— 校验 JWT，subject 必须是 `admin`。
2. `requireActiveAdmin` —— 按 token 里的 id 查库，账号必须 `active`，并把 `role`、`region` 写进 context。
3. `restrictRegionAdmin` —— **地区管理员围栏**（见 §5），白名单之外的请求一律 403。
4. `requireAdminRole(允许的角色...)` —— **只挂在需要限权的具体动作上**：
   - 角色是 `admin`（超管）→ 放行一切；
   - 否则角色须命中允许列表，命中放行，不中 `403 当前角色无权执行该操作`；
   - **不传参 `requireAdminRole()` = 仅超管**（允许列表为空，非超管一律拒）。

> 注意：JWT 里不带 role，role/region 每次请求实时查库，所以改账号角色/地区/封禁即时生效，无需重新登录。

## 3. 权限边界表

### 审核（`auditor` 或超管）
| 方法 | 路径 | 功能 |
|---|---|---|
| POST | `/admin/dramas/:id/approve` | 短剧审核通过 |
| POST | `/admin/dramas/:id/reject` | 短剧审核驳回 |
| POST | `/admin/creators/:id/verification/approve` | 创作者实名认证通过 |
| POST | `/admin/creators/:id/verification/reject` | 创作者实名认证驳回 |

### 财务（`finance` 或超管）
| 方法 | 路径 | 功能 |
|---|---|---|
| POST | `/admin/withdrawals/:id/approve` | 提现审核通过 |
| POST | `/admin/withdrawals/:id/reject` | 提现驳回 |
| POST | `/admin/withdrawals/:id/mark-paid` | 标记已打款 |
| POST | `/admin/orders/:order_no/refund` | 订单退款 |
| POST | `/admin/orders/:order_no/sync` | 订单主动查单 / 对账 |
| GET | `/admin/finance/income/template.xlsx` | 下载收入导入模板 |
| GET | `/admin/finance/income/imports` | 收入导入批次列表 |
| GET | `/admin/finance/income/imports/:batch_no` | 收入导入批次详情 |
| POST | `/admin/finance/income/import` | 导入每日收入（Excel） |
| GET | `/admin/finance/channel-incomes` | 渠道收益列表 |
| PUT | `/admin/finance/channel-incomes/:id` | 修改渠道收益 |
| DELETE | `/admin/finance/channel-incomes/:id` | 删除渠道收益 |

### 仅超管（`admin`）
| 方法 | 路径 | 功能 |
|---|---|---|
| PUT | `/admin/config/pricing` | 全局定价配置 |
| PUT | `/admin/config/aigc-tools` | AIGC 工具配置 |
| PUT | `/admin/config/hot-search` | 热搜配置 |
| PUT | `/admin/config/income-share` | 分成比例配置 |
| POST/PUT/DELETE | `/admin/config/tax-brackets[/:id]` | 个税阶梯配置 |
| POST/PUT | `/admin/admins`（含创建/编辑 region_admin） | 管理员账号管理 |

### 任意在职管理员（无角色限制，admin/finance/auditor 可）
读类接口（dashboard、各类列表/详情、config 的 GET、合同模板下载等）以及**未单独挂角色守卫的写操作**，对 `admin`/`finance`/`auditor` 一律开放，例如：
- 短剧本体 CRUD：`POST/PUT/DELETE /admin/dramas`、`/publish`、`/offline`、剧集 CRUD
- 创作者管理：列表/新建/编辑/封禁解封（注意：实名认证的 approve/reject 才限 auditor）
- 用户封禁解封、评论删除、分类/语言/渠道账号 CRUD
- 合同 CRUD / 取消 / 电子签

> `region_admin` **不在**此列——它是默认全拒、白名单放行（见 §5）。

## 4. 当前粒度的局限（按需收紧）

这是**粗粒度**模型：只有"审核动作""资金动作""全局配置"三类敏感操作被单独限权，其余写操作对所有 admin 角色开放。也就是说当前 `auditor` / `finance` 账号**也能**新建/上下架短剧、封禁用户、删评论、改分类等。

会议结论（2026-06）是"其他面板暂不考虑"，故维持现状。若后续要严格隔离（审核员只能审、财务只能管钱、其余写操作收归超管），在对应路由补 `requireAdminRole(...)` 即可，无需改鉴权框架。

## 5. 地区管理员（`region_admin`）——默认全拒的白名单围栏

> 实现于 `internal/handler/auth_status.go`（`restrictRegionAdmin` + `regionAdminAllowedActions`），2026-08-25 上线。

### 5.1 定位

- 由超管按**市**创建（`region` 字段精确到市，如 `安徽省蚌埠市`），带账号密码 + 备注。
- **只读**：只能查看本地区创作者及其作品（短剧 + 剧集元信息），**看不到视频地址**（`video_url` / `vod_file_id` 置空）。
- **没有审核权限，也没有其他任何权限**（写操作、财务、配置、用户管理、合同、渠道、订单、提现、结算等全部 403）。

### 5.2 路由白名单（`regionAdminAllowedActions`）

| 方法+路径 | 功能 |
|---|---|
| POST | `/v1/admin/auth/refresh` | 刷新 token |
| GET | `/v1/admin/me` | 自己的信息 |
| GET | `/v1/admin/creators`（含 `/:id` 详情） | 本地区创作者列表/详情 |
| GET | `/v1/admin/dramas`（含 `/:id` 详情、`/:id/episodes`） | 本地区作品列表/详情/剧集 |

显式排除（即使前缀命中也拒）：`GET /v1/admin/creators/template.xlsx`（创作者导入模板，含全量字段，不开放）。

白名单之外的一切方法/路径（包括 POST 写操作、dashboard、admins 管理、finance、users、orders、withdrawals、contracts、config 等）一律 `403 {"code":40301,"message":"地区管理员仅可查看本地区创作者及其作品（只读）"}`。

### 5.3 数据范围

列表与详情双保险：

- `GET /v1/admin/creators`：强制 `WHERE region = '<本地区>'`（地域筛选参数被忽略，防止越权）。
- `GET /v1/admin/creators/:id`：非本地区创作者返回 404（不暴露存在性）。
- `GET /v1/admin/dramas`：`creator_id IN (SELECT id FROM creators WHERE region = ...)`。
- `GET /v1/admin/dramas/:id`、`GET /v1/admin/dramas/:id/episodes`：所属创作者非本地区 → 404。
- 剧集视图 `episodeAdminViewFor`：`region_admin` 的 `video_url`、`vod_file_id` 强制置空。

### 5.4 创建与筛选（超管侧）

- `POST /v1/admin/admins`：新增 `role`（`region_admin`）、`region`（必填，≤64 字）、`remark`（≤255 字）字段；region_admin 不允许携带任何 `permissions`。
- `PUT /v1/admin/admins/:id`：可改 `region` / `remark`（region 不可清空）。
- `GET /v1/admin/admins`：支持 `role` 精确筛选 + `region` 模糊筛选（省/市均可），返回 `region`、`remark` 字段。
- 超管自身在 creators 列表也可用 `region` 参数按省/市模糊筛选（ILIKE，不强制）。

### 5.5 建议前端适配

- 管理员列表页：新增"角色"和"地区"筛选下拉；表格加 `region`、`remark` 列。
- 创建/编辑管理员表单：选"地区管理员"角色时，必填"地区"（级联省市选择器，精确到市）+ 可选备注。
- 登录后：region_admin 只保留"创作者管理（只读）"和"作品管理（只读）"入口，隐藏视频预览/播放、审核按钮、以及所有写操作按钮；接口层面已兜底，前端按 `me` 返回的 `role === 'region_admin'` 做展示裁剪即可。
