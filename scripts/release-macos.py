"""Release-only Developer ID packaging. No unsigned publication path.

The release runs as three stages on separate runners:

  runtime  (Apple secrets)  sign + notarize the pinned upstream llama.cpp runtime
  build    (no secrets)     compile the CLI/desktop app against that signed runtime
  sign     (Apple secrets)  sign, notarize and package the built payload

Third-party build code (Go modules, npm packages, Wails) runs ONLY in the build
stage. While signing credentials are loaded, run() refuses every executable
except Apple's signing/packaging tools, so a compromised dependency can never
use the Developer ID keychain or notarization profile. Code produced by the
build (the app, CLI and native runtime) is executed for verification only after
the temporary keychain has been deleted.
"""
import base64
from contextlib import contextmanager
import json
import os
from pathlib import Path, PurePosixPath
import platform
import plistlib
import re
import secrets
import shlex
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
from xml.sax.saxutils import escape

import distribution as dist

REPO = Path(__file__).resolve().parent.parent
PRIVATE_ENV = {"APPLICATION_P12_BASE64", "INSTALLER_P12_BASE64", "APPLICATION_P12_PASSWORD",
               "INSTALLER_P12_PASSWORD", "NOTARY_APPLE_ID", "NOTARY_PASSWORD"}
# The only executables permitted while the signing keychain exists.
APPLE_TOOLS = {"security", "codesign", "xcrun", "ditto", "pkgbuild", "productbuild"}
PAYLOAD_ENTRIES = {"recall", "Recall.app", "assets", "notices"}
_diagnostic_redactions = set()
_credentials_live = False


def run(args, **kwargs):
    tool = Path(str(args[0])).name
    if _credentials_live and tool not in APPLE_TOOLS:
        raise RuntimeError(f"refusing to run {tool} while signing credentials are loaded")
    # Never let CalledProcessError stringify password-bearing argument lists.
    env = kwargs.pop("env", os.environ)
    env = {k: v for k, v in env.items() if k not in PRIVATE_ENV}
    result = subprocess.run([str(a) for a in args], stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, text=True, env=env, **kwargs)
    if result.returncode:
        is_stapler = tool == "xcrun" and len(args) > 2 and str(args[1]) == "stapler"
        if tool == "codesign" or is_stapler:
            # codesign and stapler receive identity/keychain names and file
            # paths, never passwords. Expose only the target and diagnostic,
            # not command arguments, and retain redaction even after
            # credentials have been removed from the env.
            if is_stapler:
                tool, operation = "stapler", str(args[2])
            else:
                operation = "verification" if "--verify" in args else "signing"
            detail = f"{Path(args[-1]).name}: {result.stderr.strip() or result.stdout.strip()}"
            sensitive = _diagnostic_redactions | {os.environ.get(k, "") for k in PRIVATE_ENV | {"TEAM_ID"}}
            for value in sorted(filter(None, sensitive), key=len, reverse=True):
                detail = detail.replace(value, "[REDACTED]")
            raise RuntimeError(f"{tool} {operation} failed (exit {result.returncode}): " + json.dumps(detail[:8192], ensure_ascii=True))
        raise RuntimeError(f"{tool} failed (exit {result.returncode}); command arguments/output withheld")
    return result.stdout


def version(tag):
    number = r"(?:0|[1-9][0-9]*)"
    identifier = r"(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)"
    if not re.fullmatch(rf"v{number}\.{number}\.{number}(?:-{identifier}(?:\.{identifier})*)?", tag):
        raise ValueError("release tag must be vMAJOR.MINOR.PATCH, optionally with a SemVer prerelease suffix")
    return tag[1:]


def require_darwin():
    dist.require(platform.system() == "Darwin" and platform.machine() == "arm64", "release requires Darwin arm64")


def require_no_credentials():
    present = sorted(name for name in PRIVATE_ENV | {"SIGNING_KEYCHAIN"} if name in os.environ)
    dist.require(not present, "the build stage must not receive Apple credentials: " + ", ".join(present))


