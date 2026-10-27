"""Native Wails verification against a disposable collection, never real HOME.

Requires a bundle built with the desktopverify tag; release builds have no
verification hooks or verification-only bindings.
"""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

binary = Path(sys.argv[1]).resolve()
base = os.environ.get("RECALL_TEST_TMPDIR", os.environ.get("TMPDIR", tempfile.gettempdir()))
mode = sys.argv[2] if len(sys.argv) > 2 else "1"
with tempfile.TemporaryDirectory(prefix="recall-native-", dir=base) as temporary:
    home = Path(temporary)
    root = home / "collection"
    for name in ["content", "run", "models", "runtime"]:
        (root / name).mkdir(parents=True, mode=0o700)
    (root / "config.json").write_text(json.dumps({"timezone": "UTC"}))
    (root / ".recall-native-fixture").write_text("owned verification fixture")
    source = root / "content" / "smoke.note.md"
    source.write_text('---\nid: native-smoke\ntype: note\ntitle: Native smoke\ncustom: preserve-me\n---\n# Native smoke\n')
    environment = dict(os.environ, HOME=str(home), RECALL_HOME=str(root), RECALL_DESKTOP_VERIFY=mode)
    process = subprocess.run([str(binary), "--root", str(root)], env=environment, capture_output=True, text=True, timeout=25)
    print(process.stdout)
    print(process.stderr, file=sys.stderr)
    marker = "NATIVE_CONFLICT_PASS" if mode == "conflict" else "NATIVE_SMOKE_PASS"
    if process.returncode != 0 or marker not in process.stdout:
        raise SystemExit("Native smoke failed or verification hooks absent")
    saved = source.read_text()
    assert "custom: preserve-me" in saved, "body editing changed metadata"
    assert "Autosaved 自由" in saved, "3-second autosave was not persisted"
    if mode == "conflict":
        assert "External version" in saved, "conflicting source was overwritten"
        assert "Quit flush 😀" not in saved, "conflicting draft overwrote the source"
        print("Native conflicting save blocks repeated Quit and preserves source/draft until explicit reload PASS")
    else:
        assert "Quit flush 😀" in saved, "immediate quit discarded pending edits"
        print("Native autosave, preview, keyword search, source preservation, and immediate quit flush PASS")
