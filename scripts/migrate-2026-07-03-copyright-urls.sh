#!/bin/bash
# 2026-07-03 migration: copyright_file_url (单图 string) → copyright_file_urls (多图 JSON 数组)
# 幂等：列已存在则不重加，老列已删则不重删
# - 加新列 copyright_file_urls (text)
# - 老数据迁移：单图 URL → JSON 数组 ["url"]
# - 删老列 copyright_file_url
#
# 跑这个脚本之前：
#   1. systemd 服务已停（reload 阶段会触发 AutoMigrate，可能与脚本冲突）
# 建议顺序：
#   systemctl stop drama-backend drama-backend-sandbox
#   bash scripts/migrate-2026-07-03-copyright-urls.sh
#   systemctl start drama-backend drama-backend-sandbox

set -uo pipefail  # 注意：不用 -e —— 我们要每步都跑（即使上一步失败）

PSQL="psql -h 127.0.0.1 -U ai_drama -d ${DB_NAME:-ai_drama}"
export PGPASSWORD="${DB_PASS:-VBM9F3D5oAnXXaPz6wWSS1wU}"

echo "=== 1) 加新列 copyright_file_urls (text) ==="
$PSQL -v ON_ERROR_STOP=0 <<'SQL'
DO $do$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'dramas' AND column_name = 'copyright_file_urls'
  ) THEN
    ALTER TABLE dramas ADD COLUMN copyright_file_urls text;
  END IF;
END
$do$;
SQL

echo ""
echo "=== 2) 老数据迁移：单图 URL → JSON 数组 ==="
$PSQL -v ON_ERROR_STOP=0 -c "UPDATE dramas SET copyright_file_urls = json_build_array(copyright_file_url)::text WHERE copyright_file_url IS NOT NULL AND length(copyright_file_url) > 0 AND (copyright_file_urls IS NULL OR length(copyright_file_urls) = 0);"

echo ""
echo "=== 3) 删老列 copyright_file_url ==="
$PSQL -v ON_ERROR_STOP=0 <<'SQL'
DO $do$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'dramas' AND column_name = 'copyright_file_url'
  ) THEN
    ALTER TABLE dramas DROP COLUMN copyright_file_url;
  END IF;
END
$do$;
SQL

echo ""
echo "=== 4) 验证 ==="
$PSQL -c "SELECT column_name FROM information_schema.columns WHERE table_name='dramas' AND column_name LIKE '%copyright%' ORDER BY column_name;"
$PSQL -c "SELECT COUNT(*) AS dramas_with_new_field FROM dramas WHERE copyright_file_urls IS NOT NULL AND length(copyright_file_urls) > 0;"
