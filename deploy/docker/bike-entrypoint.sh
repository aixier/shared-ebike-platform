#!/usr/bin/env bash
# 单镜像入口：首次启动初始化（库与账号、密钥、各服务 env），然后在本脚本里做进程管理。
# 起停顺序：PostgreSQL -> NATS -> 15 个服务 + 网关 + 模拟器；停止时反向（服务 -> NATS -> PostgreSQL）。
# 任何一个进程退出都会被拉起（退避 1 秒起，连续快速失败最多退避 10 秒）。
# 等价于 deploy/local 的 up.sh（库与账号）、gen-env.sh（env 与密钥）、gateway.sh（网关）、svc-restart.sh（起服务），数据全部在 /data。
set -euo pipefail

DATA=/data
APP=/opt/bike
BIN=$APP/bin
PGBIN=/usr/lib/postgresql/16/bin
PGDATA=$DATA/pg
SECRETS=$DATA/secrets
ENVDIR=$DATA/env

PG_PORT=5432
NATS_PORT=4222
NATS_MON=8222
GW_PORT=18300
LOOP=127.0.0.1

log() { printf '[bike] %s\n' "$*"; }
die() { printf '[bike] 错误：%s\n' "$*" >&2; exit 1; }
as() { local u=$1; shift; HOME=/tmp setpriv --reuid="$u" --regid="$u" --init-groups "$@"; }
services() { grep -v '^#' "$APP/services.tsv" | grep -v '^[[:space:]]*$'; }
modules() { services | cut -f1; }
rand_hex() { od -An -N"$1" -tx1 /dev/urandom | tr -d ' \n'; }
port_of()    { services | awk -F'\t' -v m="$1" '$1==m{print $2}'; }
callees_of() { services | awk -F'\t' -v m="$1" '$1==m && $3!="-"{print $3}' | tr ',' ' '; }
callers_of() { services | awk -F'\t' -v m="$1" '{n=split($3,a,","); for(i=1;i<=n;i++) if(a[i]==m) print $1}'; }
upper() { printf '%s' "$1" | tr '[:lower:]' '[:upper:]'; }
sq() { local q="'"; printf "'%s'" "${1//$q/$q\\$q$q}"; }  # 单引号包裹，值里的单引号转义

# ---------------------------------------------------------------- 1. 目录与密钥
init_dirs() {
  mkdir -p "$SECRETS"/{db,jwt,service,extra} "$ENVDIR" "$DATA"/{nats,photos,mock,work,journal}
  chmod 700 "$SECRETS"
  chown -R bike:bike "$DATA/nats" "$DATA/photos" "$DATA/mock" "$DATA/work" "$DATA/journal" "$ENVDIR"
  chmod 750 "$ENVDIR"
}

init_secrets() {
  local m
  for m in $(modules); do
    [[ -s "$SECRETS/db/$m.pass" ]] || rand_hex 16 >"$SECRETS/db/$m.pass"
    [[ -s "$SECRETS/service/$m.key" ]] || rand_hex 32 >"$SECRETS/service/$m.key"
  done
  # 令牌密钥对：Ed25519，环境变量里的值 = 原始 32 字节的标准 base64（私钥为 32 字节种子）
  local n
  for n in user admin; do
    if [[ ! -s "$SECRETS/jwt/$n.priv.b64" ]]; then
      "$BIN/bike-sim" keygen "$SECRETS/jwt" "$n"
      printf '%s' "$n-$(date +%Y%m%d)" >"$SECRETS/jwt/$n.kid"
      log "已生成 $n 令牌密钥对"
    fi
  done
  [[ -s "$SECRETS/extra/pile-sim.key" ]] || rand_hex 16 >"$SECRETS/extra/pile-sim.key"
}