def team_id():
    team = os.environ.get("TEAM_ID", "")
    dist.require(re.fullmatch(r"[A-Z0-9]{10}", team), "missing/invalid Apple Team ID")
    return team


def go_toolchain(gomod):
    match = re.search(r"^toolchain (go[0-9][^\s]*)$", gomod, re.M)
    dist.require(match, "go.mod must pin a toolchain for release builds")
    return match.group(1)


def require_go_toolchain(source):
    # Shipped binaries must not silently use an older, unpatched Go release.
    for module in (source, source / "desktop"):
        want = go_toolchain((module / "go.mod").read_text())
        got = run(["go", "env", "GOVERSION"], cwd=module).strip()
        dist.require(got == want, f"release must build with {want}, not {got}")


def macho(path):
    if path.is_symlink() or not path.is_file():
        return False
    with path.open("rb") as source:
        return source.read(4) in (b"\xcf\xfa\xed\xfe", b"\xfe\xed\xfa\xcf", b"\xca\xfe\xba\xbe", b"\xbe\xba\xfe\xca")


def notarize(path):
    result = json.loads(run(["xcrun", "notarytool", "submit", path, "--wait", "--timeout", "20m",
                             "--keychain", os.environ["SIGNING_KEYCHAIN"],
                             "--keychain-profile", "recall-release", "--output-format", "json"]))
    if result.get("status") != "Accepted":
        raise RuntimeError("Apple notarization was not Accepted; submission " + str(result.get("id", "unknown")))
    print("Apple notarization Accepted:", result["id"], flush=True)


def stapler(action, path, attempts=5, delay=30):
    """Apple's ticket service can briefly fail to serve a just-notarized ticket
    (stapler exit 65/68). Retry a bounded number of times, reporting each
    diagnostic; a persistent failure still fails the release."""
    for attempt in range(1, attempts + 1):
        try:
            run(["xcrun", "stapler", action, path])
            return
        except RuntimeError as error:
            if attempt == attempts:
                raise
            print(f"{error} (attempt {attempt}/{attempts}; retrying in {delay}s)", flush=True)
            time.sleep(delay)


def verifier(team):
    requirement = '=anchor apple generic and certificate leaf[subject.OU] = "' + team + '" and certificate leaf[field.1.2.840.113635.100.6.1.13] exists'
    def verify(path):
        run(["codesign", "--verify", "--strict", "-R", requirement, path])
    return verify


def signer(keychain, identity, verify):
    def sign(path):
        print("Signing native code:", json.dumps(path.name), flush=True)
        run(["codesign", "--force", "--options", "runtime", "--timestamp", "--keychain", keychain,
             "--sign", identity, path])
        verify(path)
    return sign


def prepare_runtime(archive, lock, assets, sign, submit):
    # Validate pristine upstream input before executing or modifying any bytes.
    dist.pinned(archive, lock)
    assets.mkdir(parents=True)
    with tempfile.TemporaryDirectory(prefix="signed-runtime-", dir=assets.parent) as tmp:
        work = Path(tmp)
        with tarfile.open(archive) as tf:
            dist.extract(tf, work, "llama-b11669", True)
        native = work / "llama-b11669"
        paths = sorted(p for p in native.rglob("*") if macho(p))
        dist.require(paths, "runtime has no Mach-O files")
        for path in paths:
            sign(path)
        # Notarize uncompressed code in a supported ZIP container, not just an
        # opaque tarball nested inside the final installer.
        zipfile = work / "runtime.zip"
        run(["ditto", "-c", "-k", "--keepParent", native, zipfile])
        submit(zipfile)
        target = assets / "runtime/darwin-arm64" / lock["filename"]
        target.parent.mkdir(parents=True)
        with tarfile.open(target, "w:gz") as tf:
            tf.add(native, arcname="llama-b11669")
    # Signing changed the payload: the upstream tree digest no longer applies.
    # The build stage pins the signed payload's digest with Recall's own code.
    signed = {k: v for k, v in lock.items() if k != "tree_sha256"}
    signed.update(sha256=dist.digest(target), size=target.stat().st_size)
    signed["installation_id"] = signed["id"] + "-" + signed["sha256"]
    (assets / "runtime.lock.json").write_text(json.dumps(signed, indent=2) + "\n")
    dist.pinned(target, signed)
    return signed


