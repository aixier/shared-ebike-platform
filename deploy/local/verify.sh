#!/usr/bin/env bash
# 本机基础组件自检：库与账号隔离、NATS 连通与 JetStream、网关（若已启动）。只读，不改任何东西。
# 用法：bash deploy/local/verify.sh
source "$(dirname "${BASH_SOURCE[0]}")/_common.sh"
set +e
fail=0
ok()  { printf '  通过  %s\n' "$*"; }
bad() { printf '  失败  %s\n' "$*"; fail=1; }

psql_as() { # 账号模块 目标库模块
  PGPASSWORD="$(cat "$SECRETS_DIR/db/$1.pass")" PGCONNECT_TIMEOUT=5 \
    "$PG_BIN/psql.exe" -U "bike_$1" -d "bike_$2" -qtA -c "select current_user||'@'||current_database()" 2>&1
}

echo "PostgreSQL（$LOCAL_HOST:$PG_PORT）"
mods=($(modules))
for i in "${!mods[@]}"; do
  m="${mods[$i]}"; other="${mods[$(( (i + 1) % ${#mods[@]} ))]}"
  out="$(psql_as "$m" "$m")"
  [[ "$out" == "bike_$m@bike_$m" ]] && ok "bike_$m 连自己的库" || bad "bike_$m 连自己的库：$out"
  out="$(psql_as "$m" "$other")"
  if [[ "$out" == *"no pg_hba.conf entry"* || "$out" == *"permission denied"* ]]; then ok "bike_$m 连 bike_$other 被拒绝"
  else bad "bike_$m 连 bike_$other 未被拒绝：$out"; fi
done
lst="$(PGPASSWORD="$(cat "$SECRETS_DIR/pg_superuser.pass")" "$PG_BIN/psql.exe" -U postgres -d postgres -qtA -c 'show listen_addresses')"
[[ "$lst" == "$LOCAL_HOST" ]] && ok "listen_addresses = $lst" || bad "listen_addresses = $lst"

echo "NATS（$LOCAL_HOST:$NATS_PORT）"
reply="$(
  exec 3<>"/dev/tcp/$LOCAL_HOST/$NATS_PORT" || exit 1
  IFS= read -r -t 3 info <&3
  printf 'CONNECT {"verbose":false,"name":"deploy-verify"}\r\nPING\r\n' >&3
  IFS= read -r -t 3 pong <&3
  printf '%s|%s' "${info:0:4}" "$pong" | tr -d '\r'
)"
[[ "$reply" == "INFO|PONG" ]] && ok "客户端协议连通（INFO → CONNECT → PING/PONG）" || bad "客户端连不上：$reply"
curl -fs "http://$LOCAL_HOST:$NATS_MONITOR_PORT/healthz?js-enabled-only=true" >/dev/null \
  && ok "JetStream 已启用" || bad "JetStream 未启用"
# 经客户端协议向 JetStream 发一次账户信息请求（$JS.API.INFO），收到 io.nats.jetstream.api.v1.account_info_response 即通
js="$(
  exec 3<>"/dev/tcp/$LOCAL_HOST/$NATS_PORT" || exit 1
  IFS= read -r -t 3 _ <&3
  printf 'CONNECT {"verbose":false}\r\nSUB _INBOX.verify 1\r\nPUB $JS.API.INFO _INBOX.verify 0\r\n\r\n' >&3
  for _ in 1 2 3 4; do IFS= read -r -t 3 line <&3 || break; [[ "$line" == *account_info_response* ]] && { echo yes; break; }; done
)"
[[ "$js" == yes ]] && ok "客户端可调用 JetStream API" || bad "客户端调用 JetStream API 无响应"
host="$(curl -fs "http://$LOCAL_HOST:$NATS_MONITOR_PORT/varz" | grep -o '"host": *"[^"]*"' | head -1)"
[[ "$host" == *"$LOCAL_HOST"* ]] && ok "监听 $host" || bad "监听地址 $host"

if port_in_use "$GATEWAY_PORT"; then
  echo "网关（$LOCAL_HOST:$GATEWAY_PORT）"
  base="http://$LOCAL_HOST:$GATEWAY_PORT"
  code() { curl -s -o /dev/null -w '%{http_code}' "$base$1"; }
  [[ "$(code /internal/ride/orders)" == 403 ]] && ok "/internal/ 被拒绝（403）" || bad "/internal/ 未被拒绝"
  [[ "$(code /api/ride/internal/x)" == 403 ]] && ok "/api/<模块>/internal 被拒绝（403）" || bad "/api/<模块>/internal 未被拒绝"
  [[ "$(code /app/index.html)" == 200 ]] && ok "用户端静态页 /app/ 可打开" || bad "/app/ 打不开"
  [[ "$(code /admin/index.html)" == 200 ]] && ok "管理端静态页 /admin/ 可打开" || bad "/admin/ 打不开"
  [[ "$(code /enforce/index.html)" == 200 ]] && ok "执法端静态页 /enforce/ 可打开" || bad "/enforce/ 打不开"
  [[ "$(code /shared/API.md)" == 404 ]] && ok "原型说明文档不对外（404）" || bad "原型 .md 可被访问"
  c="$(code /api/ride/healthz)"; [[ "$c" == 503 || "$c" == 200 || "$c" == 404 || "$c" == 401 ]] && ok "/api/ride/ 转到 18302（$c；503 SERVER_BUSY 表示服务未启动）" || bad "/api/ride/ 返回 $c"
fi

(( fail == 0 )) && echo "全部通过" || { echo "有失败项"; exit 1; }