# ---------------------------------------------------------------- 2. PostgreSQL
init_pg() {
  if [[ -f "$PGDATA/PG_VERSION" ]]; then log "PostgreSQL 数据目录已初始化，跳过"; else
    log "初始化 PostgreSQL 数据目录"
    mkdir -p "$PGDATA"; chown postgres:postgres "$PGDATA"; chmod 700 "$PGDATA"
    as postgres "$PGBIN/initdb" -D "$PGDATA" -U postgres --auth-local=peer --auth-host=scram-sha-256 -E UTF8 --locale=C >/tmp/initdb.log 2>&1 \
      || { cat /tmp/initdb.log; die "initdb 失败"; }
  fi
  # 每次启动都重写：监听只在容器回环；账号只能连与自己同名的库（sameuser），超级用户只能经本地套接字（peer）
  cat >"$PGDATA/pg_hba.conf" <<EOF
# TYPE  DATABASE  USER      ADDRESS        METHOD
local   all       postgres                 peer
host    sameuser  all       $LOOP/32       scram-sha-256
EOF
  cat >"$PGDATA/bike.conf" <<EOF
listen_addresses = '$LOOP'
port = $PG_PORT
password_encryption = 'scram-sha-256'
max_connections = ${BIKE_PG_MAX_CONNECTIONS:-400}
shared_buffers = ${BIKE_PG_SHARED_BUFFERS:-128MB}
unix_socket_directories = '/var/run/postgresql'
logging_collector = off
log_destination = 'stderr'
EOF
  grep -q "include_if_exists = 'bike.conf'" "$PGDATA/postgresql.conf" || printf "\ninclude_if_exists = 'bike.conf'\n" >>"$PGDATA/postgresql.conf"
  chown postgres:postgres "$PGDATA/pg_hba.conf" "$PGDATA/bike.conf"
  mkdir -p /var/run/postgresql; chown postgres:postgres /var/run/postgresql
}

psql_su() { as postgres "$PGBIN/psql" -h /var/run/postgresql -U postgres -d postgres -v ON_ERROR_STOP=1 -qtA "$@"; }

wait_pg() {
  local i
  for i in $(seq 1 60); do as postgres "$PGBIN/pg_isready" -h /var/run/postgresql -q && return 0; sleep 1; done
  die "PostgreSQL 60 秒内没起来"
}

ensure_dbs() {
  local m role pw
  for m in $(modules); do
    role="bike_$m"; pw="$(cat "$SECRETS/db/$m.pass")"
    psql_su -c "DO \$\$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='$role') THEN CREATE ROLE $role LOGIN; END IF; END\$\$;" \
            -c "ALTER ROLE $role WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD '$pw';"
    if [[ "$(psql_su -c "SELECT 1 FROM pg_database WHERE datname='$role'")" != "1" ]]; then
      psql_su -c "CREATE DATABASE $role OWNER $role ENCODING 'UTF8' TEMPLATE template0;"
      log "已建库 $role"
    fi
    psql_su -c "REVOKE ALL ON DATABASE $role FROM PUBLIC;" -c "GRANT CONNECT, TEMPORARY ON DATABASE $role TO $role;"
  done
  log "15 个库与账号就绪（bike_<模块>，账号只能连自己的库）"
}

# ---------------------------------------------------------------- 3. NATS 配置
write_nats_conf() {
  cat >"$DATA/nats.conf" <<EOF
server_name: bike-single
host: "$LOOP"
port: $NATS_PORT
http: "$LOOP:$NATS_MON"
jetstream {
  store_dir: "$DATA/nats"
  max_file_store: ${BIKE_NATS_MAX_STORE:-10G}
}
EOF
}

