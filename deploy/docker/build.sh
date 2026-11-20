#!/usr/bin/env bash
# 构建单镜像：版本号取 deploy/local/versions.env（与本机联调同版本），上下文是项目根目录。
# 用法：bash deploy/docker/build.sh [镜像名，默认 bike-single:local]
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
ver() { sed -n "s/^$1=\\(.*\\)\$/\\1/p" "$ROOT/deploy/local/versions.env" | tr -d '\r'; }
NATS_VERSION="$(ver NATS_VERSION)"; CADDY_VERSION="$(ver CADDY_VERSION)"
[[ -n "$NATS_VERSION" && -n "$CADDY_VERSION" ]] || { echo "读不到 versions.env 里的 NATS_VERSION / CADDY_VERSION" >&2; exit 1; }
# 在宿主机下载 nats-server 与 Caddy 发行包及官方校验文件（断点续传，已有则跳过），Dockerfile 里再核对一次
DL="$HERE/.dl"; mkdir -p "$DL"
fetch() { # url 文件名
  [[ -s "$DL/$2" ]] && return 0
  local n; for n in $(seq 1 30); do
    curl -fL --retry 3 --retry-all-errors --connect-timeout 20 -C - -o "$DL/$2.part" "$1" && { mv "$DL/$2.part" "$DL/$2"; return 0; }
  done; echo "下载失败：$1" >&2; exit 1
}
n="nats-server-v$NATS_VERSION-linux-amd64"; c="caddy_${CADDY_VERSION}_linux_amd64"
fetch "https://github.com/nats-io/nats-server/releases/download/v$NATS_VERSION/$n.tar.gz" "$n.tar.gz"
fetch "https://github.com/nats-io/nats-server/releases/download/v$NATS_VERSION/SHA256SUMS" nats.SHA256SUMS
fetch "https://github.com/caddyserver/caddy/releases/download/v$CADDY_VERSION/$c.tar.gz" "$c.tar.gz"
fetch "https://github.com/caddyserver/caddy/releases/download/v$CADDY_VERSION/caddy_${CADDY_VERSION}_checksums.txt" caddy.checksums
(cd "$DL" && grep " $n.tar.gz\$" nats.SHA256SUMS | sha256sum -c - && grep " $c.tar.gz\$" caddy.checksums | sha512sum -c -) || { echo "校验不符，删掉 $DL 重来" >&2; exit 1; }
cd "$ROOT"
DOCKER_BUILDKIT=1 docker build -f deploy/docker/Dockerfile \
  --build-arg NATS_VERSION="$NATS_VERSION" --build-arg CADDY_VERSION="$CADDY_VERSION" \
  ${BIKE_GO_IMAGE:+--build-arg GO_IMAGE="$BIKE_GO_IMAGE"} ${BIKE_RUN_IMAGE:+--build-arg RUN_IMAGE="$BIKE_RUN_IMAGE"} \
  -t "${1:-bike-single:local}" .
docker images "${1:-bike-single:local}" --format '镜像 {{.Repository}}:{{.Tag}}  {{.Size}}'
