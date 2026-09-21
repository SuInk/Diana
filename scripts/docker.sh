#!/bin/sh
# Copyright (c) 2025-now SuInk.
# Licensed under the Limited Redistribution License in the repository root.

set -eu

fail() { printf 'Diana Docker: %s\n' "$*" >&2; exit 1; }
command -v curl >/dev/null 2>&1 || fail '请先安装 curl。'
command -v docker >/dev/null 2>&1 || fail '请先安装并启动 Docker（含 Compose）。'
docker compose version >/dev/null 2>&1 || fail '需要 Docker Compose v2。'
docker info >/dev/null 2>&1 || fail '无法连接 Docker，请启动 Docker 并确认当前用户有访问权限。'

image_full=ghcr.io/suink/diana:latest
image_slim=ghcr.io/suink/diana:latest-slim

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

# 镜像选择。Compose 读的是 .env 里的 DIANA_IMAGE，这里负责把它定下来。
# 完整版预装 Chromium、Noto CJK 字体、ffmpeg、yt-dlp 与 tesseract；
# 基础版都不装，体积约为完整版的五分之一。
current=''
if [ -f .env ]; then
  current=$(sed -n 's/^[[:space:]]*DIANA_IMAGE[[:space:]]*=[[:space:]]*//p' .env | tail -n 1)
fi

variant=${DIANA_VARIANT:-}
case "$variant" in
  '' | full | slim) ;;
  *) fail "DIANA_VARIANT 只能是 full 或 slim，收到的是 '$variant'。" ;;
esac

if [ -z "$variant" ] && [ -n "$current" ]; then
  # 用户手填过一个非官方镜像时一律不问也不改——那是他自己的选择，
  # 问了只会给出两个都不对的选项。要换回来就删掉 .env 里那一行。
  case "$current" in
    "$image_full" | "$image_slim") ;;
    *)
      variant=keep
      printf '%s\n' "沿用 .env 里已有的镜像：$current"
      ;;
  esac
fi

# 只在交互终端里问。管道执行（curl … | sh）不提问：已经部署过的沿用 .env 里那一个，
# 首次部署用完整版。既有的自动化脚本和文档里那条一键命令不会因此卡住等输入，
# 重复执行也不会把装着基础版的部署悄悄换成完整版。
if [ -z "$variant" ] && [ -t 0 ]; then
  default=1
  default_label='完整版'
  if [ "$current" = "$image_slim" ]; then
    default=2
    default_label='基础版'
  fi
  printf '%s\n' \
    '' \
    '选择镜像：' \
    '  1) 完整版 —— 预装 Chromium、中文字体、ffmpeg、yt-dlp、tesseract，网页渲染、截图、媒体下载和 OCR 开箱可用' \
    '  2) 基础版 —— 以上都不装，体积约为完整版的五分之一，适合不需要这些能力的部署'
  if [ -n "$current" ]; then
    printf '%s\n' "当前部署用的是$default_label，直接回车保持不变。"
  fi
  printf '请输入 1 或 2 [%s]: ' "$default"
  reply=''
  read -r reply || reply=''
  [ -n "$reply" ] || reply=$default
  case "$reply" in
    1) variant=full ;;
    2) variant=slim ;;
    *) fail "无法识别的选项 '$reply'，请重新运行并输入 1 或 2。" ;;
  esac
fi

if [ -z "$variant" ] && [ -n "$current" ]; then
  variant=keep
  printf '%s\n' "沿用 .env 里已有的镜像：$current（改用另一种：在终端里重新运行本脚本，或设 DIANA_VARIANT=full|slim）"
fi

[ -n "$variant" ] || variant=full

if [ "$variant" != keep ]; then
  if [ "$variant" = slim ]; then
    image=$image_slim
  else
    image=$image_full
  fi
  # 保留 .env 里的其它变量，只改 DIANA_IMAGE 这一行。
  if [ -f .env ]; then
    grep -v '^[[:space:]]*DIANA_IMAGE[[:space:]]*=' .env > "$stage/env" || true
  else
    : > "$stage/env"
  fi
  printf 'DIANA_IMAGE=%s\n' "$image" >> "$stage/env"
  mv "$stage/env" .env
  printf '%s\n' "镜像：$image（改用另一种：重新运行本脚本，或直接改 .env 里的 DIANA_IMAGE）"
fi

docker compose -f docker-compose.yml config --quiet
docker compose -f docker-compose.yml pull
docker compose -f docker-compose.yml up -d
printf '\n%s\n' 'Diana 已启动。默认控制台：http://localhost:18080' \
  '查看账号密码：docker compose -f docker-compose.yml logs diana' \
  '以后在此目录更新：docker compose -f docker-compose.yml pull && docker compose -f docker-compose.yml up -d'
