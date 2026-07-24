# 认领流程接口变更说明（0.15.1）

> 日期：2026-07-24
> 环境：生产 (api.langzhi.top) + 沙箱 (api-dev.langzhi.top) 均已部署
> OpenAPI 文档：`docs/openapi-claim-0.15.1.yaml`（可导入 Apifox）

---

## 一、枚举值变更（全链路统一）

以下枚举变更影响所有认领相关接口的请求参数和响应字段。

### 1. 认领状态 status / review_status

- `deposit_pending` — 待支付押金
- `authorization_pending` — 待手动授权（**原 `auth_pending`，已统一改名**）
- `review_pending` — 待审核
- `contract_pending` — 待签署合同
- `completed` — 已完成
- `rejected` — 已驳回

### 2. 押金状态 deposit_status

- `unpaid` — 未支付
- `paid` — 已支付（冻结中）
- `released` — 已释放（**新增：驳回后押金退回可用余额**）

### 3. 合同状态 contract_status

- `none` — 未进入合同阶段（**新增：审核前 / 驳回后均为 none**）
- `pending` — 待签署
- `signed` — 已签署
- `completed` — 已完成

### 4. 剧级聚合状态（claimed-dramas 列表/详情的 status）

- `active` — 已授权/活跃
- `appending` — 有活跃认领 + 有审核中认领
- `pending` — 审核中（无活跃认领）
- `rejected` — 仅驳回（无活跃认领）
- `revoked` — 全部已撤销

---

## 二、接口变更明细

### Issue 1：claimed-dramas 现包含审核中 + 已驳回认领

**影响接口：**
- `GET /v1/publisher/claimed-dramas`
- `GET /v1/publisher/claimed-dramas/:id`
- `GET /v1/publisher/claimed-dramas/:id/claims`

**变更内容：**
- 列表现在会返回该发行商「审核中」和「已驳回」的认领剧（之前只返回已入库剧）
- 已驳回认领的剧会出现在列表中，status 为 `rejected` 或 `pending`
- 新增字段 `latest_reject_reason`（string）— 最近一条驳回原因
- 新增字段 `latest_rejected_application_id`（integer）— 最近被驳回的申请 ID
- `/claims` 明细列表中，rejected 类型的记录包含 `reject_reason` 字段

**前端需要做的：**
- claimed-dramas 列表页支持展示 `pending` / `rejected` 状态的剧
- 对 `rejected` 状态的剧，展示 `latest_reject_reason` 作为驳回原因
- 可添加「重新认领」入口（驳回后剧本身仍可再认领）

---

### Issue 2：驳回后 deposit_status 更新为 released

**影响接口：**
- `POST /admin/distributor-claims/:id/reject`（驳回动作）
- `GET /admin/distributor-claims/:id`（管理端详情）
- `GET /v1/publisher/claims/:id`（发行商详情）
- `GET /v1/publisher/claims`（发行商列表）

**变更内容：**
- 驳回后 `deposit_status` 从 `paid` 更新为 `released`（之前停在 `paid`）
- 冻结押金同时释放回发行商可用余额（计费逻辑本来就正确，只是状态标签没更新）
- 数据库迁移已将历史驳回记录的 `deposit_status` 从 `paid` 修正为 `released`

**管理端需要做的：**
- 押金状态展示从「已支付」改为「已释放」（released）
- 确保状态标签映射包含 `released`

---

### Issue 3：Admin 认领详情返回 authorization_confirmed

**影响接口：**
- `GET /admin/distributor-claims/:id`

**变更内容：**
- 新增字段 `authorization_confirmed`（boolean）— 是否已确认第三方平台授权
- `authorized_at` 在发行商 submit 时已正确写入（之前为 null 的问题已修复）
- 管理端可据此展示「授权确认：已确认 / 未确认」

**管理端需要做的：**
- 详情页增加「授权确认」展示，读 `authorization_confirmed` 字段
- 若 `authorization_confirmed=true` 且 `authorized_at` 有值，展示确认时间

---