def signed_runtime(runtime_dir):
    """Validate the runtime stage's artifact against the repository's upstream pin."""
    lock = json.loads((runtime_dir / "runtime.lock.json").read_text())
    upstream = {k: v for k, v in dist.RUNTIME.items() if k not in ("sha256", "size", "tree_sha256")}
    dist.require(set(lock) == set(upstream) | {"sha256", "size", "installation_id"}, "unexpected signed runtime lock fields")
    dist.require({k: lock[k] for k in upstream} == upstream, "signed runtime lock does not describe the pinned upstream runtime")
    dist.require(re.fullmatch(r"[0-9a-f]{64}", str(lock["sha256"])) and lock["installation_id"] == lock["id"] + "-" + lock["sha256"], "invalid signed runtime identity")
    archive = runtime_dir / "runtime/darwin-arm64" / lock["filename"]
    dist.pinned(archive, lock)
    with tarfile.open(archive) as tf:
        dist.inspect(tf, "llama-b11669", True)
    return lock, archive


def verify_runtime(archive, verify):
    # Checks extracted runtime bytes, not merely the archive. Executes upstream
    # code, so callers must invoke this only after credentials are removed.
    with tempfile.TemporaryDirectory(prefix="verify-runtime-") as tmp:
        extracted = Path(tmp)
        with tarfile.open(archive) as tf:
            dist.extract(tf, extracted, "llama-b11669", True)
        for path in extracted.rglob("*"):
            if macho(path):
                verify(path)
        run([extracted / "llama-b11669/llama-server", "--version"])


def set_bundle_version(app, release):
    plist = app / "Contents/Info.plist"
    info = plistlib.loads(plist.read_bytes())
    info["CFBundleVersion"] = release.split("-")[0]
    info["CFBundleShortVersionString"] = release.split("-")[0]
    plist.write_bytes(plistlib.dumps(info))


def make_payload(root, cli, app, assets, notices):
    lib = root / "usr/local/lib/recall"
    lib.mkdir(parents=True)
    shutil.copy2(cli, lib / "recall")
    (lib / "recall").chmod(0o755)
    shutil.copytree(assets, lib / "assets", symlinks=True)
    shutil.copytree(notices, lib / "notices", symlinks=True)
    (root / "usr/local/bin").mkdir(parents=True)
    (root / "usr/local/bin/recall").symlink_to("../lib/recall/recall")
    (root / "Applications").mkdir()
    run(["ditto", app, root / "Applications/Recall.app"])


def component_rules():
    return [{"RootRelativeBundlePath": "Applications/Recall.app", "BundleIsRelocatable": False,
             "BundleIsVersionChecked": False, "BundleHasStrictIdentifier": True,
             "BundleOverwriteAction": "upgrade"}]


def build_component(payload, work, release):
    rules = work / "components.plist"
    rules.write_bytes(plistlib.dumps(component_rules()))
    run(["pkgbuild", "--root", payload, "--identifier", "local.recall.installer", "--version", release.split("-")[0],
         "--install-location", "/", "--ownership", "recommended", "--component-plist", rules, work / "Recall-component.pkg"])


def distribution_xml(release):
    numeric = escape(release.split("-")[0])
    return f'''<?xml version="1.0" encoding="utf-8"?>
<installer-gui-script minSpecVersion="2">
  <title>Recall</title>
  <options customize="never" require-scripts="false" hostArchitectures="arm64" rootVolumeOnly="true"/>
  <domains enable_localSystem="true" enable_currentUserHome="false" enable_anywhere="false"/>
  <volume-check><allowed-os-versions><os-version min="13.0"/></allowed-os-versions></volume-check>
  <choices-outline><line choice="default"/></choices-outline>
  <choice id="default" visible="false"><pkg-ref id="local.recall.installer"/></choice>
  <pkg-ref id="local.recall.installer" version="{numeric}" onConclusion="none">Recall-component.pkg</pkg-ref>
</installer-gui-script>
'''


