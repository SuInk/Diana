#!/bin/sh
# Copy the built frontend into cmd/webui/webdist so go:embed bakes it into diana-webui.
# Usage: embed-frontend.sh [source-root], defaulting to this checkout.
set -eu
cd "${1:-$(dirname "$0")/..}"
if [ ! -f frontend/dist/index.html ]; then
  echo 'frontend/dist/index.html is missing; run `make build-web` first' >&2
  exit 1
fi
mkdir -p cmd/webui/webdist
find cmd/webui/webdist -mindepth 1 -maxdepth 1 ! -name README.md -exec rm -rf {} +
cp -R frontend/dist/. cmd/webui/webdist/
