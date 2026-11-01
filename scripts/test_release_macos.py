"""Credential-free release contract tests; run with Python's unittest discovery."""
import importlib.util
import io
import json
import os
from pathlib import Path
import plistlib
import platform
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import xml.etree.ElementTree as ET

REPO = Path(__file__).resolve().parent.parent


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        path = REPO / "scripts/release-macos.py"
        self.assertTrue(path.is_file(), "signed installer implementation is missing")
        spec = importlib.util.spec_from_file_location("release_macos", path)
        self.release = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.release)
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)

    def test_version_tags_reject_paths_options_and_nonversions(self):
        for bad in ["main", "1.2.3", "v01.2.3", "v1.2", "v1.2.3/evil", "v1.2.3\n", "--help", "v1.2.3-01", "v1.2.3+build"]:
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                self.release.version(bad)
        self.assertEqual(self.release.version("v1.2.3"), "1.2.3")
        self.assertEqual(self.release.version("v1.2.3-rc.1"), "1.2.3-rc.1")

    def test_cli_payload_symlink_assets_and_notices_are_self_contained(self):
        cli = self.root / "recall"
        cli.write_bytes(b"cli fixture")
        app = self.root / "Recall.app"
        (app / "Contents/MacOS").mkdir(parents=True)
        (app / "Contents/MacOS/Recall").write_bytes(b"desktop fixture")
        assets = self.root / "assets"
        assets.mkdir()
        (assets / "runtime.lock.json").write_text("runtime fixture")
        notices = self.root / "notices"
        notices.mkdir()
        (notices / "LICENSE").write_text("notice fixture")
        payload = self.root / "payload"
        self.release.make_payload(payload, cli, app, assets, notices)
        link = payload / "usr/local/bin/recall"
        self.assertTrue(link.is_symlink())
        self.assertEqual(link.readlink().as_posix(), "../lib/recall/recall")
        self.assertEqual(link.read_bytes(), b"cli fixture")
        self.assertEqual((payload / "usr/local/lib/recall/assets/runtime.lock.json").read_text(), "runtime fixture")
        self.assertEqual((payload / "usr/local/lib/recall/notices/LICENSE").read_text(), "notice fixture")
        self.assertTrue((payload / "Applications/Recall.app/Contents/MacOS/Recall").is_file())
        self.assertFalse((payload / "Users").exists())

    def test_installer_restricts_arch_os_and_does_not_relocate_app(self):
        xml = ET.fromstring(self.release.distribution_xml("1.2.3"))
        options = xml.find("options")
        self.assertEqual(options.attrib["hostArchitectures"], "arm64")
        self.assertEqual(options.attrib["rootVolumeOnly"], "true")
        self.assertEqual(xml.find("volume-check/allowed-os-versions/os-version").attrib["min"], "13.0")
        components = self.release.component_rules()
        self.assertFalse(components[0]["BundleIsRelocatable"])
        self.assertEqual(components[0]["BundleOverwriteAction"], "upgrade")
        self.assertEqual(components[0]["RootRelativeBundlePath"], "Applications/Recall.app")

    def test_runtime_pin_is_checked_before_any_signing(self):
        archive = self.root / "upstream.tar.gz"
        archive.write_bytes(b"tampered")
        lock = dict(self.release.dist.RUNTIME, sha256="0" * 64, size=8)
        with self.assertRaises(ValueError):
            self.release.prepare_runtime(archive, lock, self.root / "assets", lambda _: self.fail("signed unverified input"), lambda _: self.fail("notarized unverified input"))

    def test_signed_runtime_manifest_describes_shipped_bytes(self):
        native = self.root / "llama-b11669"
        native.mkdir()
        # Mach-O magic: signing is an external side effect; the fixture appends bytes.
        (native / "llama-server").write_bytes(b"\xcf\xfa\xed\xfeunsigned")
        (native / "llama-server").chmod(0o755)
        archive = self.root / "upstream.tar.gz"
        with tarfile.open(archive, "w:gz") as tf:
            tf.add(native, arcname="llama-b11669")
        lock = dict(self.release.dist.RUNTIME, size=archive.stat().st_size, sha256=self.release.dist.digest(archive))
        def sign(path):
            path.write_bytes(path.read_bytes() + b"-signed")
        notarized = []
        assets = self.root / "assets"
        manifest = self.release.prepare_runtime(archive, lock, assets, sign, lambda p: notarized.append(p.read_bytes()))
        shipped = assets / "runtime/darwin-arm64" / lock["filename"]
        self.assertEqual(manifest["sha256"], self.release.dist.digest(shipped))
        self.assertEqual(manifest["size"], shipped.stat().st_size)
        self.assertEqual(manifest["installation_id"], "b11669-" + manifest["sha256"])
        self.assertNotEqual(manifest["sha256"], lock["sha256"])
        self.assertEqual(json.loads((assets / "runtime.lock.json").read_text()), manifest)
        self.assertNotIn("tree_sha256", manifest, "upstream payload digest must not describe re-signed bytes")
        self.assertEqual(len(notarized), 1)
        with tarfile.open(shipped) as tf:
            self.assertEqual(tf.extractfile("llama-b11669/llama-server").read(), b"\xcf\xfa\xed\xfeunsigned-signed")

    def test_stapler_retries_transient_ticket_failures(self):
        attempts = []
        def flaky(args, **kwargs):
            attempts.append(args)
            if len(attempts) < 3:
                raise RuntimeError("stapler staple failed (exit 68)")
            return ""
        with patch.object(self.release, "run", side_effect=flaky), patch.object(self.release.time, "sleep") as sleep:
            self.release.stapler("staple", self.root / "Recall.pkg")
        self.assertEqual([a[:3] for a in attempts], [["xcrun", "stapler", "staple"]] * 3)
        self.assertEqual(sleep.call_count, 2)

    def test_stapler_failure_reports_action_target_and_diagnostic(self):
        result = subprocess.CompletedProcess([], 68, stdout="", stderr="CloudKit query for Recall.pkg failed: secret-notary\n\x1b[31m")
        with patch.dict(os.environ, {"NOTARY_PASSWORD": "secret-notary"}), patch.object(subprocess, "run", return_value=result):
            with self.assertRaises(RuntimeError) as error:
                self.release.run(["xcrun", "stapler", "staple", self.root / "Recall.pkg"])
        message = str(error.exception)
        self.assertIn("stapler staple failed (exit 68)", message)
        self.assertIn("Recall.pkg: CloudKit query", message)
        self.assertNotIn("secret-notary", message)
        self.assertNotIn("\x1b", message)
        with patch.object(subprocess, "run", return_value=subprocess.CompletedProcess([], 1, stdout="private", stderr="private")):
            with self.assertRaises(RuntimeError) as error:
                self.release.run(["xcrun", "notarytool", "store-credentials", "--password", "private"])
        self.assertNotIn("private", str(error.exception), "notarytool output must stay withheld")

    def test_failed_command_does_not_leak_credentials_in_exception(self):
        with self.assertRaises(RuntimeError) as error:
            self.release.run([sys.executable, "-c", "import sys;sys.exit(7)", "private-password"])
        self.assertNotIn("private-password", str(error.exception))
        self.assertIn("7", str(error.exception))

    @unittest.skipUnless(platform.system() == "Darwin", "native codesign diagnostic")
    def test_codesign_failure_reports_target_and_actual_diagnostic(self):
        with self.assertRaises(RuntimeError) as error:
            self.release.run(["codesign", "--verify", self.root / "missing-native-runtime"])
        self.assertIn("missing-native-runtime", str(error.exception))
        self.assertIn("No such file or directory", str(error.exception))

    def test_codesign_diagnostic_redacts_secrets_and_escapes_controls(self):
        result = subprocess.CompletedProcess([], 1, stdout="", stderr="unable to build chain: certificate-password\n\x1b[31m")
        with patch.dict(os.environ, {"APPLICATION_P12_PASSWORD": "certificate-password"}), patch.object(subprocess, "run", return_value=result):
            with self.assertRaises(RuntimeError) as error:
                self.release.run(["codesign", "--sign", "public identity", "runtime.dylib"])
        self.assertIn("unable to build chain", str(error.exception))
        self.assertNotIn("certificate-password", str(error.exception))
        self.assertNotIn("\x1b", str(error.exception))

    def test_codesign_diagnostic_redacts_password_after_environment_is_cleared(self):
        env = {"TEAM_ID": "ABCDE12345", "APPLICATION_P12_BASE64": "Zml4dHVyZQ==", "INSTALLER_P12_BASE64": "Zml4dHVyZQ==",
               "APPLICATION_P12_PASSWORD": "secret-application", "INSTALLER_P12_PASSWORD": "secret-installer", "NOTARY_APPLE_ID": "fixture@example.test", "NOTARY_PASSWORD": "secret-notary"}
        def external(args, **kwargs):
            if args[0] == "codesign":
                return subprocess.CompletedProcess(args, 1, stdout="", stderr="could not sign: secret-application secret-notary fixture@example.test ABCDE12345")
            stdout = '1) AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA "Developer ID Application: Fixture (ABCDE12345)"\n2) BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB "Developer ID Installer: Fixture (ABCDE12345)"\n' if args[1] == "find-identity" else ""
            return subprocess.CompletedProcess(args, 0, stdout=stdout, stderr="")
        with patch.dict(os.environ, env), patch.object(subprocess, "run", side_effect=external):
            with self.release.signing_credentials(self.root):
                self.assertNotIn("APPLICATION_P12_PASSWORD", os.environ)
                with self.assertRaises(RuntimeError) as error:
                    self.release.run(["codesign", "--sign", "public identity", "runtime.dylib"])
                self.assertIn("could not sign", str(error.exception))
                for value in ["secret-application", "secret-notary", "fixture@example.test", "ABCDE12345"]:
                    self.assertNotIn(value, str(error.exception))

    def test_build_subprocesses_do_not_inherit_apple_credentials(self):
        with patch.dict(os.environ, {"APPLICATION_P12_BASE64": "private-certificate", "NOTARY_PASSWORD": "private-password"}):
            output = self.release.run([sys.executable, "-c", "import os; print(os.getenv('APPLICATION_P12_BASE64', 'absent')); print(os.getenv('NOTARY_PASSWORD', 'absent'))"])
        self.assertEqual(output, "absent\nabsent\n")

    @unittest.skipUnless(platform.system() == "Darwin", "native macOS installer tools")
    def test_native_component_expands_to_expected_install_paths(self):
        cli = self.root / "recall"
        cli.write_bytes(b"fixture")
        app = self.root / "Recall.app"
        (app / "Contents/MacOS").mkdir(parents=True)
        (app / "Contents/MacOS/Recall").write_bytes(b"fixture")
        (app / "Contents/Info.plist").write_bytes(plistlib.dumps({"CFBundleIdentifier": "local.recall.desktop", "CFBundleExecutable": "Recall", "CFBundleVersion": "1.2.3", "CFBundlePackageType": "APPL"}))
        assets, notices = self.root / "assets", self.root / "notices"
        assets.mkdir()
        notices.mkdir()
        payload = self.root / "payload"
        self.release.make_payload(payload, cli, app, assets, notices)
        self.release.build_component(payload, self.root, "1.2.3")
        xml = self.root / "Distribution.xml"
        xml.write_text(self.release.distribution_xml("1.2.3"))
        pkg = self.root / "installer.pkg"
        # Local tests deliberately create an unsigned fixture; release entry point
        # has no switch that could publish this fixture as a release.
        subprocess.run(["productbuild", "--distribution", str(xml), "--package-path", str(self.root), str(pkg)], check=True, capture_output=True)
        expanded = self.root / "expanded"
        subprocess.run(["pkgutil", "--expand-full", str(pkg), str(expanded)], check=True, capture_output=True)
        installed = expanded / "Recall-component.pkg/Payload"
        self.assertEqual((installed / "usr/local/bin/recall").read_bytes(), b"fixture")
        self.assertTrue((installed / "Applications/Recall.app/Contents/Info.plist").is_file())

    def staged_payload(self, version="1.2.3"):
        staged = self.root / "staged"
        (staged / "Recall.app/Contents/MacOS").mkdir(parents=True)
        (staged / "Recall.app/Contents/MacOS/Recall").write_bytes(b"\xcf\xfa\xed\xfeapp")
        (staged / "Recall.app/Contents/MacOS/Recall").chmod(0o755)
        (staged / "Recall.app/Contents/Info.plist").write_bytes(plistlib.dumps({"CFBundleIdentifier": "local.recall.desktop", "CFBundleShortVersionString": version, "CFBundleVersion": version}))
        (staged / "recall").write_bytes(b"\xcf\xfa\xed\xfecli")
        (staged / "recall").chmod(0o755)
        archive = self.root / "signed-runtime.tar.gz"
        archive.write_bytes(b"signed runtime fixture")
        runtime_lock = {"id": "b11669", "filename": "llama.tar.gz", "sha256": self.release.dist.digest(archive), "size": archive.stat().st_size}
        assets = staged / "assets"
        (assets / "runtime/darwin-arm64").mkdir(parents=True)
        (assets / "runtime/darwin-arm64/llama.tar.gz").write_bytes(archive.read_bytes())
        (assets / "runtime.lock.json").write_text(json.dumps(dict(runtime_lock, tree_sha256="c" * 64)))
        (assets / "model.lock.json").write_bytes((REPO / "assets/model.lock.json").read_bytes())
        (staged / "notices").mkdir()
        (staged / "notices/README.txt").write_text("notices")
        return staged, runtime_lock, archive

    def test_sign_stage_runs_only_apple_tools_while_credentials_are_loaded(self):
        staged, _, _ = self.staged_payload()
        commands, notarized = [], []
        def external(args, **kwargs):
            commands.append(Path(str(args[0])).name)
            return ""
        with patch.object(self.release, "run", side_effect=external), patch.object(self.release, "notarize", side_effect=lambda p: notarized.append(p.name)):
            pkg = self.release.sign_and_notarize(self.root, staged, "1.2.3", self.root / "release.keychain-db", {"Application": "A" * 40, "Installer": "Developer ID Installer: Fixture (ABCDE12345)"}, lambda _: None)
        self.assertEqual(pkg.name, "Recall.pkg")
        self.assertTrue(commands)
        self.assertLessEqual(set(commands), self.release.APPLE_TOOLS, commands)
        self.assertEqual(notarized, ["Recall.zip", "Recall.pkg"])

    def test_app_notarization_rejection_stops_before_installer_is_built(self):
        staged, _, _ = self.staged_payload()
        commands = []
        def rejected(_):
            raise RuntimeError("Apple notarization was not Accepted")
        with patch.object(self.release, "run", side_effect=lambda args, **kw: commands.append(Path(str(args[0])).name) or ""), patch.object(self.release, "notarize", side_effect=rejected):
            with self.assertRaises(RuntimeError):
                self.release.sign_and_notarize(self.root, staged, "1.2.3", self.root / "k", {"Application": "A" * 40, "Installer": "I"}, lambda _: None)
        self.assertNotIn("productbuild", commands)
        self.assertNotIn("pkgbuild", commands)

    def test_loaded_credentials_refuse_non_apple_executables(self):
        self.release._credentials_live = True
        self.addCleanup(setattr, self.release, "_credentials_live", False)
        for tool in [sys.executable, "go", "npm", "sh", "/tmp/build/recall", "llama-server"]:
            with self.subTest(tool=tool), self.assertRaisesRegex(RuntimeError, "while signing credentials are loaded"):
                self.release.run([tool, "--version"])
        ok = subprocess.CompletedProcess([], 0, stdout="", stderr="")
        with patch.object(subprocess, "run", return_value=ok):
            for tool in ["codesign", "/usr/bin/security", "xcrun", "ditto", "pkgbuild", "productbuild"]:
                self.release.run([tool])

    def test_credentials_are_released_after_the_signing_context(self):
        env = {"TEAM_ID": "ABCDE12345", "APPLICATION_P12_BASE64": "Zml4dHVyZQ==", "APPLICATION_P12_PASSWORD": "fixture",
               "NOTARY_APPLE_ID": "fixture@example.test", "NOTARY_PASSWORD": "fixture"}
        imported = []
        def external(args, **kwargs):
            if args[1] == "import":
                imported.append(Path(args[2]).name)
            if args[1] == "find-identity":
                return '1) AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA "Developer ID Application: Fixture (ABCDE12345)"\n'
            return ""
        with patch.dict(os.environ, env), patch.object(self.release, "run", side_effect=external):
            # The runtime stage needs no installer certificate at all.
            with self.release.signing_credentials(self.root, kinds=("Application",)) as (_, identities):
                self.assertTrue(self.release._credentials_live)
                self.assertEqual(identities, {"Application": "A" * 40})
        self.assertFalse(self.release._credentials_live)
        self.assertEqual(imported, ["application.p12"])

    def test_build_stage_refuses_apple_credentials(self):
        for name in ["APPLICATION_P12_BASE64", "NOTARY_PASSWORD", "SIGNING_KEYCHAIN"]:
            with self.subTest(name=name), patch.dict(os.environ, {name: "present"}), self.assertRaisesRegex(ValueError, name):
                self.release.require_no_credentials()

    def test_release_modules_pin_the_same_patched_go_toolchain(self):
        root = self.release.go_toolchain((REPO / "go.mod").read_text())
        self.assertEqual(root, self.release.go_toolchain((REPO / "desktop/go.mod").read_text()))
        self.assertRegex(root, r"^go1\.\d+\.\d+$")
        with self.assertRaises(ValueError):
            self.release.go_toolchain("module fixture\n\ngo 1.27.0\n")

    def test_payload_handoff_round_trips_and_rejects_unexpected_entries(self):
        staged, runtime_lock, archive = self.staged_payload()
        tar = self.root / "payload.tar"
        self.release.write_payload(staged, tar)
        received = self.root / "received"
        self.release.read_payload(tar, received)
        self.assertTrue(os.access(received / "recall", os.X_OK))
        self.release.check_payload(received, "1.2.3", runtime_lock, archive)
        with self.assertRaisesRegex(ValueError, "version"):
            self.release.check_payload(received, "1.2.4", runtime_lock, archive)
        (received / "assets/runtime/darwin-arm64/llama.tar.gz").write_bytes(b"swapped runtime fixture")
        with self.assertRaisesRegex(ValueError, "runtime"):
            self.release.check_payload(received, "1.2.3", runtime_lock, archive)
        for name, mutate in {
            "symlink": lambda tf: tf.addfile(self.link_info("notices/link", "../../etc/passwd")),
            "unexpected": lambda tf: tf.addfile(self.file_info("extra-tool", b"x"), io.BytesIO(b"x")),
            "writable": lambda tf: tf.addfile(self.file_info("notices/w", b"x", 0o666), io.BytesIO(b"x")),
        }.items():
            with self.subTest(name=name):
                bad = self.root / f"{name}.tar"
                shutil.copy(tar, bad)
                with tarfile.open(bad, "a") as tf:
                    mutate(tf)
                with self.assertRaises(ValueError):
                    self.release.read_payload(bad, self.root / f"{name}-out")

    @staticmethod
    def file_info(name, data, mode=0o644):
        info = tarfile.TarInfo(name)
        info.size, info.mode = len(data), mode
        return info

    @staticmethod
    def link_info(name, target):
        info = tarfile.TarInfo(name)
        info.type, info.linkname = tarfile.SYMTYPE, target
        return info

    def test_signed_runtime_artifact_must_match_upstream_pin(self):
        native = self.root / "llama-b11669"
        native.mkdir()
        (native / "llama-server").write_bytes(b"\xcf\xfa\xed\xfesigned")
        (native / "llama-server").chmod(0o755)
        runtime_dir = self.root / "runtime"
        archive = runtime_dir / "runtime/darwin-arm64" / self.release.dist.RUNTIME["filename"]
        archive.parent.mkdir(parents=True)
        with tarfile.open(archive, "w:gz") as tf:
            tf.add(native, arcname="llama-b11669")
        lock = {k: v for k, v in self.release.dist.RUNTIME.items() if k != "tree_sha256"}
        lock.update(sha256=self.release.dist.digest(archive), size=archive.stat().st_size)
        lock["installation_id"] = lock["id"] + "-" + lock["sha256"]
        (runtime_dir / "runtime.lock.json").write_text(json.dumps(lock))
        self.assertEqual(self.release.signed_runtime(runtime_dir), (lock, archive))
        for change in [{"url": "https://evil.example/runtime.tar.gz"}, {"installation_id": "b11669-other"}, {"extra": True}]:
            with self.subTest(change=change):
                (runtime_dir / "runtime.lock.json").write_text(json.dumps(dict(lock, **change)))
                with self.assertRaises(ValueError):
                    self.release.signed_runtime(runtime_dir)

    def test_stapling_failure_prevents_output_publication(self):
        pkg = self.root / "candidate.pkg"
        pkg.write_bytes(b"candidate")
        output = self.root / "release"
        with patch.object(self.release, "run", side_effect=RuntimeError("stapling failed")), patch.object(self.release.time, "sleep") as sleep:
            with self.assertRaises(RuntimeError):
                self.release.finish_package(pkg, output, "1.2.3")
        self.assertEqual(sleep.call_count, 4, "persistent stapler failure must be retried a bounded number of times")
        self.assertFalse(output.exists())

    def test_verified_package_gets_checksum_only_after_all_gates(self):
        pkg = self.root / "candidate.pkg"
        pkg.write_bytes(b"verified fixture")
        output = self.root / "release"
        checked = []
        def gate(args):
            self.assertFalse(output.exists(), "artifact appeared before verification finished")
            checked.append(args[0])
            return ""
        with patch.object(self.release, "run", side_effect=gate):
            self.release.finish_package(pkg, output, "1.2.3")
        target = output / "Recall-1.2.3-darwin-arm64.pkg"
        self.assertEqual(target.read_bytes(), b"verified fixture")
        self.assertEqual(checked, ["xcrun", "pkgutil", "spctl"])
        self.assertEqual(target.with_suffix(".pkg.sha256").read_text(), self.release.dist.digest(target) + "  Recall-1.2.3-darwin-arm64.pkg\n")

    def test_signing_credentials_cleanup_on_build_failure(self):
        env = {"TEAM_ID": "ABCDE12345", "APPLICATION_P12_BASE64": "Zml4dHVyZQ==", "INSTALLER_P12_BASE64": "Zml4dHVyZQ==",
               "APPLICATION_P12_PASSWORD": "fixture", "INSTALLER_P12_PASSWORD": "fixture", "NOTARY_APPLE_ID": "fixture@example.test", "NOTARY_PASSWORD": "fixture"}
        commands = []
        def external(args, **kwargs):
            commands.append(args)
            if args[1] == "find-identity":
                return '1) AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA "Developer ID Application: Fixture (ABCDE12345)"\n2) BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB "Developer ID Installer: Fixture (ABCDE12345)"\n'
            return ""
        with patch.dict(os.environ, env), patch.object(self.release, "run", side_effect=external):
            with self.assertRaisesRegex(RuntimeError, "build fixture failed"):
                with self.release.signing_credentials(self.root):
                    self.assertNotIn("NOTARY_PASSWORD", os.environ)
                    self.assertFalse(list(self.root.glob("*.p12")))
                    raise RuntimeError("build fixture failed")
            self.assertNotIn("SIGNING_KEYCHAIN", os.environ)
        self.assertEqual(commands[-1][:2], ["security", "delete-keychain"])

    def test_invalid_certificate_still_deletes_temporary_keychain(self):
        commands = []
        with patch.dict(os.environ, {"TEAM_ID": "ABCDE12345", "APPLICATION_P12_BASE64": "not-base64!"}), patch.object(self.release, "run", side_effect=lambda args: commands.append(args) or ""):
            with self.assertRaises(ValueError):
                with self.release.signing_credentials(self.root):
                    self.fail("invalid certificate accepted")
        self.assertEqual(commands[-1][:2], ["security", "delete-keychain"])

    def test_signing_lookup_registers_keychain_and_restores_existing_search_list(self):
        env = {"TEAM_ID": "ABCDE12345", "APPLICATION_P12_BASE64": "Zml4dHVyZQ==", "INSTALLER_P12_BASE64": "Zml4dHVyZQ==",
               "APPLICATION_P12_PASSWORD": "fixture", "INSTALLER_P12_PASSWORD": "fixture", "NOTARY_APPLE_ID": "fixture@example.test", "NOTARY_PASSWORD": "fixture"}
        original = ["/Users/runner/Library/Keychains/login.keychain-db", "/path with spaces/other.keychain-db"]
        for fail_signing in [False, True]:
            with self.subTest(fail_signing=fail_signing):
                search_list = original.copy()
                deleted = []
                def external(args, **kwargs):
                    if args[:4] == ["security", "list-keychains", "-d", "user"]:
                        if len(args) == 4:
                            return "\n".join(json.dumps(path) for path in search_list)
                        self.assertEqual(args[4], "-s")
                        search_list[:] = [str(path) for path in args[5:]]
                    elif args[0] == "codesign":
                        self.assertIn(str(self.root / "release.keychain-db"), search_list)
                        self.assertEqual(args[args.index("--sign") + 1], "A" * 40)
                        if fail_signing:
                            raise RuntimeError("fixture signing failure")
                    elif args[1] == "find-identity":
                        return '1) AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA "Developer ID Application: Fixture (ABCDE12345)"\n2) BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB "Developer ID Installer: Fixture (ABCDE12345)"\n'
                    elif args[1] == "delete-keychain":
                        deleted.append(str(args[-1]))
                    return ""
                with patch.dict(os.environ, env), patch.object(self.release, "run", side_effect=external):
                    try:
                        with self.release.signing_credentials(self.root) as (keychain, identities):
                            self.assertEqual(search_list, [str(keychain), *original])
                            self.assertEqual(identities["Installer"], "Developer ID Installer: Fixture (ABCDE12345)")
                            self.release.run(["codesign", "--keychain", keychain, "--sign", identities["Application"], "fixture"])
                    except RuntimeError as error:
                        self.assertTrue(fail_signing)
                        self.assertEqual(str(error), "fixture signing failure")
                self.assertEqual(search_list, original)
                self.assertEqual(deleted, [str(self.root / "release.keychain-db")])

    def test_notary_status_must_be_accepted_even_with_exit_zero(self):
        with patch.dict(os.environ, {"SIGNING_KEYCHAIN": "fixture"}), patch.object(self.release, "run", return_value='{"status":"Invalid","id":"fixture"}'):
            with self.assertRaises(RuntimeError):
                self.release.notarize(self.root / "input.pkg")

    def workflow(self):
        path = REPO / ".github/workflows/release.yml"
        self.assertTrue(path.is_file(), "release workflow is missing")
        return json.loads(subprocess.check_output(["ruby", "-ryaml", "-rjson", "-e", "puts JSON.generate(YAML.load_file(ARGV[0]))", str(path)], text=True))

    def test_workflow_version_validation_can_import_packager(self):
        jobs = self.workflow()["jobs"]
        for name in ["runtime", "build", "sign"]:
            with self.subTest(job=name):
                step = next(s for s in jobs[name]["steps"] if s.get("name") == "Validate version tag")
                result = subprocess.run(step["run"], shell=True, cwd=REPO, env=dict(os.environ, RELEASE_TAG="v1.2.3"), capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_clean_checkout_builds_frontend_before_desktop_go_tests(self):
        workflow = REPO / ".github/workflows/release.yml"
        data = json.loads(subprocess.check_output(["ruby", "-ryaml", "-rjson", "-e", "puts JSON.generate(YAML.load_file(ARGV[0]))", str(workflow)], text=True))
        step = next(s for s in data["jobs"]["build"]["steps"] if s.get("name") == "Credential-free verification")
        checkout = self.root / "checkout"
        desktop = checkout / "desktop"
        desktop.mkdir(parents=True)
        (checkout / "go.mod").write_text("module fixture\n\ngo 1.27.0\n")
        (checkout / "fixture.go").write_text("package fixture\n")
        (desktop / "go.mod").write_text("module fixture/desktop\n\ngo 1.27.0\n")
        (desktop / "assets.go").write_bytes((REPO / "desktop/assets.go").read_bytes())
        tools = self.root / "tools"
        tools.mkdir()
        # Double only the expensive Wails/frontend build and unrelated Python
        # checks. The real Go compiler enforces the actual go:embed dependency.
        build = tools / "sh"
        build.write_text(f"#!{sys.executable}\nimport pathlib,sys\nassert sys.argv[1:] == ['scripts/build-desktop.sh']\np=pathlib.Path('desktop/frontend/dist');p.mkdir(parents=True)\n(p/'index.html').write_text('built frontend')\n")
        build.chmod(0o755)
        python = tools / "python3"
        python.write_text("#!/bin/sh\nexit 0\n")
        python.chmod(0o755)
        env = dict(os.environ, PATH=str(tools) + os.pathsep + os.environ["PATH"], GOWORK="off")
        result = subprocess.run(["/bin/bash", "-e", "-c", step["run"]], cwd=checkout, env=env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_workflow_only_exposes_apple_secrets_on_release_tags(self):
        workflow = self.workflow()
        self.assertEqual(workflow["on"], {"push": {"tags": ["v*"]}})
        self.assertEqual(workflow["permissions"], {"contents": "read"})
        jobs = workflow["jobs"]
        self.assertEqual(set(jobs), {"runtime", "build", "sign", "publish"})
        self.assertIn("refs/tags/v", jobs["runtime"]["if"])
        self.assertEqual(jobs["build"]["needs"], "runtime")
        self.assertEqual(jobs["sign"]["needs"], ["runtime", "build"])
        self.assertEqual(jobs["publish"]["needs"], "sign")
        application = {"${{ secrets.APPLE_APPLICATION_CERTIFICATE_P12_BASE64 }}", "${{ secrets.APPLE_APPLICATION_CERTIFICATE_PASSWORD }}",
                       "${{ secrets.APPLE_ID }}", "${{ secrets.APPLE_TEAM_ID }}", "${{ secrets.APPLE_APP_SPECIFIC_PASSWORD }}"}
        installer = {"${{ secrets.APPLE_INSTALLER_CERTIFICATE_P12_BASE64 }}", "${{ secrets.APPLE_INSTALLER_CERTIFICATE_PASSWORD }}"}
        for name, expected in {"runtime": application, "sign": application | installer}.items():
            with self.subTest(job=name):
                job = jobs[name]
                self.assertEqual(job["permissions"], {"contents": "read"})
                self.assertEqual(job["environment"], "release")
                secret_steps = [step for step in job["steps"] if "secrets.APPLE_" in json.dumps(step)]
                self.assertEqual(len(secret_steps), 1)
                self.assertEqual(set(secret_steps[0]["env"].values()), expected)
                # Credential jobs install no toolchain and run no third-party build.
                uses = [step.get("uses", "") for step in job["steps"]]
                self.assertFalse([u for u in uses if "setup-go" in u or "setup-node" in u], uses)
                commands = " ".join(step.get("run", "") for step in job["steps"])
                for forbidden in ["go ", "npm", "build-desktop", "wails", "unittest"]:
                    self.assertNotIn(forbidden, commands)
        for name in ["build", "publish"]:
            with self.subTest(job=name):
                self.assertNotIn("secrets.APPLE_", json.dumps(jobs[name]))
                self.assertNotIn("environment", jobs[name])
        self.assertEqual(jobs["build"]["permissions"], {"contents": "read"})
        self.assertEqual(jobs["publish"]["permissions"], {"contents": "write"})
        for other in (REPO / ".github/workflows").glob("*.y*ml"):
            if other.name != "release.yml":
                self.assertNotIn("secrets.APPLE_", other.read_text())

if __name__ == "__main__":
    unittest.main()
