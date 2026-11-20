# syntax=docker/dockerfile:1
# 共享电单车监管平台 · 单镜像部署（演示/单机交付形态）：15 个服务 + PostgreSQL 16 + NATS JetStream + Caddy 网关 + 前端静态页。
# 构建上下文是项目根目录（共享单车/），构建命令见 deploy/docker/README.md：
#   docker build -f deploy/docker/Dockerfile -t bike-single:local .
# nats-server 与 Caddy 的版本与 deploy/local/versions.env 一致（build.sh 会读它传进来），下载后核对官方校验值。

ARG GO_IMAGE=golang:1.25-bookworm
ARG RUN_IMAGE=ubuntu:24.04
ARG NATS_VERSION=2.15.3
ARG CADDY_VERSION=2.11.9

# ---------------------------------------------------------------- 1. 交叉编译：15 个服务 + bike-sim，linux/amd64 静态二进制
FROM ${GO_IMAGE} AS build
ENV GOPROXY=https://goproxy.cn,https://mirrors.aliyun.com/goproxy/,direct \
    GOTOOLCHAIN=local \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64
WORKDIR /src
COPY deploy/services.tsv /tmp/services.tsv
COPY backend ./backend
COPY deploy/docker/sim ./sim
COPY deploy/docker/build-go.sh /tmp/build-go.sh
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    sed -i 's/\r$//' /tmp/build-go.sh && sh /tmp/build-go.sh

# ---------------------------------------------------------------- 2. 下载 nats-server 与 Caddy 发行包并核对校验值
FROM ${GO_IMAGE} AS fetch
ARG NATS_VERSION
ARG CADDY_VERSION
WORKDIR /dl
# 发行包与校验文件由 build.sh 在宿主机下载到 deploy/docker/.dl/（构建容器访问 GitHub 常超时）；这里照官方校验值再核对一遍
COPY deploy/docker/.dl/ /dl/
RUN set -eux; \
    n="nats-server-v${NATS_VERSION}-linux-amd64"; \
    grep " $n.tar.gz$" nats.SHA256SUMS | sha256sum -c -; \
    mkdir nx; tar -xzf "$n.tar.gz" -C nx; install -m 0755 "nx/$n/nats-server" /opt-nats-server; \
    c="caddy_${CADDY_VERSION}_linux_amd64"; \
    grep " $c.tar.gz$" caddy.checksums | sha512sum -c -; \
    mkdir cx; tar -xzf "$c.tar.gz" -C cx caddy; install -m 0755 cx/caddy /opt-caddy; \
    /opt-nats-server --version; /opt-caddy version

# ---------------------------------------------------------------- 3. 运行镜像
FROM ${RUN_IMAGE}
ENV DEBIAN_FRONTEND=noninteractive LANG=C.UTF-8
# PostgreSQL 16 用发行版包（Ubuntu 24.04 自带 16.x）；不建默认集群，数据目录由入口脚本在 /data/pg 初始化
RUN set -eux; \
    mkdir -p /etc/postgresql-common; echo "create_main_cluster = false" > /etc/postgresql-common/createcluster.conf; \
    apt-get update; \
    apt-get install -y --no-install-recommends postgresql-16 curl ca-certificates tzdata; \
    rm -rf /var/lib/apt/lists/* /usr/share/doc/* /usr/share/man/* /var/cache/apt/*; \
    useradd --system --uid 10001 --no-create-home --shell /usr/sbin/nologin bike; \
    mkdir -p /opt/bike/bin /srv/web /data

COPY --from=build /out/ /opt/bike/bin/
COPY --from=fetch /opt-nats-server /opt/bike/bin/nats-server
COPY --from=fetch /opt-caddy /opt/bike/bin/caddy
COPY deploy/services.tsv /opt/bike/services.tsv
COPY deploy/docker/extra.tsv /opt/bike/extra.tsv
COPY deploy/docker/Caddyfile /opt/bike/Caddyfile
COPY deploy/docker/bike-entrypoint.sh /usr/local/bin/bike-entrypoint
COPY deploy/docker/bike-healthcheck.sh /usr/local/bin/bike-healthcheck
COPY 原型/ /srv/web/
RUN set -eux; \
    sed -i 's/\r$//' /opt/bike/services.tsv /opt/bike/extra.tsv /opt/bike/Caddyfile /usr/local/bin/bike-entrypoint /usr/local/bin/bike-healthcheck; \
    chmod 0755 /usr/local/bin/bike-entrypoint /usr/local/bin/bike-healthcheck

VOLUME ["/data"]
EXPOSE 18300
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=30s --timeout=15s --start-period=120s --retries=3 CMD ["/usr/local/bin/bike-healthcheck"]
ENTRYPOINT ["/usr/local/bin/bike-entrypoint"]
