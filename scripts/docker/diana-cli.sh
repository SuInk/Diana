#!/bin/sh
# Copyright (c) 2025-now SuInk.
# Licensed under the Limited Redistribution License in the repository root.

# 容器里的 diana 命令：docker exec <容器> diana status 直接可用。
#
# docker exec 默认是 root，而服务以 diana（10001）运行。root 身份执行时先降权，
# 免得命令以 root 在数据目录里新建文件（如 SQLite 的 -wal/-shm），服务之后写不进去。
set -eu

if [ "$(id -u)" = 0 ]; then
  export HOME=/app/data/home USER=diana LOGNAME=diana
  exec setpriv --reuid=diana --regid=diana --init-groups -- /app/diana-webui "$@"
fi
exec /app/diana-webui "$@"
