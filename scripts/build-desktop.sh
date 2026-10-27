#!/bin/sh
set -eu
REPO=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
cd "$REPO/desktop"
export MACOSX_DEPLOYMENT_TARGET=13.0
export CGO_CFLAGS="${CGO_CFLAGS:-} -mmacosx-version-min=13.0"
export CGO_LDFLAGS="${CGO_LDFLAGS:-} -mmacosx-version-min=13.0"
go run github.com/wailsapp/wails/v2/cmd/wails@v2.17.0 build -platform darwin/arm64 -clean "$@"
mkdir -p "$REPO/build"
ditto build/bin/Recall.app "${RECALL_DESKTOP_OUTPUT:-$REPO/build/Recall.app}"
