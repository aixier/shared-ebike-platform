#!/usr/bin/env bash
# 本机入口网关：下载 Caddy（已有则跳过），校验配置后在后台启动，地址 http://127.0.0.1:18300/。
# 用法：bash deploy/local/gateway.sh [start|stop|validate]   默认 start
#   BIKE_WEB_ROOT 可覆盖静态根目录（默认 ../原型）
source "$(dirname "${BASH_SOURCE[0]}")/_common.sh"

install_caddy() {
  if [[ -x "$CADDY_EXE" ]]; then return; fi
  local name="caddy_${CADDY_VERSION}_windows_amd64"
  local base="https://github.com/caddyserver/caddy/releases/download/v$CADDY_VERSION"
  local dl="$BIN_DIR/_dl"
  fetch "$base/$name.zip" "$dl/$name.zip"
  fetch "$base/caddy_${CADDY_VERSION}_checksums.txt" "$dl/caddy.checksums"
  local want got
  want="$(grep " $name.zip\$" "$dl/caddy.checksums" | awk '{print $1}')"
  got="$(sha512sum "$dl/$name.zip" | awk '{print $1}')"
  [[ -n "$want" && "$want" == "$got" ]] || die "Caddy 校验值不符"
  mkdir -p "$(dirname "$CADDY_EXE")"
  unzip -q -o "$dl/$name.zip" caddy.exe -d "$(dirname "$CADDY_EXE")"
  rm -f "$dl/$name.zip" "$dl/caddy.checksums"
  "$CADDY_EXE" version
}

mkdir -p "$LOG_DIR"
export BIKE_WEB_ROOT="${BIKE_WEB_ROOT:-$(winpath "$PROJECT_DIR/原型")}"
export BIKE_LOG_DIR="$(winpath "$LOG_DIR")"
CONF="$(winpath "$LOCAL_DIR/Caddyfile")"

case "${1:-start}" in
  validate)
    install_caddy
    "$CADDY_EXE" validate --config "$CONF" --adapter caddyfile ;;
  start)
    install_caddy
    if port_in_use "$CADDY_ADMIN_PORT"; then
      "$CADDY_EXE" reload --config "$CONF" --adapter caddyfile --address "$LOCAL_HOST:$CADDY_ADMIN_PORT"
      log "网关已在运行，已重新加载配置"
    else
      port_bindable "$GATEWAY_PORT" || die "端口 $GATEWAY_PORT 已被别的程序占用"
      "$CADDY_EXE" start --config "$CONF" --adapter caddyfile >"$LOG_DIR/gateway.log" 2>&1 \
        || { cat "$LOG_DIR/gateway.log"; die "网关启动失败"; }
      log "网关已启动：http://$LOCAL_HOST:$GATEWAY_PORT/  （静态根 $BIKE_WEB_ROOT）"
    fi ;;
  stop)
    "$CADDY_EXE" stop --address "$LOCAL_HOST:$CADDY_ADMIN_PORT" && log "网关已停止" ;;
  *) die "用法：gateway.sh [start|stop|validate]" ;;
esac
