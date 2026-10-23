"""Developer-only packaging/inspection; no Python dependency in the release."""
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import platform
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.request

REPO = Path(__file__).resolve().parent.parent
CACHE = REPO / "build/validation-assets"
RUNTIME = json.loads((REPO / "assets/runtime.lock.json").read_text())
MODEL = json.loads((REPO / "assets/model.lock.json").read_text())
ARCHIVE = REPO / "build/recall-darwin-arm64.tar.gz"
DOCS = ["README.md", "docs/cli.md", "docs/agents.md", "docs/operations.md", "docs/verification.md"]
COMMIT = "620d2225cc436ab0b001e3200e590e3b286ef9ab"  # llama.cpp b11669


def require(ok, message):
    if not ok:
        raise ValueError(message)


def digest(path):
    with path.open("rb") as f:
        return hashlib.file_digest(f, "sha256").hexdigest()


def pinned(path, manifest):
    require(path.is_file() and not path.is_symlink(), "asset must be a regular file")
    require(path.stat().st_size == manifest["size"] and digest(path) == manifest["sha256"], "asset checksum/size mismatch")


def inspect(tf, prefix, runtime=False):
    """Validate the entire graph before any extraction, including link ancestors."""
    entries = {}
    total = 0
    for m in tf.getmembers():
        name = m.name.rstrip("/")
        parts = name.split("/")
        require(name and not name.startswith("/") and all(p not in ("", ".", "..") for p in parts), "unsafe archive path")
        require(parts[0] == prefix and name not in entries, "wrong archive prefix or duplicate path")
        require(m.isfile() or m.isdir() or m.issym(), "unsupported archive entry")
        require(m.issym() or not m.mode & 0o7022, "unsafe archive permissions")
        if m.issym():
            require(runtime and name.endswith(".dylib") and not m.linkname.startswith("/") and "\\" not in m.linkname, "unsafe archive link")
        entries[name] = m
        total += m.size
        require(total <= 512 * 1024 * 1024, "oversized archive")
    def resolve(name, seen):
        require(name in entries and name not in seen, "dangling or cyclic link")
        m = entries[name]
        if not m.issym():
            require(m.isfile(), "link target is not a regular library")
            return
        # Normalize only the relative link, never an archive member name.
        target = os.path.normpath(str(PurePosixPath(name).parent / m.linkname))
        require(target.startswith(prefix + "/") and target.endswith(".dylib"), "escaping archive link")
        resolve(target, seen | {name})
    for name, m in entries.items():
        for parent in PurePosixPath(name).parents:
            if str(parent) in entries:
                require(entries[str(parent)].isdir(), "archive writes through a non-directory")
        if m.issym():
            resolve(name, set())
    if runtime:
        server = entries.get(prefix + "/llama-server")
        require(server and server.isfile() and server.mode & 0o111, "missing executable llama-server")
    return entries


def extract(tf, dest, prefix, runtime=False):
    entries = inspect(tf, prefix, runtime)
    for name, m in entries.items():
        target = dest / name
        target.parent.mkdir(parents=True, exist_ok=True)
        if m.isdir():
            target.mkdir(exist_ok=True)
        elif m.isfile():
            with target.open("xb") as out, tf.extractfile(m) as source:
                shutil.copyfileobj(source, out)
            target.chmod(m.mode & 0o755)
    for name, m in entries.items():
        if m.issym():
            (dest / name).symlink_to(m.linkname)


def fetch(url, path):
    path.parent.mkdir(parents=True, exist_ok=True)
    if not path.exists():
        temporary = path.with_suffix(path.suffix + ".partial")
        try:
            with urllib.request.urlopen(url, timeout=120) as source, temporary.open("wb") as out:
                shutil.copyfileobj(source, out)
            temporary.replace(path)
        finally:
            temporary.unlink(missing_ok=True)
    return path