# ---------------------------------------------------------------- 4. 各服务 env（等价 gen-env.sh）
gen_env() {
  local m f c t var val line allowed sf
  local user_kid user_pub admin_kid admin_pub
  user_kid="$(cat "$SECRETS/jwt/user.kid")";   user_pub="$(cat "$SECRETS/jwt/user.pub.b64")"
  admin_kid="$(cat "$SECRETS/jwt/admin.kid")"; admin_pub="$(cat "$SECRETS/jwt/admin.pub.b64")"
  for m in $(modules); do
    f="$ENVDIR/$m.env"
    {
      echo "# $m 容器配置；每次启动由 bike-entrypoint 重新生成，勿手改（要改走 docker run -e 或 /data/env.d/$m.env）"
      echo "PORT=$(port_of "$m")"
      echo "LISTEN_HOST=$LOOP"
      echo "DB_DSN=$(sq "postgres://bike_$m:$(cat "$SECRETS/db/$m.pass")@$LOOP:$PG_PORT/bike_$m?sslmode=disable")"
      echo "NATS_URL=nats://$LOOP:$NATS_PORT"
      echo "USER_JWT_PUBKEYS=$(sq "$user_kid=$user_pub")"
      echo "ADMIN_JWT_PUBKEYS=$(sq "$admin_kid=$admin_pub")"
      if [[ "$m" == acct ]]; then
        echo "USER_JWT_PRIVKEY=$(sq "$(cat "$SECRETS/jwt/user.priv.b64")")"
        echo "USER_JWT_KID=$(sq "$user_kid")"
      fi
      if [[ "$m" == iam ]]; then
        echo "ADMIN_JWT_PRIVKEY=$(sq "$(cat "$SECRETS/jwt/admin.priv.b64")")"
        echo "ADMIN_JWT_KID=$(sq "$admin_kid")"
      fi
      echo "SERVICE_KEY=$(cat "$SECRETS/service/$m.key")"
      allowed=""
      for c in $(callers_of "$m"); do allowed+="${allowed:+,}$c=$(cat "$SECRETS/service/$c.key")"; done
      echo "ALLOWED_CALLERS=$(sq "$allowed")"
      for t in $(callees_of "$m"); do
        echo "$(upper "$t")_URL=http://$LOOP:$(port_of "$t")"
      done
      # 服务专有变量（extra.tsv）
      grep -v '^#' "$APP/extra.tsv" | grep -v '^[[:space:]]*$' | awk -F'\t' -v m="$m" '$1==m{print $2"\t"$3}' | while IFS=$'\t' read -r var val; do
        case "$val" in
          @secret32b64)
            sf="$SECRETS/extra/$m.$var"
            [[ -s "$sf" ]] || head -c 32 /dev/urandom | base64 -w0 >"$sf"
            val="$(cat "$sf")" ;;
          @dir:*)
            mkdir -p "${val#@dir:}"; chown bike:bike "${val#@dir:}"
            val="${val#@dir:}" ;;
        esac
        val="${val//@pilekey/$(cat "$SECRETS/extra/pile-sim.key")}"
        echo "$var=$(sq "$val")"
      done
      # 演示/运行开关：容器环境变量里以 <模块大写>_ 开头的原样带入该服务（不含 *_URL），EVENT_RETENTION_DAYS 带给全部
      while IFS='=' read -r var val; do
        case "$var" in
          "$(upper "$m")"_*) [[ "$var" == *_URL || "$var" == BIKE_* ]] || echo "$var=$(sq "$val")" ;;
        esac
      done < <(env)
      if [[ "$m" == deposit ]]; then
        while IFS='=' read -r var val; do case "$var" in CREDIT_*) echo "$var=$(sq "$val")" ;; esac; done < <(env)
      fi
      [[ -n "${EVENT_RETENTION_DAYS:-}" ]] && echo "EVENT_RETENTION_DAYS=$EVENT_RETENTION_DAYS"
      # 逃生口：/data/env.d/<模块>.env（shell 语法，KEY=VALUE），排在最后，覆盖前面的值
      [[ -f "$DATA/env.d/$m.env" ]] && cat "$DATA/env.d/$m.env"
      true
    } >"$f"
    chown bike:bike "$f"; chmod 400 "$f"
  done
  # 模拟器用的 env
  {
    echo "PILE_SIM_SECRET=$(cat "$SECRETS/extra/pile-sim.key")"
    echo "PILE_SIM_GATEWAY=http://$LOOP:$GW_PORT"
    echo "MOCK_OP_GATEWAY=http://$LOOP:$GW_PORT"
    echo "MOCK_OP_CREDS=$DATA/mock/creds.json"
    echo "PILE_SIM_STATE=$DATA/mock/pile-sim-state.json"
  } >"$ENVDIR/sim.env"
  chown bike:bike "$ENVDIR/sim.env"; chmod 400 "$ENVDIR/sim.env"
  log "已生成 $(modules | wc -l | tr -d ' ') 份服务配置到 $ENVDIR/"
}

# ---------------------------------------------------------------- 5. 进程管理
STOPPING=0
declare -A SV_PID   # 名称 -> 守护子 shell 的 pid

