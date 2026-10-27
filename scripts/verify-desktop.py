"""Inspect a production bundle and exercise its headless dispatch in isolation."""
import os
from pathlib import Path
import plistlib
import subprocess
import sys
import tempfile

bundle = Path(sys.argv[1]).resolve()
binary = bundle / "Contents/MacOS/Recall"
assert binary.is_file(), "Build Recall.app first"
with (bundle / "Contents/Info.plist").open("rb") as source:
    info = plistlib.load(source)
assert info["CFBundleIdentifier"] == "local.recall.desktop"
assert info["LSMinimumSystemVersion"] == "13.0"
assert "arm64" in subprocess.check_output(["file", str(binary)], text=True)
assert b"NATIVE_SMOKE_PASS" not in binary.read_bytes(), "Do not distribute the desktopverify build"
assert not list(bundle.rglob("*.gguf")), "Model weights must stay collection-backed"
with tempfile.TemporaryDirectory(prefix="recall-bundle-") as temporary:
    home = Path(temporary)
    # Relocation and spaces must not alter worker dispatch or require adjacent source.
    relocated = home / "path with spaces" / "Recall.app"
    subprocess.run(["ditto", str(bundle), str(relocated)], check=True)
    result = subprocess.run([str(relocated / "Contents/MacOS/Recall"), "_embedding-worker", "--invalid"], env=dict(os.environ, HOME=str(home)), capture_output=True, text=True, timeout=10)
    assert result.returncode == 2 and "private worker requires" in result.stderr, result
    assert not (home / ".local/share/recall").exists(), "Dispatch initialized an unexpected collection"
print("Production bundle identity, architecture, no fixture bindings/weights, relocation and headless worker dispatch PASS")
