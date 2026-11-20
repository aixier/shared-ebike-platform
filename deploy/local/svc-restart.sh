#!/usr/bin/env bash
# 重启一个服务（先杀旧进程，再用 bin/svc/<模块>.exe 起新的）。env 取 env/<模块>.env，再叠加 extra.tsv 里该模块的明文变量。
# 只监听 env 里的 127.0.0.1。用法：bash deploy/local/svc-restart.sh <模块>
source "$(dirname "${BASH_SOURCE[0]}")/_common.sh"
m="${1:?用法：svc-restart.sh <模块>}"
exe="$BIN_DIR/svc/$m.exe"
[[ -x "$exe" ]] || die "没有 $exe"
set -a; source "$ENV_DIR/$m.env"
while IFS=$'\t' read -r mod var val; do
  [[ "$mod" == "$m" && "$val" != @* && -n "$var" ]] && export "$var=$val"
done < <(grep -v '^#' "$LOCAL_DIR/extra.tsv" | grep -v '^[[:space:]]*$')
set +a
[[ "$LISTEN_HOST" == "127.0.0.1" ]] || die "LISTEN_HOST 必须是 127.0.0.1"
pid="$(netstat -ano | grep -E "TCP +127\.0\.0\.1:$PORT .*LISTENING" | awk '{print $5}' | head -1 || true)"
if [[ -n "$pid" ]]; then log "结束旧进程 $m（pid $pid）"; taskkill //PID "$pid" //F >/dev/null 2>&1 || true; sleep 1; fi
cd "$LOCAL_DIR"
nohup "$exe" >>"$LOG_DIR/$m.log" 2>&1 </dev/null &
echo $! > "$RUN_DIR/$m.pid"
for i in $(seq 1 30); do
  if curl -fsS -m2 "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then log "$m 已就绪（127.0.0.1:$PORT）"; exit 0; fi
  sleep 1
done
die "$m 30 秒内没起来，看 $LOG_DIR/$m.log"
