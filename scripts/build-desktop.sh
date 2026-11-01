#!/bin/sh
set -eu
REPO=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
cd "$REPO/desktop"
export MACOSX_DEPLOYMENT_TARGET=13.0
export CGO_CFLAGS="${CGO_CFLAGS:-} -mmacosx-version-min=13.0"
export CGO_LDFLAGS="${CGO_LDFLAGS:-} -mmacosx-version-min=13.0"
go run github.com/wailsapp/wails/v2/cmd/wails@v2.17.0 build -platform darwin/arm64 -clean "$@"
mkdir -p "$REPO/build"
OUTPUT="${RECALL_DESKTOP_OUTPUT:-$REPO/build/Recall.app}"
ditto build/bin/Recall.app "$OUTPUT"
# First launch installs the runtime bundled in Contents/Resources/assets. Local
# builds bundle the pinned upstream runtime when it is cached (and verified);
# release builds bundle the signed runtime in scripts/release-macos.py instead.
python3 - "$REPO" "$OUTPUT" <<'PY'
import shutil, sys
from pathlib import Path
repo, app = (Path(arg) for arg in sys.argv[1:])
sys.path.insert(0, str(repo / "scripts"))
import distribution as dist
bundled = app / "Contents/Resources/assets"
shutil.rmtree(bundled, ignore_errors=True)
archive = dist.CACHE / dist.RUNTIME["filename"]
if archive.is_file():
    dist.pinned(archive, dist.RUNTIME)
    (bundled / "runtime/darwin-arm64").mkdir(parents=True)
    shutil.copyfile(archive, bundled / "runtime/darwin-arm64" / dist.RUNTIME["filename"])
    for name in ("runtime.lock.json", "model.lock.json"):
        shutil.copyfile(repo / "assets" / name, bundled / name)
    print("Bundled pinned runtime for first-launch setup")
else:
    print("No cached runtime: this app cannot install semantic search on first launch (run scripts/package.sh to cache it)")
PY
# Re-seal the ad-hoc signature over the bundled resources (release builds are
# re-signed with Developer ID by the signing stage).
codesign --force --sign - "$OUTPUT"
