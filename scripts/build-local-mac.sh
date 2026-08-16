#!/usr/bin/env bash
# Copyright (c) 2025-now SuInk.
# Licensed under the Limited Redistribution License in the repository root.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUTPUT="${1:-$ROOT/dist/diana-webui}"
IDENTIFIER="${DIANA_MACOS_CODE_IDENTIFIER:-com.suink.diana}"
GO_BIN="${GO:-go}"
BUILD_VERSION="${DIANA_BUILD_VERSION:-}"
if [[ -z "$BUILD_VERSION" ]]; then
	BUILD_VERSION="$(git -C "$ROOT" describe --tags --exact-match HEAD 2>/dev/null || true)"
fi
if [[ -z "$BUILD_VERSION" ]]; then
	BUILD_VERSION="dev"
fi
if [[ "$BUILD_VERSION" != "dev" && ! "$BUILD_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$ ]]; then
	echo "DIANA_BUILD_VERSION must be dev or a semantic version such as v0.8.7: $BUILD_VERSION" >&2
	exit 1
fi
BUILD_LDFLAGS="-X main.buildVersion=$BUILD_VERSION"

if [[ "$(uname -s)" != "Darwin" ]]; then
	echo "build-local-mac.sh must run on macOS" >&2
	exit 1
fi

cd "$ROOT"
if [[ "$OUTPUT" == *.app ]]; then
	APP_PARENT="$(dirname "$OUTPUT")"
	APP_NAME="$(basename "$OUTPUT")"
	mkdir -p "$APP_PARENT"
	TEMP_APP="$APP_PARENT/.$APP_NAME.new.$$"
	APP_BINARY="$TEMP_APP/Contents/MacOS/diana-webui"
	PDF_HELPER="$TEMP_APP/Contents/MacOS/diana-pdf-vision"
	BUNDLED_FRONTEND_SOURCE="${DIANA_BUNDLED_FRONTEND_SOURCE:-$ROOT/frontend-next/dist}"
	BUNDLED_FRONTEND_TARGET="$TEMP_APP/Contents/Resources/frontend-next/dist"
	if [[ ! -f "$BUNDLED_FRONTEND_SOURCE/index.html" ]]; then
		echo "Built frontend-next is missing: $BUNDLED_FRONTEND_SOURCE" >&2
		exit 1
	fi
	mkdir -p "$TEMP_APP/Contents/MacOS" "$BUNDLED_FRONTEND_TARGET"
	trap 'rm -rf "$TEMP_APP"' EXIT

	cp "$ROOT/packaging/macos/Info.plist" "$TEMP_APP/Contents/Info.plist"
	cp -R "$BUNDLED_FRONTEND_SOURCE/." "$BUNDLED_FRONTEND_TARGET/"
	"$GO_BIN" build -trimpath -ldflags "$BUILD_LDFLAGS" -o "$APP_BINARY" ./cmd/webui
	MACOS_ARCH="$(uname -m)"
	xcrun swiftc -O -target "${MACOS_ARCH}-apple-macos12.0" \
		-framework AppKit -framework PDFKit -framework Vision \
		"$ROOT/native/macos/diana_pdf_vision.swift" -o "$PDF_HELPER"
	codesign --force --sign - --identifier "$IDENTIFIER.pdf-vision" "$PDF_HELPER"
	codesign --force --deep --sign - --identifier "$IDENTIFIER" \
		--requirements "=designated => identifier \"$IDENTIFIER\"" "$TEMP_APP"
	codesign --verify --deep --strict "$TEMP_APP"

	rm -rf "$OUTPUT"
	mv "$TEMP_APP" "$OUTPUT"
	trap - EXIT
	echo "Built and signed $OUTPUT ($IDENTIFIER)"
	exit 0
fi

mkdir -p "$(dirname "$OUTPUT")"
TEMP_OUTPUT="$(dirname "$OUTPUT")/.$(basename "$OUTPUT").new.$$"
trap 'rm -f "$TEMP_OUTPUT"' EXIT
"$GO_BIN" build -trimpath -ldflags "$BUILD_LDFLAGS" -o "$TEMP_OUTPUT" ./cmd/webui
codesign --force --sign - --identifier "$IDENTIFIER" \
	--requirements "=designated => identifier \"$IDENTIFIER\"" "$TEMP_OUTPUT"
codesign --verify --strict "$TEMP_OUTPUT"
mv -f "$TEMP_OUTPUT" "$OUTPUT"
trap - EXIT
echo "Built and signed $OUTPUT ($IDENTIFIER)"
