#!/bin/sh
set -eu
REPO=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
cd "$REPO"
python3 scripts/verify-desktop.py build/Recall.app
RECALL_DESKTOP_OUTPUT="$REPO/build/Recall.verification.app" sh scripts/build-desktop.sh -tags desktopverify
python3 scripts/desktop-smoke.py build/Recall.verification.app/Contents/MacOS/Recall
python3 scripts/desktop-smoke.py build/Recall.verification.app/Contents/MacOS/Recall conflict
if [ -n "${RECALL_VERIFY_MODEL:-}" ] && [ -n "${RECALL_VERIFY_RUNTIME:-}" ]; then
    python3 scripts/desktop-smoke.py build/Recall.verification.app/Contents/MacOS/Recall real
else
    echo "Real-model desktop gate not run: set RECALL_VERIFY_MODEL and RECALL_VERIFY_RUNTIME to pinned fixture files."
fi
