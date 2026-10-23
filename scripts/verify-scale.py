"""Opt-in full TUI idle observation; distinct from the exact-search microbenchmark."""
import json
import os
from pathlib import Path
import runpy
import subprocess
import sys
import tempfile
import time

BIN = Path(sys.argv[1] if len(sys.argv) > 1 else "build/recall").resolve()
Session = runpy.run_path(str(Path(__file__).with_name("verify-neovim.py")), run_name="recall_pty")["Session"]


def sample(pid):
    text = subprocess.check_output(["ps", "-p", str(pid), "-o", "time=,rss="], text=True).split()
    parts = text[0].split(":")
    seconds = float(parts[-1]) + 60 * int(parts[-2])
    if len(parts) > 2:
        seconds += 3600 * int(parts[-3])
    return seconds, int(text[1])


for count in [0, 1000, 10000]:
    with tempfile.TemporaryDirectory(prefix="recall-scale-") as tmp:
        home = Path(tmp)
        root = home / "collection"
        for name in ["content", "models", "runtime", "run"]:
            (root / name).mkdir(parents=True)
        (root / "config.json").write_text('{"timezone":"UTC"}')
        env = {k:v for k,v in os.environ.items() if not k.startswith("RECALL_")}
        env.update(HOME=str(home), RECALL_HOME=str(root), TERM="xterm-256color")
        template = json.loads(subprocess.check_output([str(BIN), "--json", "document", "create", "--type", "note", "--name", "template"], input=b"synthetic body\n", env=env))["data"]
        (root / "content" / template["path"]).unlink()
        for n in range(count):
            raw = template["raw"].replace(template["id"], f"{n+1:032x}").replace("title: template", f"title: synthetic-{n:05}")
            (root / "content" / f"synthetic-{n:05}.note.md").write_text(raw)
        start = time.monotonic()
        p = subprocess.run([str(BIN), "--json", "sync"], env=env, capture_output=True, timeout=180)
        assert p.returncode == 0 and not p.stderr, (p.stdout, p.stderr)
        print(f"{count} source files: initial CLI metadata reconciliation/indexing {time.monotonic()-start:.3f}s (not embedding)", flush=True)
        s = Session([], env)
        try:
            s.until(lambda: b"Recall" in s.output, seconds=120)
            s.drain(2)
            cpu0, rss0 = sample(s.process.pid)
            start = time.monotonic()
            rss = [rss0]
            for _ in range(35):
                s.drain(1)
                cpu1, r = sample(s.process.pid)
                rss.append(r)
            elapsed = time.monotonic() - start
            print(f"{count} source files: actual TUI idle {elapsed:.3f}s, CPU delta {cpu1-cpu0:.3f}s ({100*(cpu1-cpu0)/elapsed:.2f}% one core), sampled RSS {min(rss)}..{max(rss)} KiB; includes 100ms Darwin metadata polls and a 30s periodic reconciliation", flush=True)
            s.send(b"q")
            s.close()
        finally:
            s.cleanup()
print("LIMIT: metadata-only fixtures, unavailable inference; whole TUI process observation, not a system-wide profiler or semantic indexing throughput")
