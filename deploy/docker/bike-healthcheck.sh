#!/usr/bin/env bash
# 容器健康检查：网关 /healthz、15 个服务各自的 /healthz、PostgreSQL、NATS JetStream 全部正常才算健康。只读，只连容器内回环。
set -u
fail=0
chk() { curl -fsS -m3 "$2" >/dev/null 2>&1 || { echo "不健康：$1" >&2; fail=1; }; }
chk gateway "http://127.0.0.1:18300/healthz"
for m in $(grep -v '^#' /opt/bike/services.tsv | grep -v '^[[:space:]]*$' | cut -f1); do
  p="$(grep -v '^#' /opt/bike/services.tsv | awk -F'\t' -v m="$m" '$1==m{print $2}')"
  chk "$m" "http://127.0.0.1:$p/healthz"
done
chk nats "http://127.0.0.1:8222/healthz?js-enabled-only=true"
pg_isready -q -h 127.0.0.1 -p 5432 || { echo "不健康：postgres" >&2; fail=1; }
exit $fail
