#!/usr/bin/env bash
# Copyright (c) 2025-now SuInk.
# Licensed under the Limited Redistribution License in the repository root.

set -euo pipefail

# launchd does not inherit the interactive shell PATH. Add common package-manager
# locations when present so media dependencies remain discoverable after login.
extend_executable_path() {
	local dir
	for dir in /opt/homebrew/bin /usr/local/bin /opt/local/bin "$HOME/.local/bin"; do
		[[ -d "$dir" ]] || continue
		case ":${PATH:-}:" in
			*":$dir:"*) ;;
			*) PATH="$dir${PATH:+:$PATH}" ;;
		esac
	done
	export PATH
}

extend_path_from_executable() {
	local executable="${1:-}" dir
	[[ "$executable" == /* && -x "$executable" ]] || return 0
	dir="$(dirname "$executable")"
	case ":${PATH:-}:" in
		*":$dir:"*) ;;
		*) PATH="$dir${PATH:+:$PATH}" ;;
	esac
	export PATH
}

extend_executable_path

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEFAULT_RUNTIME_ENV="$ROOT/runtime.env"
if [[ ! -f "$DEFAULT_RUNTIME_ENV" && -f "$ROOT/.env" ]]; then
	DEFAULT_RUNTIME_ENV="$ROOT/.env"
fi
RUNTIME_ENV="${DIANA_RUNTIME_ENV:-${DIANA_BRIDGE_ENV:-$DEFAULT_RUNTIME_ENV}}"

load_runtime_env() {
	local line key value

	while IFS= read -r line || [[ -n "$line" ]]; do
		line="${line%$'\r'}"
		[[ "$line" =~ ^[[:space:]]*$ ]] && continue
		[[ "$line" =~ ^[[:space:]]*# ]] && continue
		[[ "$line" =~ ^[[:space:]]*(export[[:space:]]+)?([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]] || continue

		key="${BASH_REMATCH[2]}"
		value="${BASH_REMATCH[3]}"
		value="${value#"${value%%[![:space:]]*}"}"
		value="${value%"${value##*[![:space:]]}"}"
		if [[ "$value" == \"*\" && "$value" == *\" ]]; then
			value="${value:1:${#value}-2}"
		elif [[ "$value" == \'*\' && "$value" == *\' ]]; then
			value="${value:1:${#value}-2}"
		fi

		case "$key" in
			DIANA_*|LLM_*|QQBOT_*|BOT_QQ|ONEBOT_*|NONEBOT_*|AGENT_*|EXA_API_KEY*|TAVILY_API_KEY*|APP_DB_PATH|LOG_PATH|HOST|PORT|FRONTEND_DIST|BILI_SESSDATA|DOUYIN_CK|XHS_CK|RESOLVER_PROXY|NAPCAT_QQ)
				export "$key=$value"
				;;
		esac
	done < "$1"
}

if [[ -f "$RUNTIME_ENV" ]]; then
	load_runtime_env "$RUNTIME_ENV"
fi

# A configured absolute path is authoritative and also exposes sibling tools
# such as ffprobe to code paths that discover executables through PATH.
extend_path_from_executable "${DIANA_FFMPEG_PATH:-}"
extend_path_from_executable "${DIANA_FFPROBE_PATH:-}"
extend_path_from_executable "${DIANA_TTS_FFMPEG_PATH:-}"

mkdir -p "$ROOT/data" "$ROOT/logs"

if [[ "$(uname -s)" == "Darwin" && "${DIANA_START_NAPCAT:-true}" != "false" ]]; then
	NAPCAT_LAUNCHER="$ROOT/scripts/start-napcat-mac.sh"
	if [[ -x "$NAPCAT_LAUNCHER" ]]; then
		if ! "$NAPCAT_LAUNCHER" >>"$ROOT/logs/napcat-launch.log" 2>&1; then
			echo "NapCat auto-start failed; see $ROOT/logs/napcat-launch.log" >&2
		fi
	fi
fi

export HOST="${HOST:-127.0.0.1}"
export PORT="${PORT:-18080}"
export APP_DB_PATH="${APP_DB_PATH:-$ROOT/data/diana.db}"
export LOG_PATH="${LOG_PATH:-$ROOT/logs/diana.log}"
export FRONTEND_DIST="${FRONTEND_DIST:-$ROOT/frontend-next/dist}"
if [[ -z "${DIANA_UPDATE_ROOT:-}" && -d "$ROOT/.git" ]]; then
	export DIANA_UPDATE_ROOT="$ROOT"
fi
export DIANA_UPDATE_APPLY_ENABLED="${DIANA_UPDATE_APPLY_ENABLED:-true}"

export ONEBOT_REVERSE_WS_ENDPOINT="${ONEBOT_REVERSE_WS_ENDPOINT:-ws://127.0.0.1:${PORT}/onebot/v11/ws}"
export ONEBOT_ACCESS_TOKEN="${ONEBOT_ACCESS_TOKEN:-${QQBOT_ONEBOT_ACCESS_TOKEN:-}}"
export DIANA_OWNER_ID="${DIANA_OWNER_ID:-${QQBOT_OWNER_ID:-}}"
export DIANA_GROUP_TRIGGERS="${DIANA_GROUP_TRIGGERS:-Diana,diana}"
export DIANA_AGENT_WORK_DIR="${DIANA_AGENT_WORK_DIR:-${AGENT_WORK_DIR:-$ROOT}}"
export DIANA_AGENT_SKILL_ROOTS="${DIANA_AGENT_SKILL_ROOTS:-$ROOT/skills}"
export DIANA_AGENT_MCP_CONFIG="${DIANA_AGENT_MCP_CONFIG:-${AGENT_MCP_CONFIG:-$ROOT/.mcp.json}}"

if [[ -z "${QQBOT_QQ:-}" ]]; then
	export DIANA_BOT_ACCOUNT="${DIANA_BOT_ACCOUNT:-${QQBOT_SELF_ID:-${BOT_QQ:-${NAPCAT_QQ:-}}}}"
fi
if [[ -z "${LLM_BASE_URL:-}" && -n "${DIANA_BASE_URL:-}" ]]; then
	export LLM_BASE_URL="$DIANA_BASE_URL"
fi
if [[ -z "${LLM_MODEL:-}" && -n "${DIANA_MODEL:-}" ]]; then
	export LLM_MODEL="$DIANA_MODEL"
fi
if [[ -z "${LLM_API_KEY:-}" ]]; then
	if [[ -n "${DIANA_API_KEY:-}" ]]; then
		export LLM_API_KEY="$DIANA_API_KEY"
	elif [[ -n "${LLM_API_KEY_FILE:-}" && -r "$LLM_API_KEY_FILE" ]]; then
		export LLM_API_KEY="$(tr -d '\r\n' < "$LLM_API_KEY_FILE")"
	elif [[ -n "${DIANA_API_KEY_FILE:-}" && -r "$DIANA_API_KEY_FILE" ]]; then
		export LLM_API_KEY="$(tr -d '\r\n' < "$DIANA_API_KEY_FILE")"
	fi
fi

cd "$ROOT"
executables=()
if [[ -n "${DIANA_APP_EXECUTABLE:-}" ]]; then
	executables+=("$DIANA_APP_EXECUTABLE")
fi
executables+=(
	"$HOME/Applications/Diana.app/Contents/MacOS/diana-webui"
	"$ROOT/dist/diana-webui"
)
for executable in "${executables[@]}"; do
	if [[ -x "$executable" ]]; then
		exec "$executable"
	fi
done

echo "Diana executable not found; run 'make build-local-mac' first." >&2
exit 1
