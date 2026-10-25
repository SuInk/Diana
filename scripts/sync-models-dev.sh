#!/bin/sh
# Refresh build-time capabilities; offline builds retain the checked-in snapshot.
set -eu
cd "$(dirname "$0")/.."
if GOOS= GOARCH= "${GO:-go}" generate -run models_dev_gen.go ./model/llm; then
  exit 0
fi
if [ ! -s model/llm/models_dev_snapshot.json.gz ]; then
  echo 'models.dev sync failed and no bundled snapshot exists' >&2
  exit 1
fi
echo 'WARNING: models.dev sync failed; using the bundled snapshot' >&2
