#!/usr/bin/env bash
# 本机跑 Go 测试的统一入口：全机排队、限并发、降优先级。
#
# 同一台机器上常有好几个会话、好几个 worktree 同时跑 `go test -race ./...`，
# 每个都按核数并发编译和测试，-race 还让 CPU 和内存翻几倍，整机卡死。
# 这个脚本让所有 worktree 共用一把锁，同一时间只有一组测试在跑；
# 跑的时候用 nice 降优先级，并把并发限在 DIANA_TEST_PROCS（默认 4）。
#
# 用法：
#   scripts/test-serial.sh                 改动涉及的包（相对 origin/main，含工作区改动）
#   scripts/test-serial.sh ./model/agent   指定包，参数原样交给 go test
#   scripts/test-serial.sh ./...           全仓库（发版前用）
#
# 每组包跑两遍：一遍 -race，一遍 TZ=UTC，和 CI 一致。
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

procs="${DIANA_TEST_PROCS:-4}"
lock_dir="${DIANA_TEST_LOCK:-$HOME/.cache/diana-go-test.lock}"
mkdir -p "$(dirname "$lock_dir")"

packages=("$@")
if [ ${#packages[@]} -eq 0 ]; then
	base="$(git merge-base HEAD origin/main 2>/dev/null || echo HEAD)"
	while IFS= read -r dir; do
		[ -n "$dir" ] && [ -d "$dir" ] && packages+=("./$dir")
	done < <({ git diff --name-only "$base"; git ls-files --others --exclude-standard; } |
		grep '\.go$' | xargs -n1 dirname 2>/dev/null | sort -u)
	if [ ${#packages[@]} -eq 0 ]; then
		echo "没有改动的 Go 包；要跑全仓库请传 ./..."
		exit 0
	fi
fi

# mkdir 是原子的：建成功就拿到锁。锁里记着持有者的 PID，持有者已经不在了就把锁收回来。
acquire() {
	local waited=0 holder
	while ! mkdir "$lock_dir" 2>/dev/null; do
		holder="$(cat "$lock_dir/pid" 2>/dev/null || true)"
		if [ -n "$holder" ] && ! kill -0 "$holder" 2>/dev/null; then
			rm -rf "$lock_dir"
			continue
		fi
		if [ $((waited % 60)) -eq 0 ]; then
			echo "等待其他测试结束：$(cat "$lock_dir/what" 2>/dev/null || echo "PID ${holder:-未知}")"
		fi
		sleep 5
		waited=$((waited + 5))
	done
	echo $$ >"$lock_dir/pid"
	echo "$(pwd) ${packages[*]}" >"$lock_dir/what"
}
release() { rm -rf "$lock_dir"; }

acquire
trap release EXIT INT TERM

export GOMAXPROCS="$procs"
echo "== go test -race -p $procs ${packages[*]}"
nice -n 15 go test -race -p "$procs" "${packages[@]}"
echo "== TZ=UTC go test -p $procs ${packages[*]}"
TZ=UTC nice -n 15 go test -p "$procs" "${packages[@]}"
