#!/usr/bin/env bash
# 检查没有任何调用方的代码：函数（deadcode，连测试都不调用）和常量、变量、类型、
# 字段（staticcheck U1000）。分别在 darwin、linux、windows 下分析，deadcode 再加一遍
# browser_integration 标签，只报各口径都认定没人用的，避免把某个平台专用的代码当死代码。
#
# 重构后旧路径往往还被测试调用着、看起来活着，一攒就是几千行；这道检查在 CI 里拦住
# 新增的死代码。确实需要保留的（例如只为满足接口断言的测试桩），改成让它真正被使用，
# 不加白名单。
set -euo pipefail
cd "$(dirname "$0")/.."

deadcode=golang.org/x/tools/cmd/deadcode@v0.52.0
staticcheck=honnef.co/go/tools/cmd/staticcheck@v0.8.2
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# 工具按本机平台装一次；分析时再用 GOOS 切换目标平台（go run 会连工具一起按目标平台编译）。
for tool in "$deadcode" "$staticcheck"; do GOBIN="$tmp/bin" GOOS= GOARCH= go install "$tool"; done
deadcode="$tmp/bin/deadcode"
staticcheck="$tmp/bin/staticcheck"

# 去掉行列号，便于跨平台取交集。
normalize() { sed -E 's/:[0-9]+:[0-9]+:/:/' | sort -u; }

for goos in darwin linux windows; do
	GOOS=$goos "$deadcode" -test ./... | normalize >"$tmp/deadcode-$goos"
	# staticcheck 有发现时退出码非零，这里只收集输出。
	GOOS=$goos "$staticcheck" -checks U1000 -tests ./... | normalize >"$tmp/unused-$goos" || true
done
"$deadcode" -tags browser_integration -test ./... | normalize >"$tmp/deadcode-tag"

comm -12 "$tmp/deadcode-darwin" "$tmp/deadcode-linux" | comm -12 - "$tmp/deadcode-windows" | comm -12 - "$tmp/deadcode-tag" >"$tmp/dead"
comm -12 "$tmp/unused-darwin" "$tmp/unused-linux" | comm -12 - "$tmp/unused-windows" >>"$tmp/dead"

if [ -s "$tmp/dead" ]; then
	echo "发现没有任何调用方的代码，请删除（或让它真正被使用）：" >&2
	cat "$tmp/dead" >&2
	exit 1
fi
echo "没有发现死代码。"
