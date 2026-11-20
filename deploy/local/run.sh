#!/usr/bin/env bash
# 按服务名启动一个服务：读 deploy/local/env/<模块>.env，在 backend/<模块>/ 下执行 go run ./cmd/<模块>。
# 用法：bash deploy/local/run.sh <模块>      例：bash deploy/local/run.sh ride
source "$(dirname "${BASH_SOURCE[0]}")/_common.sh"

m="${1:-}"
[[ -n "$m" ]] || die "用法：run.sh <模块>，可选：$(modules | paste -sd' ' -)"
modules | grep -qx "$m" || die "没有模块 $m，可选：$(modules | paste -sd' ' -)"
env_file="$ENV_DIR/$m.env"
[[ -f "$env_file" ]] || die "没有 $env_file，先跑 bash deploy/local/gen-env.sh"
svc_dir="$PROJECT_DIR/backend/$m"
[[ -f "$svc_dir/go.mod" && -d "$svc_dir/cmd/$m" ]] || die "backend/$m/cmd/$m 还不存在（该服务尚未交付可执行入口）"

set -a
# shellcheck disable=SC1090
source "$env_file"
set +a
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

cd "$svc_dir"
log "启动 $m（$LISTEN_HOST:$PORT）"
exec go run "./cmd/$m"
