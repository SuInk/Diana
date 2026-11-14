#!/bin/sh
# Copyright (c) 2025-now SuInk.
# Licensed under the Limited Redistribution License in the repository root.

# 容器入口：以 root 起步，只做一件事——把挂进来的数据目录交给运行用户，
# 然后降权到 diana 再启动主程序。
#
# Linux 上 bind mount 的宿主机目录不存在时由 Docker 以 root:root 0755 创建，
# 旧版镜像以 root 运行时写下的文件也归 root；运行用户 diana（10001）对它们没有
# 写权限，SQLite 和日志都起不来。这里只在目录属主不对时递归修正一次，已经正确的
# 部署不会每次启动都扫一遍。
#
# 用 --user / compose 的 user: 指定了非 root 用户时直接启动：权限由部署方自己负责。
set -eu

if [ "$(id -u)" = 0 ]; then
  for dir in /app/data /app/data/home; do
    mkdir -p "$dir" 2>/dev/null || true
    if [ "$(stat -c %u:%g "$dir")" != "10001:10001" ]; then
      # NFS root_squash 等场景 root 也改不了属主：照常启动，由主程序报出
      # 具体哪个文件写不进去，而不是在这里静默退出。
      chown -R diana:diana "$dir" ||
        printf 'diana-entrypoint: cannot hand %s to uid 10001; make it writable by that user on the host\n' "$dir" >&2
    fi
  done
  # Docker 按 root 设的 HOME 是 /root；编码 CLI 的登录态要落在数据卷里。
  export HOME=/app/data/home USER=diana LOGNAME=diana
  # 工作区容器：挂进来的 rootless Docker socket 归宿主机上那个用户的组（0660），
  # 降权时把这个组加给 diana，挂上 socket 就能用，不用另做镜像或改宿主权限。
  box_host=${DIANA_BOX_DOCKER_HOST:-}
  box_socket=${box_host#unix://}
  if [ -n "$box_host" ] && [ "$box_socket" != "$box_host" ] && [ -S "$box_socket" ]; then
    exec setpriv --reuid=diana --regid=diana --groups="$(stat -c %g "$box_socket")" -- "$@"
  fi
  exec setpriv --reuid=diana --regid=diana --init-groups -- "$@"
fi
exec "$@"
