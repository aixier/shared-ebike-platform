#!/bin/sh
# 构建阶段用：编译 bike-sim 与 15 个服务到 /out。构建机访问 goproxy.cn 偶发超时，整体重试；已下载的模块在缓存卷里不会重复下载。
set -u
build_all() {
  (cd /src/sim && go build -trimpath -ldflags="-s -w" -o /out/bike-sim .) || return 1
  grep -v '^#' /tmp/services.tsv | grep -v '^[[:space:]]*$' | cut -f1 | tr -d '\r' | \
    xargs -P 3 -I{} sh -c 'cd /src/backend/{} && go build -trimpath -ldflags="-s -w" -o /out/{} ./cmd/{}'
}
mkdir -p /out
i=1
while [ "$i" -le 25 ]; do
  build_all && { ls -la /out; exit 0; }
  echo "第 $i 次构建失败，5 秒后重试"; i=$((i + 1)); sleep 5
done
exit 1
