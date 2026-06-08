-- fix-draft-audit-status.sql
-- 一次性数据订正：把"草稿且从未提交审核、却被旧默认值写成 approved"的短剧改回 pending。
--
-- 背景：旧代码 creatorCreateDrama 把新建草稿的 audit_status 默认写成 approved
--       （GORM 列默认值也是 approved），导致创作者中心"审核状态"列把
--       从未提交审核的草稿误显示为"审核通过"。代码已修复为默认 pending，
--       本脚本订正存量数据。仅命中 draft + approved + 从未提交(audit_submitted_at IS NULL)。
--       已上架(published)、待上架(awaiting_publish) 的 approved 是正确状态，不会被动到。
--
-- 用法（在服务器上，对准生产库）：
--   psql "<生产 DSN>" -f scripts/fix-draft-audit-status.sql
-- 脚本在一个事务内执行：先打印将被改动的行，再 UPDATE（带 RETURNING 可审计），最后 COMMIT。
-- 若预览结果不符合预期，按 Ctrl-C 或把 COMMIT 改成 ROLLBACK 即可放弃。

\echo '=== 改动前：命中的草稿（draft + approved + 从未提交） ==='
SELECT id, title, status, audit_status, audit_submitted_at, created_at
FROM dramas
WHERE status = 'draft' AND audit_status = 'approved' AND audit_submitted_at IS NULL
ORDER BY created_at DESC;

BEGIN;

UPDATE dramas
SET audit_status = 'pending'
WHERE status = 'draft' AND audit_status = 'approved' AND audit_submitted_at IS NULL
RETURNING id, title, status, audit_status;

COMMIT;

\echo '=== 改动后核对：应已无 draft + approved ==='
SELECT id, title, status, audit_status
FROM dramas
WHERE status = 'draft'
ORDER BY id;

\echo '=== 安全核对：published/awaiting_publish 的 approved 未受影响 ==='
SELECT status, count(*)
FROM dramas
WHERE audit_status = 'approved'
GROUP BY status
ORDER BY status;
