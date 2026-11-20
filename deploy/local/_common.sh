# 本目录脚本共用的变量与小函数（只给 deploy/local/*.sh 自己 source，不给任何服务引用）
set -euo pipefail

LOCAL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$LOCAL_DIR/.." && pwd)"
PROJECT_DIR="$(cd "$DEPLOY_DIR/.." && pwd)"

# shellcheck disable=SC1091
source "$LOCAL_DIR/versions.env"

# PostgreSQL 在含中文的路径下 initdb 失败（把本地代码页字节当 UTF-8 写进库）。
# 项目路径含非 ASCII 字符时，在 %LOCALAPPDATA% 下建一个纯英文名的目录联接（junction）指向本目录，
# 二进制与数据实际仍在 deploy/local/bin、deploy/local/data，只是经联接路径访问。
ascii_root() {
  if [[ "$LOCAL_DIR" =~ ^[[:print:]]+$ ]] && LC_ALL=C grep -q '^[ -~]*$' <<<"$LOCAL_DIR"; then
    printf '%s' "$LOCAL_DIR"; return
  fi
  local link_win link
  link_win="$(cygpath -w "${LOCALAPPDATA:-$USERPROFILE/AppData/Local}")\\bike-deploy-local"
  link="$(cygpath -u "$link_win")"
  if [[ ! -e "$link" ]]; then
    cmd.exe //c mklink //J "$link_win" "$(cygpath -w "$LOCAL_DIR")" >/dev/null || die "无法创建目录联接 $link_win"
  fi
  [[ -f "$link/versions.env" && "$link/versions.env" -ef "$LOCAL_DIR/versions.env" ]] \
    || die "$link_win 已存在但不指向本目录；删掉它（rmdir）后重试"
  printf '%s' "$link"
}
ROOT="$(ascii_root)"

BIN_DIR="$ROOT/bin"
DATA_DIR="$ROOT/data"
LOG_DIR="$ROOT/logs"
ENV_DIR="$ROOT/env"
SECRETS_DIR="$ROOT/secrets"
RUN_DIR="$ROOT/run"

PG_HOME="$BIN_DIR/pgsql"
PG_BIN="$PG_HOME/bin"
PG_DATA="$DATA_DIR/pg"
NATS_EXE="$BIN_DIR/nats/nats-server.exe"
NATS_STORE="$DATA_DIR/nats"
CADDY_EXE="$BIN_DIR/caddy/caddy.exe"

export PGHOST="$LOCAL_HOST" PGPORT="$PG_PORT"

# Windows 路径（正斜杠），给 Windows 原生程序用
winpath() { cygpath -m "$1"; }

log() { printf '[deploy] %s\n' "$*"; }
die() { printf '[deploy] 错误：%s\n' "$*" >&2; exit 1; }

# 读 services.tsv：输出 "模块 端口 被调方"
services() { grep -v '^#' "$DEPLOY_DIR/services.tsv" | grep -v '^[[:space:]]*$'; }
modules() { services | cut -f1; }

port_in_use() { netstat -ano 2>/dev/null | grep -E "TCP +[0-9.]+:$1 .*LISTENING" >/dev/null; }

# 能否在 127.0.0.1 上绑定该端口。netstat 看不到 WSL 镜像网络模式下 Linux 侧占用的端口，所以实测绑定一次
port_bindable() {
  powershell.exe -NoProfile -Command "try { \$l = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Parse('$LOCAL_HOST'), $1); \$l.Start(); \$l.Stop(); exit 0 } catch { exit 1 }" </dev/null >/dev/null 2>&1
}

rand_hex() { od -An -N"$1" -tx1 /dev/urandom | tr -d ' \n'; }

# 下载到 _dl 并校验（有校验值时）
fetch() { # url dest
  mkdir -p "$(dirname "$2")"
  log "下载 $1"
  local n
  for n in $(seq 1 30); do  # 断点续传：网络中断后从已下载处接着下
    curl -fL --progress-bar --retry 3 --retry-all-errors --connect-timeout 20 -C - -o "$2.part" "$1" && { mv "$2.part" "$2"; return 0; }
    log "下载中断，第 $n 次续传"
  done
  die "下载失败：$1（已下载部分保留在 $2.part，再跑一次会续传）"
}
