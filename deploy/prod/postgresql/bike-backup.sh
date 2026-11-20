#!/usr/bin/env bash
# 每日逐库导出并加密上传到异地对象存储。在从库机器上由 bike-backup.timer 触发。
# 配置：/etc/bike/backup.env（BACKUP_DBS、BACKUP_REMOTE、BACKUP_AGE_RECIPIENT、PGHOST、PGPORT、PGUSER、PGPASSFILE）
set -euo pipefail
source /etc/bike/backup.env

day="$(date +%Y-%m-%d)"
work="/var/lib/bike-backup/$day"
mkdir -p "$work"
trap 'rm -rf "$work"' EXIT

for db in $BACKUP_DBS; do
  pg_dump -Fc -Z 6 -d "$db" -f "$work/$db.dump"
  age -r "$BACKUP_AGE_RECIPIENT" -o "$work/$db.dump.age" "$work/$db.dump"
  rm -f "$work/$db.dump"
  sha256sum "$work/$db.dump.age" >>"$work/SHA256SUMS"
done

rclone copy "$work" "$BACKUP_REMOTE/daily/$day" --immutable
logger -t bike-backup "备份完成 $day：$BACKUP_DBS"
