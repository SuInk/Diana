#!/usr/bin/env bash
# Copyright (c) 2025-now SuInk.
# Licensed under the Limited Redistribution License in the repository root.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

failed=false

# report_matches <label> <pattern> <allow-pattern> <pathspec...>
# allow-pattern 为空表示不做例外过滤；用于放行确实需要写死的公共路径。
report_matches() {
	local label="$1"
	local pattern="$2"
	local allow="$3"
	shift 3
	local matches
	matches="$(git grep -nI -E "$pattern" -- "$@" 2>/dev/null || true)"
	if [[ -n "$matches" && -n "$allow" ]]; then
		matches="$(printf '%s\n' "$matches" | grep -vE "$allow" || true)"
	fi
	if [[ -n "$matches" ]]; then
		printf 'public-repo audit: %s\n%s\n' "$label" "$matches" >&2
		failed=true
	fi
}

# Linuxbrew 固定装在 /home/linuxbrew，是发行版级别的公共路径而不是某个人的家目录。
report_matches \
	"absolute user home path found" \
	'(/Users/[A-Za-z0-9._-]+|/home/[A-Za-z0-9._-]+|[A-Za-z]:\\Users\\[A-Za-z0-9._-]+)' \
	'/home/linuxbrew/\.linuxbrew' \
	. ':!scripts/check-public-repo.sh'

report_matches \
	"credential-like value found" \
	'(sk-[A-Za-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|AIza[0-9A-Za-z_-]{30,}|gh[pousr]_[A-Za-z0-9]{20,}|-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----|https?://[^/@[:space:]]+:[^/@[:space:]]+@)' \
	'' \
	. ':!scripts/check-public-repo.sh'

report_matches \
	"long QQ identity found; use synthetic five-digit fixture ranges" \
	'((SelfID|UserID|GroupID|OperatorID|OwnerID|BotQQ):[[:space:]]*"[0-9]{8,12}"|"(self_id|user_id|group_id|operator_id|owner_id|uin)"[[:space:]]*:[[:space:]]*"?[0-9]{8,12}|(CQ:at,qq=|NAPCAT_QUICK_ACCOUNT[^0-9]*|QQBOT_QQ[^0-9]*)[0-9]{8,12})' \
	'' \
	'*.go' '*.js' '*.ts' '*.vue' '*.md' '*.yml' '*.yaml'

tracked_runtime_files="$(git ls-files | grep -E '(^|/)(runtime\.env|\.env|.*\.(db|sqlite|sqlite3)(-(shm|wal))?|.*\.log|cookies[^/]*\.txt)$' | grep -vE '(^|/)\.env\.example$' || true)"
if [[ -n "$tracked_runtime_files" ]]; then
	printf 'public-repo audit: tracked runtime or credential files found\n%s\n' "$tracked_runtime_files" >&2
	failed=true
fi

if [[ "$failed" == "true" ]]; then
	exit 1
fi

echo "public-repo audit passed"