def finish_package(pkg, output, release):
    # Runs after credentials are removed; the package is already notarized/stapled.
    dist.require(not output.exists(), "release output already exists; refusing stale artifacts")
    stapler("validate", pkg)
    run(["pkgutil", "--check-signature", pkg])
    run(["spctl", "--assess", "--type", "install", "--verbose=2", pkg])
    output.mkdir(parents=True)
    target = output / f"Recall-{release}-darwin-arm64.pkg"
    shutil.copy2(pkg, target)
    target.with_suffix(".pkg.sha256").write_text(dist.digest(target) + "  " + target.name + "\n")


@contextmanager
def signing_credentials(work, kinds=("Application", "Installer")):
    global _credentials_live
    team = team_id()
    keychain = work / "release.keychain-db"
    password = secrets.token_urlsafe(32)
    previous_redactions = _diagnostic_redactions.copy()
    _diagnostic_redactions.update(os.environ.get(k, "") for k in PRIVATE_ENV | {"TEAM_ID"})
    _diagnostic_redactions.add(password)
    created = False
    search_list_changed = False
    original_search_list = []
    _credentials_live = True
    try:
        original_search_list = shlex.split(run(["security", "list-keychains", "-d", "user"]))
        run(["security", "create-keychain", "-p", password, keychain])
        created = True
        run(["security", "set-keychain-settings", "-lut", "21600", keychain])
        run(["security", "unlock-keychain", "-p", password, keychain])
        for kind in kinds:
            certificate = work / (kind.lower() + ".p12")
            encoded = "".join(os.environ[kind.upper() + "_P12_BASE64"].split())
            with certificate.open("xb") as dest:
                os.chmod(certificate, 0o600)
                dest.write(base64.b64decode(encoded, validate=True))
            run(["security", "import", certificate, "-k", keychain, "-P", os.environ[kind.upper() + "_P12_PASSWORD"],
                 "-T", "/usr/bin/codesign", "-T", "/usr/bin/productbuild"])
            certificate.unlink()
        run(["security", "set-key-partition-list", "-S", "apple-tool:,apple:,codesign:", "-s", "-k", password, keychain])
        # --keychain narrows identity lookup but does not add this file to the
        # search list used by codesign to construct the certificate chain.
        search_list_changed = True
        run(["security", "list-keychains", "-d", "user", "-s", keychain, *original_search_list])
        identities = run(["security", "find-identity", "-v", keychain])
        selected = {}
        for kind in kinds:
            matches = re.findall(r'\b([0-9A-Fa-f]{40})\s+"(Developer ID ' + kind + r': [^"\n]+ \(' + team + r'\))"', identities)
            dist.require(len(matches) == 1, "expected exactly one valid Developer ID " + kind + " identity for requested team")
            # codesign explicitly supports the certificate SHA-1 as an exact
            # identity selector; productbuild documents the certificate CN.
            selected[kind] = matches[0][0 if kind == "Application" else 1]
        run(["xcrun", "notarytool", "store-credentials", "recall-release", "--keychain", keychain,
             "--apple-id", os.environ["NOTARY_APPLE_ID"], "--team-id", team,
             "--password", os.environ["NOTARY_PASSWORD"]])
        # Subprocesses must not inherit certificates or Apple passwords.
        for name in PRIVATE_ENV:
            os.environ.pop(name, None)
        os.environ["SIGNING_KEYCHAIN"] = str(keychain)
        yield keychain, selected
    finally:
        os.environ.pop("SIGNING_KEYCHAIN", None)
        try:
            try:
                if search_list_changed:
                    run(["security", "list-keychains", "-d", "user", "-s", *original_search_list])
            finally:
                if created:
                    run(["security", "delete-keychain", keychain])
        finally:
            _credentials_live = False
            _diagnostic_redactions.clear()
            _diagnostic_redactions.update(previous_redactions)


def source_snapshot(dest):
    # Only tracked source inputs; exclude local caches, signing material and outputs.
    tracked = subprocess.check_output(["git", "ls-files", "-z"], cwd=REPO).decode().split("\0")
    for name in filter(None, tracked):
        source = REPO / name
        dist.require(not source.is_symlink(), "unexpected source symlink")
        target = dest / name
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source, target)