### Issue 4：审核前 contract_status 为 none

**影响接口：**
- 所有认领相关接口的 `contract_status` 字段

**变更内容：**
- 创建认领时 `contract_status` 初始为 `none`（之前为 `pending`）
- 驳回后 `contract_status` 设为 `none`（之前保持 `pending`）
- 审核通过后 `contract_status` 设为 `pending`（待签署）
- 上传合同后 `contract_status` 设为 `completed`
- 数据库迁移已将历史记录中未进入合同阶段的 `contract_status` 从 `pending` 修正为 `none`

**前端/管理端需要做的：**
- 合同状态标签映射增加 `none` → 「未进入合同阶段」或「-」
- `pending` 仅在审核通过后出现，表示「待签署」

---

### Issue 5：auth_pending 统一为 authorization_pending

**影响接口：**
- 所有认领相关接口的 `status` / `review_status` 字段
- `GET /admin/distributor-claims?status=xxx` 筛选参数

**变更内容：**
- `auth_pending` 全部替换为 `authorization_pending`
- 数据库迁移已将历史记录从 `auth_pending` 更新为 `authorization_pending`
- 管理端筛选「待手动授权」时参数用 `authorization_pending`

**前端/管理端需要做的：**
- 状态标签映射中 `authorization_pending` → 「待手动授权」
- 移除 `auth_pending` 的旧映射（或保留兼容但标记 deprecated）
- 管理端筛选下拉框值更新为 `authorization_pending`

---

## 三、数据迁移说明

部署时自动执行了以下幂等迁移（`migrateClaimStatusEnums`）：

1. `distributor_applications.status` 列扩容 VARCHAR(20) → VARCHAR(32)（`authorization_pending` 21 字符超限）
2. `UPDATE ... SET status = 'authorization_pending' WHERE status = 'auth_pending'` — 历史数据统一命名
3. `UPDATE ... SET deposit_status = 'released' WHERE status = 'rejected' AND deposit_status = 'paid'` — 驳回记录押金状态修正
4. `UPDATE ... SET contract_status = 'none' WHERE status IN ('deposit_pending','authorization_pending','review_pending','rejected') AND contract_status = 'pending'` — 未进入合同阶段的状态修正

所有迁移均带 WHERE 条件，重复执行安全。

---

## 四、前端 / 管理端适配清单

### 发行商前端

- [ ] claimed-dramas 列表支持 `pending` / `rejected` / `appending` 状态展示
- [ ] rejected 状态展示 `latest_reject_reason` 驳回原因
- [ ] claimed-dramas 详情页展示 `latest_reject_reason`（如有）
- [ ] claimed-dramas/:id/claims 明细列表展示 rejected 记录的 `reject_reason`
- [ ] 认领申请列表状态标签 `authorization_pending` → 「待手动授权」
- [ ] 移除 `auth_pending` 旧标签映射
- [ ] 押金状态标签增加 `released` → 「已释放」
- [ ] 合同状态标签增加 `none` → 「-」或「未进入合同阶段」

### 管理端

- [ ] 认领详情展示 `authorization_confirmed` 字段（授权确认：已确认/未确认）
- [ ] 认领详情展示 `authorized_at`（授权确认时间）
- [ ] 押金状态标签 `released` → 「已释放」
- [ ] 合同状态标签 `none` → 「-」或「未进入合同阶段」
- [ ] `contract_status=pending` 仅在审核通过后出现
- [ ] 状态筛选下拉框值更新为 `authorization_pending`（非 auth_pending）
- [ ] 驳回操作后确认 `deposit_status` 显示为 `released`

---

## 五、验证用例

- 认领申请 #24（reject_reason=平台授权截图缺失，请补充后重新提交）
  - `GET /v1/publisher/claimed-dramas` → 应出现该剧，status=rejected
  - `GET /v1/publisher/claimed-dramas/:id/claims` → rejected 记录含 reject_reason
  - `GET /admin/distributor-claims/24` → deposit_status=released, contract_status=none, authorization_confirmed=true
