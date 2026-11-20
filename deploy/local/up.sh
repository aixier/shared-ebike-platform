#!/usr/bin/env bash
# 本机开发：一条命令起 PostgreSQL 16（15 个库与 15 个账号）和 NATS JetStream。可重复执行。
# 用法：bash deploy/local/up.sh
source "$(dirname "${BASH_SOURCE[0]}")/_common.sh"

mkdir -p "$BIN_DIR" "$DATA_DIR" "$LOG_DIR" "$SECRETS_DIR/db" "$RUN_DIR"

# ---------- 1. 下载 ----------
install_pg() {
  if [[ -x "$PG_BIN/postgres.exe" && -x "$PG_BIN/psql.exe" && -d "$PG_HOME/share/timezonesets" ]]; then log "PostgreSQL $PG_VERSION 已在 bin/，跳过下载"; return; fi
  local zip="$BIN_DIR/_dl/postgresql-$PG_VERSION-1-windows-x64-binaries.zip"
  [[ -f "$zip" ]] || fetch "https://get.enterprisedb.com/postgresql/postgresql-$PG_VERSION-1-windows-x64-binaries.zip" "$zip"
  log "解压 PostgreSQL（整包解压后删去 pgAdmin、调试符号与文档）"
  rm -rf "$PG_HOME"
  unzip -q -o "$zip" -d "$BIN_DIR"
  rm -rf "$PG_HOME/pgAdmin 4" "$PG_HOME/symbols" "$PG_HOME/doc" "$PG_HOME/StackBuilder" "$PG_HOME/include"
  [[ -d "$PG_HOME/share/timezonesets" && -x "$PG_BIN/initdb.exe" ]] || die "PostgreSQL 包不完整，已保留 $zip 供排查"
  rm -f "$zip"
  "$PG_BIN/postgres.exe" --version
}

install_nats() {
  if [[ -x "$NATS_EXE" ]]; then log "nats-server $NATS_VERSION 已在 bin/，跳过下载"; return; fi
  local name="nats-server-v$NATS_VERSION-windows-amd64"
  local base="https://github.com/nats-io/nats-server/releases/download/v$NATS_VERSION"
  local dl="$BIN_DIR/_dl"
  fetch "$base/$name.zip" "$dl/$name.zip"
  fetch "$base/SHA256SUMS" "$dl/nats.SHA256SUMS"
  local want got
  want="$(grep " $name.zip\$" "$dl/nats.SHA256SUMS" | awk '{print $1}')"
  got="$(sha256sum "$dl/$name.zip" | awk '{print $1}')"
  [[ -n "$want" && "$want" == "$got" ]] || die "nats-server 校验值不符（期望 $want，实际 $got）"
  rm -rf "$dl/nats-x" && unzip -q -o "$dl/$name.zip" -d "$dl/nats-x"
  mkdir -p "$(dirname "$NATS_EXE")"
  mv "$(find "$dl/nats-x" -name nats-server.exe | head -1)" "$NATS_EXE"
  rm -rf "$dl/nats-x" "$dl/$name.zip" "$dl/nats.SHA256SUMS"
  "$NATS_EXE" --version
}

# ---------- 2. 账号口令（本机生成，不入库） ----------
ensure_passwords() {
  [[ -s "$SECRETS_DIR/pg_superuser.pass" ]] || rand_hex 16 > "$SECRETS_DIR/pg_superuser.pass"
  local m
  for m in $(modules); do
    [[ -s "$SECRETS_DIR/db/$m.pass" ]] || rand_hex 16 > "$SECRETS_DIR/db/$m.pass"
  done
}

# ---------- 3. 初始化数据目录 ----------
init_pg() {
  if [[ -f "$PG_DATA/PG_VERSION" ]]; then log "PostgreSQL 数据目录已初始化，跳过"; return; fi
  log "初始化 PostgreSQL 数据目录"
  "$PG_BIN/initdb.exe" -D "$(winpath "$PG_DATA")" -U postgres \
    --pwfile="$(winpath "$SECRETS_DIR/pg_superuser.pass")" \
    --auth=scram-sha-256 -E UTF8 --locale=C >"$LOG_DIR/initdb.log" 2>&1 \
    || { cat "$LOG_DIR/initdb.log"; die "initdb 失败"; }
  cat >>"$PG_DATA/postgresql.conf" <<EOF

# ---- deploy/local/up.sh 追加 ----
listen_addresses = '$LOCAL_HOST'
port = $PG_PORT
password_encryption = 'scram-sha-256'
logging_collector = off
EOF
  # 超级用户只用于建库建号；每个服务账号只能连与自己同名的库（sameuser）
  cat >"$PG_DATA/pg_hba.conf" <<EOF
# TYPE  DATABASE  USER      ADDRESS        METHOD
host    all       postgres  $LOCAL_HOST/32  scram-sha-256
host    sameuser  all       $LOCAL_HOST/32  scram-sha-256
EOF
}

