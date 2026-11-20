#!/usr/bin/env bash
# 停止本机开发的基础组件（网关、NATS、PostgreSQL）。数据保留；要清空删 deploy/local/data/。
# 用法：bash deploy/local/down.sh
source "$(dirname "${BASH_SOURCE[0]}")/_common.sh"

if [[ -x "$CADDY_EXE" ]] && port_in_use "$CADDY_ADMIN_PORT"; then
  "$CADDY_EXE" stop --address "$LOCAL_HOST:$CADDY_ADMIN_PORT" >/dev/null 2>&1 && log "网关已停止" || log "网关停止失败"
fi

if [[ -s "$RUN_DIR/nats.pid" ]]; then
  pid="$(tr -d '[:space:]' <"$RUN_DIR/nats.pid")"
  if taskkill //PID "$pid" //F >/dev/null 2>&1; then log "NATS 已停止（PID $pid）"; else log "NATS 未在运行"; fi
  rm -f "$RUN_DIR/nats.pid"
else
  log "NATS 未在运行"
fi

if [[ -f "$PG_DATA/PG_VERSION" ]] && "$PG_BIN/pg_ctl.exe" status -D "$(winpath "$PG_DATA")" >/dev/null 2>&1; then
  "$PG_BIN/pg_ctl.exe" stop -D "$(winpath "$PG_DATA")" -m fast -w >/dev/null && log "PostgreSQL 已停止"
else
  log "PostgreSQL 未在运行"
fi