def desktop_notices(source, notices):
    # Reuse CLI/runtime notices, then add Wails/desktop Go and frontend dependencies.
    raw = run(["go", "list", "-deps", "-json", "."], cwd=source / "desktop")
    decoder = json.JSONDecoder()
    while raw.strip():
        obj, end = decoder.raw_decode(raw.lstrip())
        raw = raw.lstrip()[end:]
        mod = obj.get("Module", {})
        if not mod or mod.get("Main") or mod.get("Path") == "recall":
            continue
        directory = Path(mod["Dir"])
        for path in directory.rglob("*"):
            if path.is_file() and path.name.lower().startswith(("license", "copying", "notice", "copyright", "patents")):
                target = notices / "go" / (mod["Path"] + "@" + mod["Version"]) / path.relative_to(directory)
                if target.suffix == ".go":
                    target = target.with_suffix(".go.txt")
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(path, target)
    modules = source / "desktop/frontend/node_modules"
    for path in modules.rglob("*"):
        if not path.is_symlink() and path.is_file() and path.name.lower().startswith(("license", "copying", "notice")):
            target = notices / "npm" / path.relative_to(modules)
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(path, target)


def write_payload(staged, archive):
    """Hand the unsigned payload to the signing stage. Modes are normalized so
    the signing stage can reject anything unusual instead of trusting it."""
    def normalize(info):
        dist.require(info.isfile() or info.isdir(), "release payload may contain only regular files and directories")
        info.uid = info.gid = 0
        info.uname = info.gname = ""
        info.mode = 0o755 if info.isdir() or info.mode & 0o100 else 0o644
        return info
    with tarfile.open(archive, "x") as tf:
        for name in sorted(PAYLOAD_ENTRIES):
            tf.add(staged / name, arcname=name, filter=normalize)


def read_payload(archive, dest):
    with tarfile.open(archive) as tf:
        members = tf.getmembers()
        for m in members:
            parts = PurePosixPath(m.name).parts
            dist.require(m.name and not m.name.startswith("/") and ".." not in parts and parts[0] in PAYLOAD_ENTRIES, "unexpected release payload entry")
            dist.require(m.isfile() or m.isdir(), "release payload may contain only regular files and directories")
            dist.require(not m.mode & 0o7022, "unsafe release payload permissions")
        dist.require({PurePosixPath(m.name).parts[0] for m in members} == PAYLOAD_ENTRIES, "incomplete release payload")
        dest.mkdir()
        tf.extractall(dest, members=members, filter="data")


def check_payload(staged, release, runtime_lock, runtime_archive):
    """Independently confirm the build stage used the runtime stage's exact bytes."""
    cli, app, assets = staged / "recall", staged / "Recall.app", staged / "assets"
    dist.require(cli.is_file() and cli.stat().st_mode & 0o111, "release payload CLI is missing or not executable")
    info = plistlib.loads((app / "Contents/Info.plist").read_bytes())
    numeric = release.split("-")[0]
    dist.require(info.get("CFBundleIdentifier") == "local.recall.desktop" and info.get("CFBundleShortVersionString") == numeric and info.get("CFBundleVersion") == numeric, "release payload app identity/version mismatch")
    archive = f"runtime/darwin-arm64/{runtime_lock['filename']}"
    files = {p.relative_to(assets).as_posix() for p in assets.rglob("*") if not p.is_dir()}
    dist.require(files == {"runtime.lock.json", "model.lock.json", archive}, "unexpected release payload assets")
    dist.require((assets / "model.lock.json").read_bytes() == (REPO / "assets/model.lock.json").read_bytes(), "release payload model pin mismatch")
    lock = json.loads((assets / "runtime.lock.json").read_text())
    dist.require(re.fullmatch(r"[0-9a-f]{64}", str(lock.get("tree_sha256", ""))), "release payload runtime lock lacks its payload digest")
    dist.require({k: v for k, v in lock.items() if k != "tree_sha256"} == runtime_lock, "release payload runtime lock differs from the signed runtime")
    dist.require(dist.digest(assets / archive) == dist.digest(runtime_archive), "release payload runtime differs from the signed runtime")
    bundled = app / "Contents/Resources/assets"
    bundled_files = {p.relative_to(bundled).as_posix() for p in bundled.rglob("*") if not p.is_dir()} if bundled.is_dir() else set()
    dist.require(bundled_files == files and all((bundled / f).read_bytes() == (assets / f).read_bytes() for f in files), "desktop app assets differ from the release assets")


