#!/usr/bin/env bash
# 启停本机运营商模拟器（127.0.0.1:18390，见 mock-operator.mjs）。用法：bash deploy/local/mock-operator.sh [start|stop]
source "$(dirname "${BASH_SOURCE[0]}")/_common.sh"
port="${MOCK_OP_PORT:-18390}"
case "${1:-start}" in
  stop)
    pid="$(netstat -ano | grep -E "TCP +127\.0\.0\.1:$port .*LISTENING" | awk '{print $5}' | head -1 || true)"
    [[ -n "$pid" ]] && taskkill //PID "$pid" //F >/dev/null 2>&1 && log "已停止 mock-operator" || log "mock-operator 没在运行" ;;
  start)
    if curl -fsS -m2 "http://127.0.0.1:$port/mock/state" >/dev/null 2>&1; then log "mock-operator 已在运行"; exit 0; fi
    mkdir -p "$SECRETS_DIR/extra"
    cd "$LOCAL_DIR"
    MOCK_OP_PORT="$port" nohup node "$(winpath "$LOCAL_DIR/mock-operator.mjs")" >>"$LOG_DIR/mock-operator.log" 2>&1 </dev/null &
    sleep 1
    curl -fsS -m3 "http://127.0.0.1:$port/mock/state" >/dev/null && log "mock-operator 已启动 127.0.0.1:$port" || die "启动失败，看 $LOG_DIR/mock-operator.log" ;;
esac
