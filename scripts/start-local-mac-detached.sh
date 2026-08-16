#!/usr/bin/env bash
# Copyright (c) 2025-now SuInk.
# Licensed under the Limited Redistribution License in the repository root.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PID_FILE="$ROOT/data/diana.pid"
RUNTIME_LOG="$ROOT/logs/runtime.log"

mkdir -p "$ROOT/data" "$ROOT/logs"

if [[ -f "$PID_FILE" ]]; then
	old_pid="$(cat "$PID_FILE" 2>/dev/null || true)"
	if [[ -n "$old_pid" ]] && kill -0 "$old_pid" 2>/dev/null; then
		echo "Diana is already running: $old_pid"
		exit 0
	fi
fi

cd "$ROOT"
nohup "$ROOT/scripts/start-local-mac.sh" >"$RUNTIME_LOG" 2>&1 &
pid="$!"
echo "$pid" >"$PID_FILE"

sleep 1
if ! kill -0 "$pid" 2>/dev/null; then
	echo "Diana failed to start; recent log:"
	tail -80 "$RUNTIME_LOG" || true
	exit 1
fi

echo "Diana started: $pid"