# sv <名称> <运行身份> <停止信号> <env 文件或空> <命令...>
# 后台起一个守护子 shell：循环拉起命令，输出加 [名称] 前缀后走容器标准输出。命令是简单命令直接后台，$! 就是真实进程，信号直达。
sv() {
  local name=$1 user=$2 sig=$3 envf=$4; shift 4
  (
    pre=(env HOME=/tmp setpriv --reuid="$user" --regid="$user" --init-groups)
    child=0; stop=0; fast=0
    trap 'stop=1; [[ $child -ne 0 ]] && kill -s '"$sig"' "$child" 2>/dev/null; true' TERM
    while [[ $stop -eq 0 ]]; do
      start=$SECONDS
      if [[ -n "$envf" ]]; then
        "${pre[@]}" bash -c 'set -a; . "$0"; set +a; cd /data/work; exec "$@"' "$envf" "$@" > >(sed -u "s/^/[$name] /") 2>&1 &
      else
        "${pre[@]}" "$@" > >(sed -u "s/^/[$name] /") 2>&1 &
      fi
      child=$!
      while kill -0 "$child" 2>/dev/null; do wait "$child" 2>/dev/null || true; done
      rc=0; wait "$child" 2>/dev/null || rc=$?
      child=0
      [[ $stop -eq 1 ]] && break
      if (( SECONDS - start < 5 )); then fast=$((fast + 1)); else fast=0; fi
      d=$(( fast > 10 ? 10 : (fast > 0 ? fast : 1) ))
      printf '[bike] %s 退出（%s），%s 秒后拉起
' "$name" "$rc" "$d" >&2
      sleep "$d" & wait $! 2>/dev/null || true
    done
  ) &
  SV_PID[$name]=$!
}

stop_sv() { # 名称...
  local n
  for n in "$@"; do [[ -n "${SV_PID[$n]:-}" ]] && kill -TERM "${SV_PID[$n]}" 2>/dev/null || true; done
  for n in "$@"; do [[ -n "${SV_PID[$n]:-}" ]] && { wait "${SV_PID[$n]}" 2>/dev/null || true; unset 'SV_PID[$n]'; }; done
}

shutdown() {
  [[ $STOPPING -eq 1 ]] && return
  STOPPING=1
  log "收到停止信号，按 服务 -> NATS -> PostgreSQL 的顺序停止"
  local m names=(gateway mock-operator pile-sim)
  for m in $(modules); do names+=("$m"); done
  stop_sv "${names[@]}"
  stop_sv nats
  stop_sv postgres
  log "已停止"
  exit 0
}

wait_http() { # url 秒数
  local i
  for i in $(seq 1 "$2"); do curl -fsS -m2 "$1" >/dev/null 2>&1 && return 0; sleep 1; done
  return 1
}

main() {
  [[ "$(id -u)" == 0 ]] || die "入口须以 root 运行（服务本身以 bike 账号运行）"
  trap shutdown TERM INT
  init_dirs
  init_secrets
  init_pg
  write_nats_conf

  # PostgreSQL 先起；postgres 收到 SIGINT 即快速关闭
  sv postgres postgres INT "" "$PGBIN/postgres" -D "$PGDATA"
  wait_pg
  ensure_dbs

  # NATS 次之
  sv nats bike TERM "" "$BIN/nats-server" -c "$DATA/nats.conf"
  wait_http "http://$LOOP:$NATS_MON/healthz?js-enabled-only=true" 60 || die "NATS JetStream 60 秒内没就绪"
  log "PostgreSQL 与 NATS 就绪，启动服务"

  gen_env
  local m
  for m in $(modules); do
    sv "$m" bike TERM "$ENVDIR/$m.env" "$BIN/$m"
  done
  sv gateway bike TERM "" env XDG_DATA_HOME=/tmp/caddy-data XDG_CONFIG_HOME=/tmp/caddy-config "$BIN/caddy" run --config "$APP/Caddyfile" --adapter caddyfile
  sv mock-operator bike TERM "$ENVDIR/sim.env" "$BIN/bike-sim" mock-operator
  sv pile-sim bike TERM "$ENVDIR/sim.env" "$BIN/bike-sim" pile-sim run

  if wait_http "http://$LOOP:$GW_PORT/healthz" 60; then log "网关就绪 :$GW_PORT；/healthz 全部服务见 /healthz/<模块>"; fi
  # 主循环：等信号（wait 被 trap 打断后进入 shutdown）
  while true; do wait || true; sleep 1 & wait $! 2>/dev/null || true; done
}

main "$@"
