#!/usr/bin/env bash
# Copyright (c) 2025-now SuInk.
# Licensed under the Limited Redistribution License in the repository root.
#
# 把官方发布的 yt-dlp 取下来放进产物目录：版本和每个平台的 SHA-256 都钉死在这里，
# 对不上就直接失败，理由同 fetch-gitea-mcp.sh。
#
# 为什么不用发行版的包：yt-dlp 跟着各视频站的反爬改动走，几周不更新就解析不动，
# 而 Debian stable 会把它冻在发布那天的版本（bookworm 冻在 2023.03）。官方发的是
# PyInstaller 打好的自包含二进制，不需要容器里有 Python。
#
# 升级方式：改 YT_DLP_VERSION，再把该版本 SHA2-256SUMS 里对应两行的校验和抄进来。
#
# 用法：scripts/fetch-yt-dlp.sh <goos> <goarch> <目标目录>

set -euo pipefail

YT_DLP_VERSION="2026.08.19"

# https://github.com/yt-dlp/yt-dlp/releases/download/<版本>/SHA2-256SUMS 的原文，
# 只保留 Diana 镜像会用到的平台。
checksum_for() {
  case "$1" in
    yt-dlp_linux) echo 58162f9bfdc27458ea47bfcb311cf47028f17d8154a8bf7d689861d46399230a ;;
    yt-dlp_linux_aarch64) echo b16e4dab368a816cd05d477d698a605a6ae87ccee1c8ffd38fa21d7254141fcc ;;
    *) return 1 ;;
  esac
}

asset_for() {
  case "$1/$2" in
    linux/amd64) echo yt-dlp_linux ;;
    linux/arm64) echo yt-dlp_linux_aarch64 ;;
    *) return 1 ;;
  esac
}

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <goos> <goarch> <dest-dir>" >&2
  exit 2
fi

goos="$1"
goarch="$2"
dest="$3"

asset=$(asset_for "$goos" "$goarch") || {
  echo "fetch-yt-dlp: 上游没有发布 ${goos}/${goarch} 的二进制" >&2
  exit 1
}
want=$(checksum_for "$asset")
url="https://github.com/yt-dlp/yt-dlp/releases/download/${YT_DLP_VERSION}/${asset}"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

echo "fetch-yt-dlp: 下载 ${asset}（${YT_DLP_VERSION}）"
curl -fsSL --retry 3 --retry-delay 2 -o "$work/$asset" "$url"

got=$(sha256sum "$work/$asset" 2>/dev/null | cut -d' ' -f1 || shasum -a 256 "$work/$asset" | cut -d' ' -f1)
if [ "$got" != "$want" ]; then
  echo "fetch-yt-dlp: ${asset} 校验和不符，期望 ${want}，实际 ${got}" >&2
  exit 1
fi

mkdir -p "$dest"
install -m 0755 "$work/$asset" "$dest/yt-dlp"
echo "fetch-yt-dlp: 已放好 ${dest}/yt-dlp"