def sign_and_notarize(work, staged, release, keychain, identities, verify):
    """Runs with credentials loaded: Apple tools and this file's Python only."""
    cli, app = staged / "recall", staged / "Recall.app"
    sign = signer(keychain, identities["Application"], verify)
    sign(cli)
    sign(app / "Contents/MacOS/Recall")
    sign(app)
    app_zip = work / "Recall.zip"
    run(["ditto", "-c", "-k", "--keepParent", app, app_zip])
    notarize(app_zip)
    stapler("staple", app)
    payload = work / "payload"
    make_payload(payload, cli, app, staged / "assets", staged / "notices")
    build_component(payload, work, release)
    xml = work / "Distribution.xml"
    xml.write_text(distribution_xml(release))
    pkg = work / "Recall.pkg"
    run(["productbuild", "--distribution", xml, "--package-path", work, "--keychain", keychain,
         "--sign", identities["Installer"], pkg])
    notarize(pkg)
    stapler("staple", pkg)
    return pkg


def verify_signed(work, pkg, staged, runtime_lock, verify):
    """Runs after the keychain is deleted, so executing built code is safe."""
    cli, app, assets = staged / "recall", staged / "Recall.app", staged / "assets"
    verify(cli)
    verify(app)
    stapler("validate", app)
    run(["spctl", "--assess", "--type", "execute", "--verbose=2", app])
    run([sys.executable, "scripts/verify-desktop.py", app], cwd=REPO)
    verify_runtime(assets / "runtime/darwin-arm64" / runtime_lock["filename"], verify)
    run([cli, "--help"])
    # Expand the actual installer before publication.
    expanded = work / "expanded"
    run(["pkgutil", "--expand-full", pkg, expanded])
    installed = expanded / "Recall-component.pkg/Payload"
    dist.require((installed / "usr/local/bin/recall").readlink().as_posix() == "../lib/recall/recall", "installer CLI link mismatch")
    dist.require((installed / "usr/local/lib/recall/assets/runtime.lock.json").read_bytes() == (assets / "runtime.lock.json").read_bytes(), "installer runtime pin mismatch")
    verify(installed / "usr/local/lib/recall/recall")
    verify(installed / "Applications/Recall.app")


@contextmanager
def private_workdir():
    # Exclusively created by this invocation (never a broad delete); the
    # workflow's always-run cleanup also covers interrupted Python execution.
    REPO.joinpath("build").mkdir(exist_ok=True)
    work = REPO / "build/release-private"
    work.mkdir(mode=0o700)
    try:
        yield work
    finally:
        shutil.rmtree(work)


def runtime_stage(tag, output):
    version(tag)
    require_darwin()
    verify = verifier(team_id())
    dist.require(not output.exists(), "runtime output already exists")
    # Network and pin checks happen before any credential is loaded.
    archive = dist.fetch(dist.RUNTIME["url"], dist.CACHE / dist.RUNTIME["filename"])
    dist.pinned(archive, dist.RUNTIME)
    with private_workdir() as work:
        with signing_credentials(work, kinds=("Application",)) as (keychain, identities):
            signed = prepare_runtime(archive, dist.RUNTIME, output, signer(keychain, identities["Application"], verify), notarize)
    verify_runtime(output / "runtime/darwin-arm64" / signed["filename"], verify)
    print("Signed and notarized runtime:", signed["installation_id"], flush=True)


