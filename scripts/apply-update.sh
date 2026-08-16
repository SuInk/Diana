#!/usr/bin/env bash
set -euo pipefail

ROOT="${DIANA_UPDATE_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
TARGET_COMMIT="${DIANA_UPDATE_TARGET_COMMIT:-$(git -C "$ROOT" rev-parse HEAD)}"
BUILD_VERSION="${DIANA_BUILD_VERSION:-}"
RUNNING_EXECUTABLE="${DIANA_RUNNING_EXECUTABLE:-}"
FRONTEND_TARGET="${FRONTEND_DIST:-$ROOT/frontend-next/dist}"
GO_BIN="${GO:-go}"
NPM_BIN="${NPM:-npm}"

if ! git -C "$ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
	echo "Update root is not a Git checkout: $ROOT" >&2
	exit 1
fi
if [[ ! -f "$ROOT/frontend-next/package-lock.json" ]]; then
	echo "frontend-next lockfile is missing from update root." >&2
	exit 1
fi
if [[ -z "$BUILD_VERSION" ]]; then
	BUILD_VERSION="$(git -C "$ROOT" describe --tags --exact-match "$TARGET_COMMIT" 2>/dev/null || true)"
fi
if [[ -z "$BUILD_VERSION" ]]; then
	BUILD_VERSION="dev"
fi
if [[ "$BUILD_VERSION" != "dev" && ! "$BUILD_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$ ]]; then
	echo "DIANA_BUILD_VERSION must be dev or a semantic version such as v0.8.7: $BUILD_VERSION" >&2
	exit 1
fi
case "$FRONTEND_TARGET" in
	""|"/"|"$HOME")
		echo "Unsafe frontend target: $FRONTEND_TARGET" >&2
		exit 1
		;;
esac

TARGET_APP=""
TARGET_EXECUTABLE="${DIANA_APP_EXECUTABLE:-$RUNNING_EXECUTABLE}"
if [[ "$TARGET_EXECUTABLE" == */Contents/MacOS/diana-webui ]]; then
	TARGET_APP="${TARGET_EXECUTABLE%/Contents/MacOS/diana-webui}"
fi
if [[ -z "$TARGET_EXECUTABLE" || "$TARGET_EXECUTABLE" == *"/go-build"* ]]; then
	TARGET_EXECUTABLE="$ROOT/dist/diana-webui"
	TARGET_APP=""
fi

FRONTEND_PARENT="$(dirname "$FRONTEND_TARGET")"
mkdir -p "$FRONTEND_PARENT"
STAGED_FRONTEND="$(mktemp -d "$FRONTEND_PARENT/.diana-frontend.new.XXXXXX")"
STAGED_APP=""
STAGED_EXECUTABLE=""

cleanup() {
	[[ -z "$STAGED_FRONTEND" || ! -e "$STAGED_FRONTEND" ]] || rm -rf "$STAGED_FRONTEND"
	[[ -z "$STAGED_APP" || ! -e "$STAGED_APP" ]] || rm -rf "$STAGED_APP"
	[[ -z "$STAGED_EXECUTABLE" || ! -e "$STAGED_EXECUTABLE" ]] || rm -f "$STAGED_EXECUTABLE"
}
trap cleanup EXIT

echo "Installing frontend-next dependencies..."
(
	cd "$ROOT/frontend-next"
	"$NPM_BIN" ci
	./node_modules/.bin/vue-tsc -b
	./node_modules/.bin/vite build --outDir "$STAGED_FRONTEND" --emptyOutDir
)

echo "Building Diana at $TARGET_COMMIT (version $BUILD_VERSION)..."
if [[ -n "$TARGET_APP" ]]; then
	APP_PARENT="$(dirname "$TARGET_APP")"
	APP_NAME="$(basename "$TARGET_APP")"
	mkdir -p "$APP_PARENT"
	STAGED_APP="$APP_PARENT/.${APP_NAME%.app}.update.$$.app"
	DIANA_BUILD_VERSION="$BUILD_VERSION" DIANA_BUNDLED_FRONTEND_SOURCE="$STAGED_FRONTEND" GO="$GO_BIN" \
		"$ROOT/scripts/build-local-mac.sh" "$STAGED_APP"
else
	EXECUTABLE_PARENT="$(dirname "$TARGET_EXECUTABLE")"
	mkdir -p "$EXECUTABLE_PARENT"
	STAGED_EXECUTABLE="$EXECUTABLE_PARENT/.$(basename "$TARGET_EXECUTABLE").update.$$"
	if [[ "$(uname -s)" == "Darwin" ]]; then
		DIANA_BUILD_VERSION="$BUILD_VERSION" GO="$GO_BIN" \
			"$ROOT/scripts/build-local-mac.sh" "$STAGED_EXECUTABLE"
	else
		(
			cd "$ROOT"
			"$GO_BIN" build -trimpath -ldflags "-X main.buildVersion=$BUILD_VERSION" -o "$STAGED_EXECUTABLE" ./cmd/webui
		)
	fi
fi

APP_BACKUP=""
EXECUTABLE_BACKUP=""
FRONTEND_BACKUP="$FRONTEND_TARGET.backup"
app_swapped=false
executable_swapped=false
frontend_swapped=false

rollback() {
	set +e
	if [[ "$frontend_swapped" == "true" ]]; then
		rm -rf "$FRONTEND_TARGET"
		[[ ! -e "$FRONTEND_BACKUP" ]] || mv "$FRONTEND_BACKUP" "$FRONTEND_TARGET"
	fi
	if [[ "$app_swapped" == "true" ]]; then
		rm -rf "$TARGET_APP"
		[[ -z "$APP_BACKUP" || ! -e "$APP_BACKUP" ]] || mv "$APP_BACKUP" "$TARGET_APP"
	fi
	if [[ "$executable_swapped" == "true" ]]; then
		rm -f "$TARGET_EXECUTABLE"
		[[ -z "$EXECUTABLE_BACKUP" || ! -e "$EXECUTABLE_BACKUP" ]] || mv "$EXECUTABLE_BACKUP" "$TARGET_EXECUTABLE"
	fi
}
trap rollback ERR

if [[ -n "$TARGET_APP" ]]; then
	APP_BACKUP="$TARGET_APP.backup"
	rm -rf "$APP_BACKUP"
	if [[ -e "$TARGET_APP" ]]; then
		mv "$TARGET_APP" "$APP_BACKUP"
	fi
	mv "$STAGED_APP" "$TARGET_APP"
	STAGED_APP=""
	app_swapped=true
else
	EXECUTABLE_BACKUP="$TARGET_EXECUTABLE.backup"
	rm -f "$EXECUTABLE_BACKUP"
	if [[ -e "$TARGET_EXECUTABLE" ]]; then
		mv "$TARGET_EXECUTABLE" "$EXECUTABLE_BACKUP"
	fi
	mv "$STAGED_EXECUTABLE" "$TARGET_EXECUTABLE"
	STAGED_EXECUTABLE=""
	executable_swapped=true
fi

rm -rf "$FRONTEND_BACKUP"
if [[ -e "$FRONTEND_TARGET" ]]; then
	mv "$FRONTEND_TARGET" "$FRONTEND_BACKUP"
fi
frontend_swapped=true
mv "$STAGED_FRONTEND" "$FRONTEND_TARGET"
STAGED_FRONTEND=""

trap - ERR
trap - EXIT
echo "Update applied at commit $TARGET_COMMIT. Restart Diana to run the new version."
