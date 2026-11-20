#!/usr/bin/env bash
# 生成本机用的全套服务配置到 deploy/local/env/<模块>.env（不入库）。可重复执行：
# 已有的密钥与凭证沿用；加 --rotate 重新生成令牌密钥对与服务凭证（数据库口令由 up.sh 管理，不在此轮换）。
# 变量名照 contracts/wire.md 第 5 节；调用关系照 deploy/services.tsv。
# 用法：bash deploy/local/gen-env.sh [--rotate]
source "$(dirname "${BASH_SOURCE[0]}")/_common.sh"

ROTATE=0; [[ "${1:-}" == "--rotate" ]] && ROTATE=1
command -v openssl >/dev/null || die "需要 openssl（Git for Windows 自带）"
[[ -d "$SECRETS_DIR/db" ]] || die "没有数据库口令，先跑 bash deploy/local/up.sh"

JWT_DIR="$SECRETS_DIR/jwt"; SVC_DIR="$SECRETS_DIR/service"
mkdir -p "$JWT_DIR" "$SVC_DIR" "$ENV_DIR"
if (( ROTATE )); then rm -f "$JWT_DIR"/* "$SVC_DIR"/*; log "已清除旧的令牌密钥与服务凭证，重新生成"; fi

# 令牌密钥对：Ed25519。环境变量里的值 = 原始 32 字节的标准 base64
#   公钥：32 字节公钥；私钥：32 字节种子（Go 里用 ed25519.NewKeyFromSeed 还原）
gen_keypair() { # 名称 kid
  local n="$1"
  if [[ ! -s "$JWT_DIR/$n.priv.pem" ]]; then
    openssl genpkey -algorithm ed25519 -out "$JWT_DIR/$n.priv.pem" 2>/dev/null
    printf '%s' "$2" >"$JWT_DIR/$n.kid"
  fi
  openssl pkey -in "$JWT_DIR/$n.priv.pem" -outform DER 2>/dev/null | tail -c 32 | base64 -w0 >"$JWT_DIR/$n.priv.b64"
  openssl pkey -in "$JWT_DIR/$n.priv.pem" -pubout -outform DER 2>/dev/null | tail -c 32 | base64 -w0 >"$JWT_DIR/$n.pub.b64"
}
gen_keypair user  "user-$(date +%Y%m%d)"
gen_keypair admin "admin-$(date +%Y%m%d)"
USER_KID="$(cat "$JWT_DIR/user.kid")";   USER_PUB="$(cat "$JWT_DIR/user.pub.b64")"
ADMIN_KID="$(cat "$JWT_DIR/admin.kid")"; ADMIN_PUB="$(cat "$JWT_DIR/admin.pub.b64")"

for m in $(modules); do
  [[ -s "$SVC_DIR/$m.key" ]] || rand_hex 32 >"$SVC_DIR/$m.key"
done

port_of()    { services | awk -F'\t' -v m="$1" '$1==m{print $2}'; }
callees_of() { services | awk -F'\t' -v m="$1" '$1==m && $3!="-"{print $3}' | tr ',' ' '; }
callers_of() { services | awk -F'\t' -v m="$1" '{n=split($3,a,","); for(i=1;i<=n;i++) if(a[i]==m) print $1}'; }

for m in $(modules); do
  f="$ENV_DIR/$m.env"
  {
    echo "# $m 本机开发配置；由 deploy/local/gen-env.sh 生成，勿手改，勿入库"
    echo "PORT=$(port_of "$m")"
    echo "LISTEN_HOST=$LOCAL_HOST"
    echo "DB_DSN=postgres://bike_$m:$(cat "$SECRETS_DIR/db/$m.pass")@$LOCAL_HOST:$PG_PORT/bike_$m?sslmode=disable"
    echo "NATS_URL=nats://$LOCAL_HOST:$NATS_PORT"
    echo "USER_JWT_PUBKEYS=$USER_KID=$USER_PUB"
    echo "ADMIN_JWT_PUBKEYS=$ADMIN_KID=$ADMIN_PUB"
    if [[ "$m" == acct ]]; then
      echo "USER_JWT_PRIVKEY=$(cat "$JWT_DIR/user.priv.b64")"
      echo "USER_JWT_KID=$USER_KID"
    fi
    if [[ "$m" == iam ]]; then
      echo "ADMIN_JWT_PRIVKEY=$(cat "$JWT_DIR/admin.priv.b64")"
      echo "ADMIN_JWT_KID=$ADMIN_KID"
    fi
    echo "SERVICE_KEY=$(cat "$SVC_DIR/$m.key")"
    allowed=""
    for c in $(callers_of "$m"); do allowed+="${allowed:+,}$c=$(cat "$SVC_DIR/$c.key")"; done
    echo "ALLOWED_CALLERS=$allowed"
    for t in $(callees_of "$m"); do
      echo "$(printf '%s' "$t" | tr '[:lower:]' '[:upper:]')_URL=http://$LOCAL_HOST:$(port_of "$t")"
    done
    # 服务专有变量（deploy/local/extra.tsv）
    grep -v '^#' "$LOCAL_DIR/extra.tsv" | awk -F'\t' -v m="$m" '$1==m{print $2"\t"$3}' | while IFS=$'\t' read -r var val; do
      case "$val" in
        @secret32b64)
          mkdir -p "$SECRETS_DIR/extra"
          sf="$SECRETS_DIR/extra/$m.$var"
          [[ -s "$sf" ]] || head -c 32 /dev/urandom | base64 -w0 >"$sf"
          val="$(cat "$sf")" ;;
        @dir:*)
          mkdir -p "$DATA_DIR/${val#@dir:}"
          val="$(winpath "$DATA_DIR/${val#@dir:}")" ;;
      esac
      echo "$var=$val"
    done
  } >"$f"
done

log "已生成 $(modules | wc -l | tr -d ' ') 份配置到 deploy/local/env/"
for m in $(modules); do
  printf '  %-8s 调用：%-22s 允许调用方：%s\n' "$m" "$(callees_of "$m" | tr ' ' ',' )" "$(callers_of "$m" | paste -sd, -)"
done
