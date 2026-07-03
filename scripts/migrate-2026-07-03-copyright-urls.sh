#!/bin/bash
# 2026-07-03 migration: copyright_file_url (单图 string) → copyright_file_urls (多图 JSON 数组)
# 吴建棉反馈：上传授权文件要支持多张图片上传
# 21 条老数据需要保留（外层包成 JSON 数组）
#
# 跑这个脚本之前：
#   1. 二进制已部署到 sandbox + prod（model 改了，但 DB 没动）
#   2. 部署完会启动失败（model 字段找不到），所以**必须先跑 migration 再启动服务**
# 建议：先把 systemd 停掉，跑完 migration，再启动

set -euo pipefail

PGPASSWORD="${DB_PASS:-VBM9F3D5oAnXXaPz6wWSS1wU}" \
  psql -h 127.0.0.1 -U ai_drama -d "${DB_NAME:-ai_drama}" <<'SQL'
BEGIN;

-- 1) 加新列
ALTER TABLE dramas ADD COLUMN IF NOT EXISTS copyright_file_urls text;

-- 2) 老数据迁移：单图 URL → JSON 数组 ["url"]
-- 用 json_build_array 正确转义 URL 里的特殊字符
UPDATE dramas
SET copyright_file_urls = json_build_array(copyright_file_url)::text
WHERE copyright_file_url IS NOT NULL
  AND length(copyright_file_url) > 0
  AND (copyright_file_urls IS NULL OR length(copyright_file_urls) = 0);

-- 3) 删老列
ALTER TABLE dramas DROP COLUMN IF EXISTS copyright_file_url;

COMMIT;

-- 4) 验证
SELECT 'drama_count_with_copyright' AS metric, COUNT(*) AS value
FROM dramas WHERE copyright_file_urls IS NOT NULL AND length(copyright_file_urls) > 0;
SQL
