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

		# 应用配置已经搬进 config.yaml，这里只放外部集成用的变量：解析器的站点
		# cookie 和代理、搜索服务的 key、ffmpeg / NapCat 这些外部程序的路径。
		# 它们在 WebUI 里没有对应项，不存在两个真相源的问题。
		case "$key" in
			DIANA_*|EXA_API_KEY*|TAVILY_API_KEY*|BILI_SESSDATA|DOUYIN_CK|XHS_CK|RESOLVER_PROXY|NAPCAT_QQ)
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

# 应用配置走 config.yaml。本地开发第一次跑的时候生成一份指向仓库目录的默认
# 配置；已经存在就原样用，不覆盖开发者自己改过的内容。
CONFIG_FILE="${DIANA_CONFIG:-$ROOT/config.yaml}"
if [[ ! -f "$CONFIG_FILE" ]]; then
	cat >"$CONFIG_FILE" <<EOF
# 本地开发用配置，由 scripts/start-local-mac.sh 首次运行时生成。
# 完整字段见 config.example.yaml。bot / llm 两段只在数据库为空时播种一次，
# 之后请在 WebUI 里改。
server:
  host: '127.0.0.1'
  port: '18080'
  frontend_dist: '$ROOT/frontend-next/dist'
storage:
  db_path: '$ROOT/data/diana.db'
  log_path: '$ROOT/logs/diana.log'
update:
  root: '$ROOT'
  apply_enabled: true
EOF
	chmod 600 "$CONFIG_FILE"
	echo "generated $CONFIG_FILE"
fi
export DIANA_CONFIG="$CONFIG_FILE"

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
