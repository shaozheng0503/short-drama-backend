# 管理后台角色权限边界

> 本文档由 `internal/handler/server.go` 的实际路由守卫推导而来，是角色权限的权威说明。
> 改了路由上的 `requireAdminRole(...)` 记得同步本文件。

## 1. 角色与初始账号

`admins.role` 取值（见 `internal/model/model.go`）：

| role | 名称 | 初始账号 | 初始密码 |
|---|---|---|---|
| `admin` | 超级管理员（放行一切） | `admin` | `ADMIN_INIT_PASSWORD` |
| `finance` | 财务 | `finance` | 同 `ADMIN_INIT_PASSWORD` |
| `auditor` | 审核 | `auditor` | 同 `ADMIN_INIT_PASSWORD` |

- 三个账号由 `internal/database/database.go` 在启动时自动补齐（`ensureInitialAdmin` + `ensureRoleAdmins`），**幂等**：账号已存在则跳过，不覆盖已改过的密码。
- `finance` / `auditor` 的初始密码与 `admin` 同源，都取 `ADMIN_INIT_PASSWORD`（生产该值若为 `123456`，三个账号初始密码就都是 `123456`）。
- 登录入口统一 `POST /v1/admin/auth/login`，返回 JWT；之后请求带 `Authorization: Bearer <token>`。

## 2. 鉴权机制（怎么生效的）

请求经过三层（`server.go` 的 `adminAuth` 组）：

1. `RequireAdmin` —— 校验 JWT，subject 必须是 `admin`。
2. `requireActiveAdmin` —— 按 token 里的 id 查库，账号必须 `active`，并把 `role` 写进 context。
3. `requireAdminRole(允许的角色...)` —— **只挂在需要限权的具体动作上**：
   - 角色是 `admin`（超管）→ 放行一切；
   - 否则角色须命中允许列表，命中放行，不中 `403 当前角色无权执行该操作`；
   - **不传参 `requireAdminRole()` = 仅超管**（允许列表为空，非超管一律拒）。

> 注意：JWT 里不带 role，role 每次请求实时查库，所以改账号角色/封禁即时生效，无需重新登录。

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

### 任意在职管理员（无角色限制，三种角色都可）
读类接口（dashboard、各类列表/详情、config 的 GET、合同模板下载等）以及**未单独挂角色守卫的写操作**，对 `admin`/`finance`/`auditor` 一律开放，例如：
- 短剧本体 CRUD：`POST/PUT/DELETE /admin/dramas`、`/publish`、`/offline`、剧集 CRUD
- 创作者管理：列表/新建/编辑/封禁解封（注意：实名认证的 approve/reject 才限 auditor）
- 用户封禁解封、评论删除、分类/语言/渠道账号 CRUD
- 合同 CRUD / 取消 / 电子签

## 4. 当前粒度的局限（按需收紧）

这是**粗粒度**模型：只有"审核动作""资金动作""全局配置"三类敏感操作被单独限权，其余写操作对所有 admin 角色开放。也就是说当前 `auditor` / `finance` 账号**也能**新建/上下架短剧、封禁用户、删评论、改分类等。

会议结论（2026-06）是"其他面板暂不考虑"，故维持现状。若后续要严格隔离（审核员只能审、财务只能管钱、其余写操作收归超管），在对应路由补 `requireAdminRole(...)` 即可，无需改鉴权框架。
