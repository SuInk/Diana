#!/bin/sh
# Copyright (c) 2025-now SuInk.
# Licensed under the Limited Redistribution License in the repository root.

set -eu

fail() { printf 'Diana Docker: %s\n' "$*" >&2; exit 1; }
command -v curl >/dev/null 2>&1 || fail '请先安装 curl。'
command -v docker >/dev/null 2>&1 || fail '请先安装并启动 Docker（含 Compose）。'
docker compose version >/dev/null 2>&1 || fail '需要 Docker Compose v2。'
docker info >/dev/null 2>&1 || fail '无法连接 Docker，请启动 Docker 并确认当前用户有访问权限。'

# 在当前目录部署；重复执行保留用户修改过的 Compose 和 seccomp 配置。
# 显式指定配置文件，避免误用当前目录或父目录中的其他 Compose 项目。
base=https://raw.githubusercontent.com/SuInk/Diana/main
stage=$(mktemp -d "${TMPDIR:-/tmp}/diana-docker.XXXXXX")
trap 'rm -rf "$stage"' EXIT
trap 'exit 1' HUP INT TERM
if [ ! -e docker-compose.yml ]; then
  curl -fsSL "$base/docker-compose.yml" -o "$stage/docker-compose.yml"
fi
if [ ! -e scripts/docker/chromium-seccomp.json ]; then
  curl -fsSL "$base/scripts/docker/chromium-seccomp.json" -o "$stage/chromium-seccomp.json"
fi
# 所有下载成功后才写入部署目录，下载失败不会留下半个配置文件。
if [ -f "$stage/docker-compose.yml" ]; then
  mv "$stage/docker-compose.yml" docker-compose.yml
fi
if [ -f "$stage/chromium-seccomp.json" ]; then
  mkdir -p scripts/docker
  mv "$stage/chromium-seccomp.json" scripts/docker/chromium-seccomp.json
fi
# 镜像变体：默认完整版（预装 Chromium、字体、ffmpeg、yt-dlp、tesseract）。
# DIANA_VARIANT=slim 换成轻量版，拉取体积约为五分之一，但没有浏览器、媒体和 OCR。
# 变体写进部署目录的 .env，compose 会自己读，以后 pull 和 up 都跟着走，不用每次带变量。
variant=${DIANA_VARIANT:-}
if [ -n "$variant" ]; then
  case "$variant" in
    full) image=ghcr.io/suink/diana:latest ;;
    slim) image=ghcr.io/suink/diana:latest-slim ;;
    *) fail "DIANA_VARIANT 只认 full 或 slim，收到：$variant" ;;
  esac
  # 只改 DIANA_IMAGE 这一行，用户 .env 里的其他内容原样保留。
  if [ -f .env ]; then
    grep -v '^DIANA_IMAGE=' .env > "$stage/env" || true
  else
    : > "$stage/env"
  fi
  printf 'DIANA_IMAGE=%s\n' "$image" >> "$stage/env"
  mv "$stage/env" .env
  printf 'Diana Docker: 使用 %s 变体（%s）\n' "$variant" "$image"
fi

docker compose -f docker-compose.yml config --quiet
docker compose -f docker-compose.yml pull
docker compose -f docker-compose.yml up -d
printf '\n%s\n' 'Diana 已启动。默认控制台：http://localhost:18080' \
  '查看账号密码：docker compose -f docker-compose.yml logs diana' \
  '以后在此目录更新：docker compose -f docker-compose.yml pull && docker compose -f docker-compose.yml up -d'
