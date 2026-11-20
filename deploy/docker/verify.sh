#!/usr/bin/env bash
# 单镜像容器自检（对应 deploy/local/verify.sh）：网关与 15 个服务 /healthz、库与账号隔离、NATS JetStream、/internal 拒绝、静态页。只读。
# 用法：bash deploy/docker/verify.sh     BIKE_GATEWAY、BIKE_CONTAINER 可覆盖
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
G="${BIKE_GATEWAY:-http://127.0.0.1:28300}"; C="${BIKE_CONTAINER:-bike-single}"
fail=0; ok() { printf '  通过  %s\n' "$*"; }; bad() { printf '  失败  %s\n' "$*"; fail=1; }
dx() { MSYS_NO_PATHCONV=1 docker exec -i "$C" "$@"; }
code() { curl -s -o /dev/null -w '%{http_code}' -m5 "$G$1"; }

echo "网关与服务 /healthz"
[[ "$(curl -s -m5 $G/healthz)" == '{"ok":true}' ]] && ok "网关 /healthz" || bad "网关 /healthz"
mods=($(grep -v '^#' "$HERE/../services.tsv" | grep -v '^[[:space:]]*$' | cut -f1 | tr -d '\r'))
for m in "${mods[@]}"; do
  [[ "$(curl -s -m5 $G/healthz/$m)" == '{"ok":true}' ]] && ok "经网关 /healthz/$m" || bad "经网关 /healthz/$m"
  p="$(grep -v '^#' "$HERE/../services.tsv" | awk -F'\t' -v m="$m" '$1==m{print $2}' | tr -d '\r')"
  [[ "$(dx curl -s -m5 http://127.0.0.1:$p/healthz)" == *'"ok":true'* ]] && ok "容器内 $m:$p /healthz" || bad "容器内 $m:$p /healthz"
done

echo "PostgreSQL 账号隔离（账号连自己的库通过、连别人的库被拒）"
n=${#mods[@]}
for i in "${!mods[@]}"; do
  m="${mods[$i]}"; o="${mods[$(( (i + 1) % n ))]}"
  pw="$(dx cat /data/secrets/db/$m.pass | tr -d '\r\n')"
  r1="$(dx env PGPASSWORD="$pw" PGCONNECT_TIMEOUT=5 psql -h 127.0.0.1 -U bike_$m -d bike_$m -qtA -c 'select current_user||chr(64)||current_database()' 2>&1 | tr -d '\r')"
  [[ "$r1" == "bike_$m@bike_$m" ]] && ok "bike_$m 连 bike_$m" || bad "bike_$m 连自己的库：$r1"
  r2="$(dx env PGPASSWORD="$pw" PGCONNECT_TIMEOUT=5 psql -h 127.0.0.1 -U bike_$m -d bike_$o -qtA -c 'select 1' 2>&1 | tr -d '\r')"
  [[ "$r2" == *"no pg_hba.conf entry"* || "$r2" == *"permission denied"* ]] && ok "bike_$m 连 bike_$o 被拒绝" || bad "bike_$m 连 bike_$o 未被拒绝：$r2"
done
ls="$(dx su postgres -s /bin/sh -c "psql -h /var/run/postgresql -U postgres -qtA -c 'show listen_addresses'" | tr -d '\r')"
[[ "$ls" == 127.0.0.1 ]] && ok "listen_addresses = $ls" || bad "listen_addresses = $ls"

echo "NATS JetStream"
dx curl -fs "http://127.0.0.1:8222/healthz?js-enabled-only=true" >/dev/null && ok "JetStream 已启用" || bad "JetStream 未启用"
js="$(dx bash -c 'exec 3<>/dev/tcp/127.0.0.1/4222; IFS= read -r -t 3 _ <&3; printf "CONNECT {\"verbose\":false}\r\nSUB _INBOX.v 1\r\nPUB \$JS.API.INFO _INBOX.v 0\r\n\r\n" >&3; for _ in 1 2 3 4; do IFS= read -r -t 3 l <&3 || break; [[ "$l" == *account_info_response* ]] && { echo yes; break; }; done')"
[[ "$js" == yes ]] && ok "客户端可调用 JetStream API" || bad "JetStream API 无响应"
streams="$(dx curl -fs "http://127.0.0.1:8222/jsz?streams=true" | tr -d '\r' | grep -o '"streams": *[0-9]*' | head -1)"
ok "已建流：${streams:-未知}"
[[ "$(dx curl -s http://127.0.0.1:8222/varz | grep -o '"host": *"[^"]*"' | head -1)" == *127.0.0.1* ]] && ok "NATS 只监听 127.0.0.1" || bad "NATS 监听地址不是 127.0.0.1"

echo "网关"
[[ "$(code /internal/ride/orders)" == 403 ]] && ok "/internal/ 403" || bad "/internal/ 未被拒绝"
[[ "$(code /api/ride/internal/x)" == 403 ]] && ok "/api/<模块>/internal 403" || bad "/api/<模块>/internal 未被拒绝"
for p in /app/ /admin/ /enforce/; do [[ "$(code $p)" == 200 ]] && ok "$p 200" || bad "$p 打不开"; done
[[ "$(code /shared/API.md)" == 404 ]] && ok "原型说明文档 404" || bad "原型 .md 可被访问"

echo "监听面（容器内只有网关在 0.0.0.0）"
ext="$(dx bash -c 'awk "NR>1 && \$4==\"0A\"{print \$2}" /proc/net/tcp /proc/net/tcp6 | grep -vE "^0100007F:|^00000000000000000000000001000000:" | sort -u || true' | tr -d '\r')"
[[ "$ext" == *":477C" && $(wc -l <<<"$ext") -eq 1 ]] && ok "非回环监听只有一个：端口 18300（0x477C）" || bad "非回环监听：$ext"
(( fail == 0 )) && echo "全部通过" || { echo "有失败项"; exit 1; }
