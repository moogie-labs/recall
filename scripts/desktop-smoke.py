"""Native Wails verification against a disposable collection, never real HOME.

Requires a desktopverify build. Real mode accepts only pinned fixture assets
and waits on the owned worker lifetime lease before removing its collection.
"""
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tarfile
import tempfile

binary = Path(sys.argv[1]).resolve()
mode = sys.argv[2] if len(sys.argv) > 2 else "1"
base = os.environ.get("RECALL_TEST_TMPDIR", os.environ.get("TMPDIR", tempfile.gettempdir()))
if mode == "real":
    # Darwin's 103-byte Unix socket path cannot fit the harness's long temp base.
    base = "/private/tmp"
with tempfile.TemporaryDirectory(prefix="recall-native-", dir=base) as temporary:
    home = Path(temporary)
    root = home / "collection"
    for name in ["content", "run", "models", "runtime"]:
        (root / name).mkdir(parents=True, mode=0o700)
    configuration = {"timezone": "UTC"}
    if mode == "real":
        repository = Path(__file__).resolve().parent.parent
        model = json.loads((repository / "assets/model.lock.json").read_text())
        runtime = json.loads((repository / "assets/runtime.lock.json").read_text())
        weights = Path(os.environ["RECALL_VERIFY_MODEL"])
        archive = Path(os.environ["RECALL_VERIFY_RUNTIME"])
        for path, manifest in [(weights, model), (archive, runtime)]:
            with path.open("rb") as source:
                assert path.stat().st_size == manifest["size"] and hashlib.file_digest(source, "sha256").hexdigest() == manifest["sha256"], "Unverified native fixture assets"
        shutil.copyfile(weights, root / "models" / model["filename"])
        with tarfile.open(archive) as source:
            source.extractall(root / "runtime", filter="data")
        (root / "runtime/llama-b11669").rename(root / "runtime/b11669")
        configuration.update(model_id=model["id"], model_sha256=model["sha256"], model_signature="sha256:" + model["sha256"], runtime_id=runtime["id"])
    (root / "config.json").write_text(json.dumps(configuration))
    (root / ".recall-native-fixture").write_text("owned verification fixture")
    source = root / "content/smoke.note.md"
    source.write_text('---\nid: native-smoke\ntype: note\ntitle: Native smoke\ncustom: preserve-me\n---\n# Native smoke\n')
    environment = dict(os.environ, HOME=str(home), RECALL_HOME=str(root), RECALL_DESKTOP_VERIFY=mode)
    try:
        try:
            process = subprocess.run([str(binary), "--root", str(root)], env=environment, capture_output=True, text=True, timeout=45 if mode == "real" else 25)
        except subprocess.TimeoutExpired as error:
            print(error.stdout.decode() if isinstance(error.stdout, bytes) else error.stdout or "")
            print(error.stderr.decode() if isinstance(error.stderr, bytes) else error.stderr or "", file=sys.stderr)
            raise
        print(process.stdout)
        print(process.stderr, file=sys.stderr)
        marker = "NATIVE_CONFLICT_PASS" if mode == "conflict" else "NATIVE_SMOKE_PASS"
        if process.returncode != 0 or marker not in process.stdout:
            raise SystemExit("Native smoke failed or verification hooks absent")
        saved = source.read_text()
        assert "custom: preserve-me" in saved, "body editing changed metadata"
        assert "Autosaved 自由" in saved, "3-second autosave was not persisted"
        assert list((root / "content").iterdir()) == [source], "Native tree CRUD left or resurrected source files"
        if mode == "conflict":
            assert "External version" in saved and "Quit flush 😀" not in saved, "Conflicting source was overwritten"
            print("Native conflicting save blocks repeated Quit and retains draft until explicit reload PASS")
        else:
            assert "Quit flush 😀" in saved, "immediate quit discarded pending edits"
            print("Native autosave, Markdown, search, metadata preservation and immediate quit flush PASS (" + mode + ")")
    finally:
        if mode == "real":
            key = hashlib.sha256(str(root.resolve()).encode()).hexdigest()[:16]
            lease = home / ".cache/recall/sockets" / (key + ".worker.lock")
            if lease.exists():
                # Respect the existing 120-second idle lifecycle, never kill a guessed PID.
                signal.alarm(160)
                with lease.open("rb") as lock:
                    fcntl.flock(lock, fcntl.LOCK_EX)
                signal.alarm(0)
                print("Owned inference manager/guardian/native lifetime lease released")
