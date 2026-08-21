#!/usr/bin/env bash
# Copyright (c) 2025-now SuInk.
# Licensed under the Limited Redistribution License in the repository root.

set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
	echo "start-napcat-mac.sh must run on macOS" >&2
	exit 1
fi

# NapCat 需要宿主聊天客户端的真实安装路径，路径本身不能改名。
CLIENT_APP="${DIANA_CLIENT_APP:-${DIANA_QQ_APP:-/Applications/QQ.app}}"
CLIENT_BINARY="$CLIENT_APP/Contents/MacOS/QQ"
EXPECTED_VERSION="${DIANA_CLIENT_EXPECTED_VERSION:-${DIANA_QQ_EXPECTED_VERSION:-}}"
ENFORCE_APP="${DIANA_CLIENT_ENFORCE_APP:-${DIANA_QQ_ENFORCE_APP:-true}}"

client_processes() {
	/usr/bin/pgrep -x QQ 2>/dev/null || true
}

client_running_with_napcat() {
	local pid command found=false

	while IFS= read -r pid; do
		[[ -n "$pid" ]] || continue
		command="$(/bin/ps -ww -o command= -p "$pid" 2>/dev/null || true)"
		if [[ "$command" == "$CLIENT_BINARY"* && "$command" == *" --no-sandbox"* ]]; then
			found=true
			continue
		fi
		return 1
	done < <(client_processes)
	[[ "$found" == "true" ]]
}

if [[ ! -x "$CLIENT_BINARY" ]]; then
	echo "chat client app not found: $CLIENT_APP" >&2
	exit 1
fi

if [[ -n "$EXPECTED_VERSION" ]]; then
	PACKAGE_JSON="$CLIENT_APP/Contents/Resources/app/package.json"
	ACTUAL_VERSION="$(/usr/bin/plutil -extract version raw -o - "$PACKAGE_JSON" 2>/dev/null || true)"
	if [[ "$ACTUAL_VERSION" != "$EXPECTED_VERSION" ]]; then
		echo "QQ version mismatch: expected $EXPECTED_VERSION, got ${ACTUAL_VERSION:-unknown} ($QQ_APP)" >&2
		exit 3
	fi
fi

if client_running_with_napcat; then
	echo "NapCat QQ is already running from $QQ_APP"
	exit 0
fi

if [[ -n "$(client_processes)" ]]; then
	if [[ "$ENFORCE_APP" != "true" ]]; then
		echo "A different QQ process is running; quit it and restart Diana to load $QQ_APP" >&2
		exit 2
	fi
	echo "Stopping mismatched QQ process before starting $QQ_APP" >&2
	while IFS= read -r pid; do
		[[ -n "$pid" ]] && /bin/kill -TERM "$pid" 2>/dev/null || true
	done < <(client_processes)
	for _ in {1..20}; do
		[[ -z "$(client_processes)" ]] && break
		/bin/sleep 0.5
	done
	if [[ -n "$(client_processes)" ]]; then
		echo "Mismatched QQ process did not exit" >&2
		exit 2
	fi
fi

launch_args=(--no-sandbox)
if [[ -n "${NAPCAT_QQ:-}" ]]; then
	launch_args+=(-q "$NAPCAT_QQ")
fi
/usr/bin/open -n "$CLIENT_APP" --args "${launch_args[@]}"

for _ in {1..30}; do
	if client_running_with_napcat; then
		echo "NapCat QQ started"
		exit 0
	fi
	/bin/sleep 0.5
done

echo "QQ did not start with --no-sandbox" >&2
exit 1
