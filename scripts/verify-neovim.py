"""Opt-in real Neovim acceptance in owned PTYs; never opens the user's desktop."""
import fcntl
import json
import os
from pathlib import Path
import pty
import select
import shutil
import struct
import subprocess
import sys
import tempfile
import termios
import time

BIN = Path(sys.argv[1] if len(sys.argv) > 1 else "build/recall").resolve()
BASE = Path(tempfile.gettempdir())


class Session:
    def __init__(self, args, env):
        self.master, self.slave = pty.openpty()
        fcntl.ioctl(self.slave, termios.TIOCSWINSZ, struct.pack("HHHH", 30, 120, 0, 0))
        self.initial = termios.tcgetattr(self.slave)
        self.output = b""
        self.process = subprocess.Popen([str(BIN), *args], stdin=self.slave,
                                        stdout=self.slave, stderr=self.slave,
                                        env=env, start_new_session=True)

    def drain(self, seconds=.15):
        end = time.monotonic() + seconds
        while time.monotonic() < end:
            if select.select([self.master], [], [], .02)[0]:
                try:
                    data = os.read(self.master, 65536)
                except OSError:
                    break
                self.output += data
                if b"\x1b[6n" in data:
                    os.write(self.master, b"\x1b[1;1R")
                if b"\x1b[c" in data:
                    os.write(self.master, b"\x1b[?1;2c")
                if b"]11;?" in data:
                    os.write(self.master, b"\x1b]11;rgb:0000/0000/0000\x1b\\")
                if b"]10;?" in data:
                    os.write(self.master, b"\x1b]10;rgb:ffff/ffff/ffff\x1b\\")

    def until(self, predicate, seconds=10):
        end = time.monotonic() + seconds
        while not predicate() and time.monotonic() < end:
            self.drain(.05)
        assert predicate(), self.output[-5000:]

    def send(self, data):
        os.write(self.master, data)
        self.drain()

    def ex(self, command):
        self.send(b"\x1b:" + command.encode() + b"\r")

    def close(self, code=0):
        self.until(lambda: self.process.poll() is not None)
        self.process.wait()
        self.drain()
        assert self.process.returncode == code, self.output[-2500:]
        actual = termios.tcgetattr(self.slave)
        # Darwin sets PENDIN when terminal query replies arrive as raw mode is
        # restored to canonical. It is transient pending-input bookkeeping, not
        # an editor/CLI setting. Compare every other termios field/bit.
        expected = list(self.initial)
        actual[3] &= ~termios.PENDIN
        expected[3] &= ~termios.PENDIN
        assert actual == expected, ("terminal settings not restored", expected, actual)

    def cleanup(self):
        if self.process.poll() is None:
            # This is the actual Popen-owned CLI, not a discovered/guessed PID.
            self.process.terminate()
            self.process.wait(timeout=10)
        os.close(self.master)
        os.close(self.slave)


