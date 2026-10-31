"""Release-only Developer ID packaging. No unsigned publication path.

Apple credentials are supplied only to the version-tag workflow's signing step.
All generated manifests, credentials and signing work live in temporary storage.
"""
import base64
from contextlib import contextmanager
import json
import os
from pathlib import Path
import platform
import plistlib
import re
import secrets
import shutil
import subprocess
import sys
import tarfile
import tempfile
from xml.sax.saxutils import escape

import distribution as dist

REPO = Path(__file__).resolve().parent.parent
PRIVATE_ENV = {"APPLICATION_P12_BASE64", "INSTALLER_P12_BASE64", "APPLICATION_P12_PASSWORD",
               "INSTALLER_P12_PASSWORD", "NOTARY_APPLE_ID", "NOTARY_PASSWORD"}


def run(args, **kwargs):
    # Never let CalledProcessError stringify password-bearing argument lists.
    env = kwargs.pop("env", os.environ)
    env = {k: v for k, v in env.items() if k not in PRIVATE_ENV}
    result = subprocess.run([str(a) for a in args], stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, text=True, env=env, **kwargs)
    if result.returncode:
        raise RuntimeError(f"{Path(args[0]).name} failed (exit {result.returncode}); command arguments/output withheld")
    return result.stdout


def version(tag):
    number = r"(?:0|[1-9][0-9]*)"
    identifier = r"(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)"
    if not re.fullmatch(rf"v{number}\.{number}\.{number}(?:-{identifier}(?:\.{identifier})*)?", tag):
        raise ValueError("release tag must be vMAJOR.MINOR.PATCH, optionally with a SemVer prerelease suffix")
    return tag[1:]


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
    signed = dict(lock, sha256=dist.digest(target), size=target.stat().st_size)
    signed["installation_id"] = signed["id"] + "-" + signed["sha256"]
    (assets / "runtime.lock.json").write_text(json.dumps(signed, indent=2) + "\n")
    dist.pinned(target, signed)
    return signed


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


def finish_package(pkg, output, release, submit):
    dist.require(not output.exists(), "release output already exists; refusing stale artifacts")
    submit(pkg)
    run(["xcrun", "stapler", "staple", pkg])
    run(["xcrun", "stapler", "validate", pkg])
    run(["pkgutil", "--check-signature", pkg])
    run(["spctl", "--assess", "--type", "install", "--verbose=2", pkg])
    output.mkdir(parents=True)
    target = output / f"Recall-{release}-darwin-arm64.pkg"
    shutil.copy2(pkg, target)
    target.with_suffix(".pkg.sha256").write_text(dist.digest(target) + "  " + target.name + "\n")


@contextmanager
def signing_credentials(work):
    team = os.environ.get("TEAM_ID", "")
    dist.require(re.fullmatch(r"[A-Z0-9]{10}", team), "missing/invalid Apple Team ID")
    keychain = work / "release.keychain-db"
    password = secrets.token_urlsafe(32)
    created = False
    try:
        run(["security", "create-keychain", "-p", password, keychain])
        created = True
        run(["security", "set-keychain-settings", "-lut", "21600", keychain])
        run(["security", "unlock-keychain", "-p", password, keychain])
        for kind in ("APPLICATION", "INSTALLER"):
            certificate = work / (kind.lower() + ".p12")
            encoded = "".join(os.environ[kind + "_P12_BASE64"].split())
            with certificate.open("xb") as dest:
                os.chmod(certificate, 0o600)
                dest.write(base64.b64decode(encoded, validate=True))
            run(["security", "import", certificate, "-k", keychain, "-P", os.environ[kind + "_P12_PASSWORD"],
                 "-T", "/usr/bin/codesign", "-T", "/usr/bin/productbuild"])
            certificate.unlink()
        run(["security", "set-key-partition-list", "-S", "apple-tool:,apple:,codesign:", "-s", "-k", password, keychain])
        identities = run(["security", "find-identity", "-v", keychain])
        selected = {}
        for kind in ("Application", "Installer"):
            matches = re.findall(r'"(Developer ID ' + kind + r': [^"\n]+ \(' + team + r'\))"', identities)
            dist.require(len(matches) == 1, "expected exactly one valid Developer ID " + kind + " identity for requested team")
            selected[kind] = matches[0]
        run(["xcrun", "notarytool", "store-credentials", "recall-release", "--keychain", keychain,
             "--apple-id", os.environ["NOTARY_APPLE_ID"], "--team-id", team,
             "--password", os.environ["NOTARY_PASSWORD"]])
        # Build/dependency tools must not inherit certificates or Apple passwords.
        for name in PRIVATE_ENV:
            os.environ.pop(name, None)
        os.environ["SIGNING_KEYCHAIN"] = str(keychain)
        yield keychain, selected
    finally:
        os.environ.pop("SIGNING_KEYCHAIN", None)
        if created:
            run(["security", "delete-keychain", keychain])


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