start_pg() {
  if "$PG_BIN/pg_ctl.exe" status -D "$(winpath "$PG_DATA")" >/dev/null 2>&1; then log "PostgreSQL 已在运行"; return; fi
  port_bindable "$PG_PORT" || die "端口 $PG_PORT 已被别的程序占用（可设 BIKE_PG_PORT 换端口）"
  log "启动 PostgreSQL（$LOCAL_HOST:$PG_PORT）"
  "$PG_BIN/pg_ctl.exe" start -D "$(winpath "$PG_DATA")" -o "-p $PG_PORT -c listen_addresses=$LOCAL_HOST" -l "$(winpath "$LOG_DIR/postgresql.log")" -w -t 60 </dev/null >/dev/null 2>&1 \
    || { tail -20 "$LOG_DIR/postgresql.log"; die "PostgreSQL 启动失败"; }
}

psql_super() { PGPASSWORD="$(cat "$SECRETS_DIR/pg_superuser.pass")" "$PG_BIN/psql.exe" -U postgres -d postgres -v ON_ERROR_STOP=1 -qtA "$@"; }

ensure_dbs() {
  local m role pw
  for m in $(modules); do
    role="bike_$m"; pw="$(cat "$SECRETS_DIR/db/$m.pass")"
    psql_super -c "DO \$\$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='$role') THEN CREATE ROLE $role LOGIN; END IF; END\$\$;" \
               -c "ALTER ROLE $role WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD '$pw';"
    if [[ "$(psql_super -c "SELECT 1 FROM pg_database WHERE datname='$role'")" != "1" ]]; then
      psql_super -c "CREATE DATABASE $role OWNER $role ENCODING 'UTF8' TEMPLATE template0;"
      log "已建库 $role"
    fi
    psql_super -c "REVOKE ALL ON DATABASE $role FROM PUBLIC;" -c "GRANT CONNECT, TEMPORARY ON DATABASE $role TO $role;"
  done
  log "15 个库与账号就绪（bike_<模块>，账号只能连自己的库）"
}

# ---------- 4. NATS JetStream ----------
start_nats() {
  mkdir -p "$NATS_STORE"
  local conf="$DATA_DIR/nats.conf"
  cat >"$conf" <<EOF
# 由 deploy/local/up.sh 生成，勿手改
server_name: bike-local
host: "$LOCAL_HOST"
port: $NATS_PORT
http: "$LOCAL_HOST:$NATS_MONITOR_PORT"
pid_file: "$(winpath "$RUN_DIR/nats.pid")"
log_file: "$(winpath "$LOG_DIR/nats.log")"
jetstream {
  store_dir: "$(winpath "$NATS_STORE")"
  max_file_store: 10G
}
EOF
  if port_in_use "$NATS_PORT"; then
    curl -fs "http://$LOCAL_HOST:$NATS_MONITOR_PORT/healthz?js-enabled-only=true" >/dev/null \
      && { log "NATS 已在运行"; return; } || die "端口 $NATS_PORT 已被别的程序占用"
  fi
  port_bindable "$NATS_PORT" && port_bindable "$NATS_MONITOR_PORT" || die "端口 $NATS_PORT 或 $NATS_MONITOR_PORT 已被别的程序占用"
  log "启动 NATS JetStream（$LOCAL_HOST:$NATS_PORT，监控 $LOCAL_HOST:$NATS_MONITOR_PORT）"
  powershell.exe -NoProfile -Command "Start-Process -FilePath '$(winpath "$NATS_EXE")' -ArgumentList '-c','\"$(winpath "$conf")\"' -WindowStyle Hidden" </dev/null >/dev/null 2>&1 \
    || die "无法启动 nats-server"
  local i
  for i in $(seq 1 30); do
    curl -fs "http://$LOCAL_HOST:$NATS_MONITOR_PORT/healthz?js-enabled-only=true" >/dev/null && return
    sleep 0.5
  done
  tail -20 "$LOG_DIR/nats.log" 2>/dev/null || true
  die "NATS 未就绪"
}

install_pg
install_nats
ensure_passwords
init_pg
start_pg
ensure_dbs
start_nats

log "就绪：PostgreSQL $LOCAL_HOST:$PG_PORT，NATS nats://$LOCAL_HOST:$NATS_PORT"
log "下一步：bash deploy/local/gen-env.sh 生成各服务配置；bash deploy/local/verify.sh 自检"