def build_stage(tag, runtime_dir, archive):
    release = version(tag)
    require_darwin()
    require_no_credentials()
    dist.require(not archive.exists(), "payload output already exists")
    runtime_lock, runtime_archive = signed_runtime(runtime_dir)
    work = REPO / "build/release-build"
    work.mkdir(parents=True)
    try:
        source = work / "source"
        source_snapshot(source)
        require_go_toolchain(source)
        # Both binaries embed the signed runtime's pin, including the digest of
        # its extracted payload that the worker checks before every launch.
        lock = dict(runtime_lock, tree_sha256=dist.runtime_tree_digest(runtime_archive, cwd=source))
        lock_text = json.dumps(lock, indent=2) + "\n"
        (source / "assets/runtime.lock.json").write_text(lock_text)
        staged = work / "payload"
        assets = staged / "assets"
        (assets / "runtime/darwin-arm64").mkdir(parents=True)
        shutil.copy2(runtime_archive, assets / "runtime/darwin-arm64" / lock["filename"])
        (assets / "runtime.lock.json").write_text(lock_text)
        shutil.copy2(REPO / "assets/model.lock.json", assets / "model.lock.json")
        cli = staged / "recall"
        env = dict(os.environ, CGO_ENABLED="0", GOOS="darwin", GOARCH="arm64")
        run(["go", "build", "-trimpath", "-o", cli, "./cmd/recall"], cwd=source, env=env)
        # Prevent an inherited developer output override from escaping staging.
        env = dict(os.environ, RECALL_DESKTOP_OUTPUT=str(staged / "Recall.app"))
        run(["sh", "scripts/build-desktop.sh"], cwd=source, env=env)
        set_bundle_version(staged / "Recall.app", release)
        # The desktop app installs its own runtime on first launch, so it
        # carries the same pinned assets as the CLI (sealed by its signature).
        bundled = staged / "Recall.app/Contents/Resources/assets"
        dist.require(not bundled.exists(), "desktop build unexpectedly bundled assets")
        shutil.copytree(assets, bundled)
        run([sys.executable, "scripts/verify-desktop.py", staged / "Recall.app"], cwd=source)
        run([cli, "--help"])
        notices = staged / "notices"
        dist.notices(notices)
        desktop_notices(source, notices)
        archive.parent.mkdir(parents=True, exist_ok=True)
        write_payload(staged, archive)
    finally:
        shutil.rmtree(work)
    print("Built unsigned release payload:", archive.name, flush=True)


def sign_stage(tag, archive, runtime_dir, output):
    release = version(tag)
    require_darwin()
    verify = verifier(team_id())
    dist.require(not output.exists(), "release output already exists")
    runtime_lock, runtime_archive = signed_runtime(runtime_dir)
    with private_workdir() as work:
        staged = work / "staged"
        read_payload(archive, staged)
        check_payload(staged, release, runtime_lock, runtime_archive)
        with signing_credentials(work) as (keychain, identities):
            pkg = sign_and_notarize(work, staged, release, keychain, identities, verify)
        verify_signed(work, pkg, staged, runtime_lock, verify)
        finish_package(pkg, output, release)
    print("Verified signed and notarized installer:", output.name, flush=True)


USAGE = """usage:
  release-macos.py runtime vX.Y.Z OUTPUT-DIR
  release-macos.py build   vX.Y.Z SIGNED-RUNTIME-DIR PAYLOAD-TAR
  release-macos.py sign    vX.Y.Z PAYLOAD-TAR SIGNED-RUNTIME-DIR OUTPUT-DIR"""


def main(argv):
    stages = {"runtime": (runtime_stage, 2), "build": (build_stage, 3), "sign": (sign_stage, 4)}
    if len(argv) < 2 or argv[0] not in stages or len(argv) != 1 + stages[argv[0]][1]:
        raise ValueError(USAGE)
    stage, _ = stages[argv[0]]
    stage(argv[1], *(Path(arg).resolve() for arg in argv[2:]))


if __name__ == "__main__":
    try:
        main(sys.argv[1:])
    except Exception as error:
        # Do not emit traceback frames/arguments containing certificate passwords.
        print("Release failed:", type(error).__name__, str(error) if isinstance(error, (ValueError, RuntimeError)) else "details withheld", file=sys.stderr)
        sys.exit(1)
