#!/usr/bin/env bash
# Copyright (c) 2025-now SuInk.
# Licensed under the Limited Redistribution License in the repository root.
#
# 把官方发布的 gitea-mcp 取下来放进产物目录：版本和每个平台的 SHA-256 都钉死在
# 这里，对不上就直接失败。打包别人的二进制等于把他们的发布链路接进我们的供应链，
# 钉版本加校验是唯一能让这件事可复现的办法——上游改了同名资产我们会当场发现，
# 而不是悄悄把一个没人看过的东西发给用户。
#
# 用法：scripts/fetch-gitea-mcp.sh <goos> <goarch> <目标目录>

set -euo pipefail

GITEA_MCP_VERSION="1.7.0"

# gitea-mcp_1.7.0_checksums.txt 的原文，只保留 Diana 会发布的平台。
checksum_for() {
  case "$1" in
    gitea-mcp_Linux_x86_64.tar.gz) echo bbc9a7b462facd3c56b1558ee6054e91f2fca27a2878b5599afddcf57d446b8d ;;
    gitea-mcp_Linux_arm64.tar.gz) echo a8250ccd99cd220af4b1d8a1146fa72052fa12e5bc155a55223c85d58a50eadd ;;
    gitea-mcp_Darwin_x86_64.tar.gz) echo f78fedd6cddd779f146e5ff5ced8bb60c012dc3da0f4b7fcedb25af1ec468074 ;;
    gitea-mcp_Darwin_arm64.tar.gz) echo 295c83b49238b7f81ceadd1aa49b8cbd948172aad5b6ca29413fec148a07f00e ;;
    gitea-mcp_Windows_x86_64.zip) echo 9698fdd23d684d34c31cd87ec37290563ebb230d279efeac8d3a296927bc55c8 ;;
    *) return 1 ;;
  esac
}

asset_for() {
  case "$1/$2" in
    linux/amd64) echo gitea-mcp_Linux_x86_64.tar.gz ;;
    linux/arm64) echo gitea-mcp_Linux_arm64.tar.gz ;;
    darwin/amd64) echo gitea-mcp_Darwin_x86_64.tar.gz ;;
    darwin/arm64) echo gitea-mcp_Darwin_arm64.tar.gz ;;
    windows/amd64) echo gitea-mcp_Windows_x86_64.zip ;;
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
  echo "fetch-gitea-mcp: 上游没有发布 ${goos}/${goarch} 的二进制" >&2
  exit 1
}
want=$(checksum_for "$asset")
url="https://gitea.com/gitea/gitea-mcp/releases/download/v${GITEA_MCP_VERSION}/${asset}"

binary="gitea-mcp"
if [ "$goos" = "windows" ]; then
  binary="gitea-mcp.exe"
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

echo "fetch-gitea-mcp: 下载 ${asset}（v${GITEA_MCP_VERSION}）"
curl -fsSL --retry 3 --retry-delay 2 -o "$work/$asset" "$url"

got=$(sha256sum "$work/$asset" 2>/dev/null | cut -d' ' -f1 || shasum -a 256 "$work/$asset" | cut -d' ' -f1)
if [ "$got" != "$want" ]; then
  echo "fetch-gitea-mcp: ${asset} 校验和不符，期望 ${want}，实际 ${got}" >&2
  exit 1
fi

case "$asset" in
  *.tar.gz) tar -xzf "$work/$asset" -C "$work" ;;
  *.zip) unzip -q "$work/$asset" -d "$work" ;;
esac

mkdir -p "$dest"
install -m 0755 "$work/$binary" "$dest/$binary"
# MIT 要求随二进制附上许可证和版权声明，压缩包里本来就有，原样带走。
install -m 0644 "$work/LICENSE" "$dest/gitea-mcp.LICENSE"
echo "fetch-gitea-mcp: 已放好 ${dest}/${binary}"