def main():
    NVIM = shutil.which("nvim")
    assert NVIM, "installed nvim required; this script does not install it"
    print(subprocess.check_output([NVIM, "--version"], text=True).splitlines()[0])
    with tempfile.TemporaryDirectory(prefix="recall-neovim-", dir=BASE) as tmp:
        home = Path(tmp).resolve()  # Recall canonicalizes Darwin's /var -> /private/var
        root = home / "collection"
        root.mkdir()
        for name in ["content", "models", "runtime", "run"]:
            (root / name).mkdir()
        (root / "config.json").write_text(json.dumps({"timezone": "UTC"}))
        # The editor is a per-user setting (beneath the isolated HOME), never a collection setting.
        settings = home / "Library/Application Support/recall/config.json"
        settings.parent.mkdir(parents=True)
        settings.write_text(json.dumps({"editor": [NVIM, "--clean", "-u", "NONE", "-i", "NONE", "-n"]}))
        env = {k:v for k,v in os.environ.items() if not k.startswith(("RECALL_", "NVIM_", "VIM", "XDG_"))}
        env.update(HOME=str(home), RECALL_HOME=str(root), TERM="xterm-256color", XDG_CONFIG_HOME=str(home / "config"), XDG_DATA_HOME=str(home / "data"), XDG_STATE_HOME=str(home / "state"))

        def cli(*args, body=""):
            p = subprocess.run([str(BIN), "--json", *args], input=body, env=env, text=True, capture_output=True, timeout=20)
            assert p.returncode == 0, (p.stdout, p.stderr)
            assert not p.stderr, p.stderr
            return json.loads(p.stdout)["data"]

        note = cli("document", "create", "--type", "note", "--name", "A note space", body="original note\n")
        task = cli("document", "create", "--type", "task", "--name", "B task", body="original task\n")
        sessions = []

        def session(args):
            s = Session(args, env)
            sessions.append(s)
            return s

        def proof(s, name):
            p = home / name
            s.ex("call writefile([expand('%:p'), string(&modified)], '" + str(p) + "')")
            s.until(p.exists)
            return p.read_text().splitlines()

        try:
            for doc in [note, task]:
                s = session(["document", "edit", "--id", doc["id"]])
                s.until(lambda: ("original " + doc["kind"]).encode() in s.output)
                path = root / "content" / doc["path"]
                assert proof(s, doc["kind"] + "-path")[0] == str(path)
                inode = path.stat().st_ino
                s.ex("set backupcopy=no writebackup nobackup")
                if doc["kind"] == "task":
                    s.ex("%s/^state: planned$/state: in_progress/")
                marker = "atomic " + doc["kind"] + " save"
                s.send(b"Go" + marker.encode() + b"\x1b")
                s.ex("w")
                s.until(lambda: marker in path.read_text())
                assert inode != path.stat().st_ino, "Neovim save was not atomic replacement"
                s.ex("q")
                s.close()
            got = cli("document", "get", "--id", task["id"])
            assert got["state"] == "in_progress" and len(got["history"]) == 2, got
            assert "atomic note save" in cli("document", "get", "--id", note["id"])["body"]
            print("PASS explicit CLI edit -> installed Neovim: note/task actual paths, atomic inode replacement, saved content, source history, clean quit and terminal restoration")

            for doc in [note, task]:
                s = session(["document", "edit", "--id", doc["id"]])
                s.until(lambda: b"atomic " in s.output)
                s.ex("set noautoread")
                s.send(b"GoUNSAVED PRIVATE BUFFER\x1b")
                path = root / "content" / doc["path"]

                def external(marker):
                    temporary = path.with_suffix(".owned-temp")
                    temporary.write_text(path.read_text() + marker + "\n")
                    temporary.replace(path)

                external("EXTERNAL ONE")
                before = len(s.output)
                s.ex("checktime")
                s.until(lambda: b"W12" in s.output[before:] or b"has changed" in s.output[before:])
                s.send(b"o")  # Native Neovim (O)K: keep dirty buffer, cancel reload.
                data = proof(s, doc["kind"] + "-kept")
                assert data[0] == str(path) and data[1] == "1", data
                kept = home / (doc["kind"] + "-buffer")
                s.ex("call writefile(getline(1,'$'), '" + str(kept) + "')")
                s.until(kept.exists)
                assert "UNSAVED PRIVATE BUFFER" in kept.read_text() and "EXTERNAL ONE" not in kept.read_text()
                assert "EXTERNAL ONE" in path.read_text() and "UNSAVED PRIVATE BUFFER" not in path.read_text()
                external("EXTERNAL TWO")
                before = len(s.output)
                s.ex("checktime")
                s.until(lambda: b"W12" in s.output[before:] or b"has changed" in s.output[before:])
                s.send(b"l")  # Native Neovim (L)oad File, discarding dirty buffer.
                loaded = home / (doc["kind"] + "-loaded")
                s.ex("call writefile(getline(1,'$'), '" + str(loaded) + "')")
                s.until(loaded.exists)
                assert "EXTERNAL TWO" in loaded.read_text() and "UNSAVED PRIVATE BUFFER" not in loaded.read_text(), loaded.read_text()
                s.ex("q!")
                s.close()
                assert "EXTERNAL TWO" in cli("document", "get", "--id", doc["id"])["body"]
                print("PASS CLI -> installed Neovim:", doc["kind"], "dirty external-change W12; native OK/cancel preserves dirty buffer+disk; native Load reloads; terminal restored")

        finally:
            for s in sessions:
                s.cleanup()
        print("LIMIT: automated terminal assertions only; no human visual judgement or personal editor config tested")


if __name__ == "__main__":
    main()
