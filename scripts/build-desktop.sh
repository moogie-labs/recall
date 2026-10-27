#!/bin/sh
set -eu
REPO=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
cd "$REPO/desktop"
go run github.com/wailsapp/wails/v2/cmd/wails@v2.17.0 build -platform darwin/arm64 -clean
mkdir -p "$REPO/build"
ditto build/bin/Recall.app "$REPO/build/Recall.app"