def notices(dest):
    dest.mkdir()
    # Keep complete upstream files (including embedded header licenses) rather
    # than silently omitting license/copyright text when upstream layout changes.
    native = ["LICENSE", "licenses/LICENSE-jsonhpp", "vendor/cpp-httplib/LICENSE",
              "vendor/hash/rotate-bits/LICENSE.md", "vendor/hash/sha256/LICENSE",
              "vendor/hash/xxhash/LICENSE", "vendor/hash/sha1/sha1.c",
              "vendor/miniaudio/miniaudio.h", "vendor/sheredom/subprocess.h", "vendor/stb/stb_image.h",
              "ggml/src/ggml-cpu/llamafile/sgemm.cpp"]
    for name in native:
        source = fetch(f"https://raw.githubusercontent.com/ggml-org/llama.cpp/{COMMIT}/{name}", CACHE / "notices/llama" / name)
        target = dest / "llama.cpp" / name
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, target)
    apache = fetch("https://www.apache.org/licenses/LICENSE-2.0.txt", CACHE / "notices/Apache-2.0.txt")
    shutil.copyfile(apache, dest / "nomic-Apache-2.0.txt")
    # The upstream macOS payload statically links BoringSSL (confirmed by nm,
    # not visible in otool -L). Version comes from pinned cpp-httplib CMake.
    boring = fetch("https://raw.githubusercontent.com/google/boringssl/0.20260929.0/LICENSE", CACHE / "notices/BoringSSL-0.20260929.0-LICENSE")
    shutil.copyfile(boring, dest / "BoringSSL-0.20260929.0-LICENSE")
    fiat = fetch("https://raw.githubusercontent.com/google/boringssl/427ec40cc8edc545253289232e885708c409e5ea/third_party/fiat/LICENSE", CACHE / "notices/BoringSSL-fiat-LICENSE")
    shutil.copyfile(fiat, dest / "BoringSSL-fiat-LICENSE")
    # Collect the actual linked Go module graph, not licenses for Recall itself.
    raw = subprocess.check_output(["go", "list", "-deps", "-json", "./cmd/recall"], cwd=REPO).decode()
    decoder, modules = json.JSONDecoder(), {}
    while raw.strip():
        obj, end = decoder.raw_decode(raw.lstrip())
        raw = raw.lstrip()[end:]
        mod = obj.get("Module", {})
        if mod and not mod.get("Main"):
            modules[mod["Path"] + "@" + mod["Version"]] = Path(mod["Dir"])
    records = []
    for name, source in sorted(modules.items()):
        files = [p for p in source.rglob("*") if p.is_file() and p.name.lower().startswith(("license", "copying", "notice", "copyright", "patents"))]
        require(files, "no license found for " + name)
        for p in files:
            target = dest / "go" / name / p.relative_to(source)
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(p, target)
        records.append(name)
    goroot = Path(subprocess.check_output(["go", "env", "GOROOT"], text=True).strip())
    for name in ["LICENSE", "PATENTS"]:
        # Homebrew moves LICENSE one level above libexec/GOROOT.
        source = goroot / name
        if not source.is_file():
            source = goroot.parent / name
        shutil.copyfile(source, dest / ("Go-" + name))
    (dest / "README.txt").write_text(
        "Third-party notices only; no license is granted here for Recall's own code.\n"
        f"llama.cpp b11669 source {COMMIT}; full vendor headers retained for notices.\n"
        "Statically linked BoringSSL 0.20260929.0 license included separately.\n"
        "Nomic Embed Text v1.5, Nomic AI, Apache-2.0; model weights downloaded separately.\n"
        "https://huggingface.co/nomic-ai/nomic-embed-text-v1.5-GGUF\n"
        "Apple system libraries/frameworks are OS-provided, not redistributed.\n"
        "Linked Go modules (plus Go standard library):\n" + "\n".join(records) + "\n")


def package():
    require(platform.system() == "Darwin" and platform.machine() == "arm64", "packaging requires Darwin/arm64 for native verification")
    runtime = Path(os.environ.get("RECALL_RUNTIME_ARCHIVE", CACHE / RUNTIME["filename"]))
    if not runtime.exists() and "RECALL_RUNTIME_ARCHIVE" not in os.environ:
        fetch(RUNTIME["url"], runtime)
    pinned(runtime, RUNTIME)
    with tarfile.open(runtime) as tf:
        inspect(tf, "llama-b11669", True)
    ARCHIVE.parent.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="recall-package-", dir=ARCHIVE.parent) as tmp:
        root = Path(tmp) / "recall-darwin-arm64"
        bindir = root / "bin"
        bindir.mkdir(parents=True)
        env = dict(os.environ, CGO_ENABLED="0", GOOS="darwin", GOARCH="arm64")
        subprocess.run(["go", "build", "-trimpath", "-o", str(bindir / "recall"), "./cmd/recall"], cwd=REPO, env=env, check=True)
        target = bindir / "assets/runtime/darwin-arm64" / RUNTIME["filename"]
        target.parent.mkdir(parents=True)
        shutil.copyfile(runtime, target)
        for name in ["runtime", "model"]:
            shutil.copyfile(REPO / f"assets/{name}.lock.json", bindir / "assets" / f"{name}.lock.json")
        for name in DOCS:
            target = root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(REPO / name, target)
        notices(root / "notices")
        # Package modes are deliberate, independent of the developer's umask.
        for p in root.rglob("*"):
            p.chmod(0o755 if p.is_dir() or p == bindir / "recall" else 0o644)
        root.chmod(0o755)
        with tarfile.open(ARCHIVE, "w:gz") as tf:
            tf.add(root, arcname=root.name)
    ARCHIVE.with_suffix(ARCHIVE.suffix + ".sha256").write_text(digest(ARCHIVE) + "  " + ARCHIVE.name + "\n")
    print(ARCHIVE.relative_to(REPO))


