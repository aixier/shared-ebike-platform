#!/usr/bin/env bash
# 本机桩模拟器启停：bash deploy/local/pile-sim.sh start|stop|status|occupy <桩位号> <tag> [tagType]|leave <桩位号>
# pid 在 run/pile-sim.pid，日志在 logs/pile-sim.log。只连 127.0.0.1 网关，不监听端口。
source "$(dirname "${BASH_SOURCE[0]}")/_common.sh"
f="$LOCAL_DIR/pile-sim.mjs"; pidf="$RUN_DIR/pile-sim.pid"
alive() { [[ -f "$pidf" ]] && tasklist //FI "PID eq $(cat "$pidf")" 2>/dev/null | grep -qi node; }
case "${1:-}" in
  start) alive && { log "pile-sim 已在运行（pid $(cat "$pidf")）"; exit 0; }
    cd "$LOCAL_DIR"; nohup node "$f" run >>"$LOG_DIR/pile-sim.log" 2>&1 </dev/null &
    sleep 3
    alive && log "pile-sim 已启动（pid $(cat "$pidf")）" || die "启动失败，看 $LOG_DIR/pile-sim.log" ;;
  stop) if alive; then taskkill //PID "$(cat "$pidf")" //F >/dev/null 2>&1; log "pile-sim 已停止"; else log "pile-sim 没在运行"; fi; rm -f "$pidf" ;;
  status) alive && echo "运行中 pid $(cat "$pidf")" || echo "未运行" ;;
  occupy|leave) shift_cmd="$1"; shift; node "$f" "$shift_cmd" "$@" ;;
  *) echo "用法：pile-sim.sh start|stop|status|occupy <桩位号> <tag> [tagType]|leave <桩位号>"; exit 2 ;;
esac
