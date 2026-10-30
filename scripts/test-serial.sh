#!/usr/bin/env bash
# 本机跑 Go 测试的统一入口：全机排队、限并发、降优先级。
#
# 同一台机器上常有好几个会话、好几个 worktree 同时跑 `go test -race ./...`，
# 每个都按核数并发编译和测试，-race 还让 CPU 和内存翻几倍，整机卡死。
# 这个脚本让所有 worktree 排同一条队，同一时间只有一组测试在跑，按先来后到轮转；
# 同一 worktree 再提交请求时，它还在排队的旧请求会被取代（以退出码 3 结束）。
# 跑的时候用 nice 降优先级，并把并发限在 DIANA_TEST_PROCS（默认 4）；单包超时
# 默认 40 分钟（DIANA_TEST_TIMEOUT）。
#
# 用法：
#   scripts/test-serial.sh                 改动涉及的包（相对 origin/main，含工作区改动）
#   scripts/test-serial.sh ./model/agent   指定包，参数原样交给 go test
#   scripts/test-serial.sh ./...           全仓库（发版前用）
#
# 只跑一遍 -race（本机时区）。CI 不开 -race，跑在 UTC，两边合起来覆盖竞态和两种时区。
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

procs="${DIANA_TEST_PROCS:-4}"
# 限了并发以后大包跑得更慢（model/assistant 的 -race 超过 go test 默认的 10 分钟），超时放宽。
timeout="${DIANA_TEST_TIMEOUT:-40m}"
queue_dir="${DIANA_TEST_QUEUE:-$HOME/.cache/diana-go-test.queue}"
# 旧版脚本用的锁。别的 worktree 还没 rebase 时仍在用它，轮到自己后也要拿到它才开跑。
legacy_lock="${DIANA_TEST_LOCK:-$HOME/.cache/diana-go-test.lock}"
mkdir -p "$queue_dir" "$(dirname "$legacy_lock")"

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

# 排队号是「时间戳-PID」，文件名按字典序就是先来后到。排队时是 .wait，开跑后改名 .run。
# 号文件第一行记 worktree，第二行记参数。
worktree="$(pwd)"
ticket="$queue_dir/$(printf '%012d-%07d' "$(date +%s)" $$)"
held_legacy=0

cleanup() {
	rm -f "$ticket.wait" "$ticket.run"
	if [ "$held_legacy" = 1 ]; then rm -rf "$legacy_lock"; fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM

describe() { echo "$(basename "$(sed -n 1p "$1" 2>/dev/null)") $(sed -n 2p "$1" 2>/dev/null)"; }

# 进程已经不在的号直接收走。
reap() {
	local f pid
	for f in "$queue_dir"/*.wait "$queue_dir"/*.run; do
		[ -e "$f" ] || continue
		pid="${f##*-}"
		pid="${pid%.*}"
		kill -0 "$((10#$pid))" 2>/dev/null || rm -f "$f"
	done
}

# 同一 worktree 还在排队的旧请求作废，已经开跑的不动。
for f in "$queue_dir"/*.wait; do
	[ -e "$f" ] || continue
	if [ "$(sed -n 1p "$f" 2>/dev/null)" = "$worktree" ]; then
		what="$(describe "$f")"
		rm -f "$f" && echo "取代同一 worktree 还在排队的旧请求：$what"
	fi
done

printf '%s\n%s\n' "$worktree" "${packages[*]}" >"$queue_dir/.tmp-$$"
mv "$queue_dir/.tmp-$$" "$ticket.wait"

waited=0
while :; do
	if [ ! -e "$ticket.wait" ]; then
		echo "这次请求已被同一 worktree 的新请求取代，没有运行测试。" >&2
		exit 3
	fi
	reap
	entries=("$queue_dir"/*)
	# mv 是原子的：被取代的同时轮到自己，只会有一边成功。
	if [ "${entries[0]}" = "$ticket.wait" ] && mv "$ticket.wait" "$ticket.run" 2>/dev/null; then
		break
	fi
	if [ $((waited % 60)) -eq 0 ]; then
		ahead=0
		for f in "${entries[@]}"; do
			[[ "$f" < "$ticket" ]] && ahead=$((ahead + 1))
		done
		echo "排队中，前面还有 $ahead 组；正在跑：$(describe "${entries[0]}")"
	fi
	sleep 5
	waited=$((waited + 5))
done

waited=0
while ! mkdir "$legacy_lock" 2>/dev/null; do
	holder="$(cat "$legacy_lock/pid" 2>/dev/null || true)"
	if [ -n "$holder" ] && ! kill -0 "$holder" 2>/dev/null; then
		rm -rf "$legacy_lock"
		continue
	fi
	if [ $((waited % 60)) -eq 0 ]; then
		echo "等待旧版脚本的测试结束：$(cat "$legacy_lock/what" 2>/dev/null || echo "PID ${holder:-未知}")"
	fi
	sleep 5
	waited=$((waited + 5))
done
held_legacy=1
echo $$ >"$legacy_lock/pid"
echo "$worktree ${packages[*]}" >"$legacy_lock/what"

export GOMAXPROCS="$procs"
echo "== go test -race -p $procs ${packages[*]}"
nice -n 15 go test -race -p "$procs" -timeout "$timeout" "${packages[@]}"