def verify(path):
    expected, name = path.with_suffix(path.suffix + ".sha256").read_text().strip().split()
    require(name == path.name and expected == digest(path), "release checksum mismatch")
    with tempfile.TemporaryDirectory(prefix="recall relocated with spaces ") as tmp:
        dest = Path(tmp)
        with tarfile.open(path) as tf:
            extract(tf, dest, "recall-darwin-arm64")
        root = dest / "recall-darwin-arm64"
        for name in DOCS + ["notices/README.txt", "notices/nomic-Apache-2.0.txt", "notices/Go-LICENSE", "notices/llama.cpp/LICENSE", "notices/BoringSSL-0.20260929.0-LICENSE", "notices/BoringSSL-fiat-LICENSE"]:
            require((root / name).is_file() and (root / name).stat().st_size > 0, "missing documentation/notice: " + name)
        require(any((root / "notices/go").rglob("LICENSE*")), "missing linked dependency notices")
        for name in ["runtime", "model"]:
            require((root / f"bin/assets/{name}.lock.json").read_bytes() == (REPO / f"assets/{name}.lock.json").read_bytes(), "manifest mismatch")
        require(not list(root.rglob("*.gguf")), "weights must not be in the release")
        binary = root / "bin/recall"
        require(binary.stat().st_mode & 0o111, "recall is not executable")
        nested = root / "bin/assets/runtime/darwin-arm64" / RUNTIME["filename"]
        pinned(nested, RUNTIME)
        with tarfile.open(nested) as tf:
            extract(tf, dest, "llama-b11669", True)
        native = dest / "llama-b11669"
        count = 0
        for p in native.iterdir():
            if not p.is_file() or p.is_symlink() or p.name == "LICENSE":
                continue
            libs = subprocess.check_output(["otool", "-L", str(p)], text=True)
            for line in libs.splitlines()[1:]:
                lib = line.strip().split(" (", 1)[0]
                if lib.startswith(("@rpath/", "@loader_path/", "@executable_path/")):
                    require((native / lib.split("/", 1)[1]).is_file(), "missing relocated dependency: " + lib)
                else:
                    require(lib.startswith(("/usr/lib/", "/System/Library/")), "nonrelocatable dependency: " + lib)
            count += 1
        env = {k:v for k,v in os.environ.items() if not k.startswith(("DYLD_", "LLAMA_", "RECALL_"))}
        env["HOME"] = str(dest)
        subprocess.run([str(binary), "--help"], env=env, cwd=dest, stdout=subprocess.DEVNULL, check=True)
        subprocess.run([str(native / "llama-server"), "--version"], env=env, cwd=dest, capture_output=True, timeout=20, check=True)
        print(f"PASS release checksum, permissions, safe links, notices, no weights; {count} Mach-O files relocated; native --version startup (not model inference)")


if __name__ == "__main__":
    try:
        mode = sys.argv[1]
        if mode == "check-runtime":
            with tarfile.open(sys.argv[2]) as tf:
                inspect(tf, "llama-b11669", True)
        elif mode == "package":
            require(len(sys.argv) == 2, "package takes no arguments")
            package()
        elif mode == "verify":
            require(len(sys.argv) <= 3, "verify takes at most one archive path")
            verify(Path(sys.argv[2]).resolve() if len(sys.argv) == 3 else ARCHIVE)
        else:
            raise ValueError("unknown distribution operation")
    except (ValueError, OSError, tarfile.TarError, subprocess.SubprocessError) as e:
        sys.exit("distribution: " + str(e))