def build_release(tag):
    release = version(tag)
    dist.require(platform.system() == "Darwin" and platform.machine() == "arm64", "release requires Darwin arm64")
    output = REPO / "build/release"
    dist.require(not output.exists(), "release output already exists")
    REPO.joinpath("build").mkdir(exist_ok=True)
    # Credential temporary directory is also covered by the workflow's always cleanup.
    work = REPO / "build/release-private"
    work.mkdir(mode=0o700)
    try:
        with signing_credentials(work) as (keychain, identities):
            source = work / "source"
            source_snapshot(source)
            requirement = '=anchor apple generic and certificate leaf[subject.OU] = "' + os.environ["TEAM_ID"] + '" and certificate leaf[field.1.2.840.113635.100.6.1.13] exists'
            def verify(path):
                run(["codesign", "--verify", "--strict", "-R", requirement, path])
            def sign(path):
                run(["codesign", "--force", "--options", "runtime", "--timestamp", "--keychain", keychain,
                     "--sign", identities["Application"], path])
                verify(path)
            archive = dist.fetch(dist.RUNTIME["url"], dist.CACHE / dist.RUNTIME["filename"])
            assets = work / "assets"
            signed = prepare_runtime(archive, dist.RUNTIME, assets, sign, notarize)
            shutil.copy2(REPO / "assets/model.lock.json", assets / "model.lock.json")
            # Both binaries embed this signed archive's pin, not the upstream pin.
            shutil.copy2(assets / "runtime.lock.json", source / "assets/runtime.lock.json")
            cli = work / "recall"
            env = dict(os.environ, CGO_ENABLED="0", GOOS="darwin", GOARCH="arm64")
            run(["go", "build", "-trimpath", "-o", cli, "./cmd/recall"], cwd=source, env=env)
            # Prevent an inherited developer output override from escaping staging.
            env = dict(os.environ, RECALL_DESKTOP_OUTPUT=str(source / "build/Recall.app"))
            run(["sh", "scripts/build-desktop.sh"], cwd=source, env=env)
            app = source / "build/Recall.app"
            plist = app / "Contents/Info.plist"
            info = plistlib.loads(plist.read_bytes())
            info["CFBundleVersion"] = release.split("-")[0]
            info["CFBundleShortVersionString"] = release.split("-")[0]
            plist.write_bytes(plistlib.dumps(info))
            sign(cli)
            sign(app / "Contents/MacOS/Recall")
            sign(app)
            app_zip = work / "Recall.zip"
            run(["ditto", "-c", "-k", "--keepParent", app, app_zip])
            notarize(app_zip)
            run(["xcrun", "stapler", "staple", app])
            run(["xcrun", "stapler", "validate", app])
            verify(app)
            run(["spctl", "--assess", "--type", "execute", "--verbose=2", app])
            run([sys.executable, "scripts/verify-desktop.py", app], cwd=source)
            # Check extracted runtime bytes, not merely the archive signature.
            extracted = work / "verify-runtime"
            with tarfile.open(assets / "runtime/darwin-arm64" / signed["filename"]) as tf:
                dist.extract(tf, extracted, "llama-b11669", True)
            for path in extracted.rglob("*"):
                if macho(path):
                    verify(path)
            run([extracted / "llama-b11669/llama-server", "--version"])
            run([cli, "--help"])
            notices = work / "notices"
            dist.notices(notices)
            desktop_notices(source, notices)
            payload = work / "payload"
            make_payload(payload, cli, app, assets, notices)
            build_component(payload, work, release)
            xml = work / "Distribution.xml"
            xml.write_text(distribution_xml(release))
            pkg = work / "Recall.pkg"
            run(["productbuild", "--distribution", xml, "--package-path", work, "--keychain", keychain,
                 "--sign", identities["Installer"], pkg])
            # Expand the actual installer before notarization/publication.
            expanded = work / "expanded"
            run(["pkgutil", "--expand-full", pkg, expanded])
            installed = expanded / "Recall-component.pkg/Payload"
            dist.require((installed / "usr/local/bin/recall").readlink().as_posix() == "../lib/recall/recall", "installer CLI link mismatch")
            dist.require((installed / "usr/local/lib/recall/assets/runtime.lock.json").read_bytes() == (assets / "runtime.lock.json").read_bytes(), "installer runtime pin mismatch")
            verify(installed / "usr/local/lib/recall/recall")
            verify(installed / "Applications/Recall.app")
            finish_package(pkg, output, release, notarize)
            print("Verified signed and notarized installer:", output.name, flush=True)
    finally:
        # This path was exclusively created by this invocation, never a broad delete.
        shutil.rmtree(work)


if __name__ == "__main__":
    try:
        if len(sys.argv) != 2:
            raise ValueError("usage: python3 scripts/release-macos.py vMAJOR.MINOR.PATCH")
        build_release(sys.argv[1])
    except Exception as error:
        # Do not emit traceback frames/arguments containing certificate passwords.
        print("Release failed:", type(error).__name__, str(error) if isinstance(error, (ValueError, RuntimeError)) else "details withheld", file=sys.stderr)
        sys.exit(1)
