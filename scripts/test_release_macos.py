"""Credential-free release contract tests; run with Python's unittest discovery."""
import importlib.util
import json
import os
from pathlib import Path
import plistlib
import platform
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
        self.assertEqual(len(notarized), 1)
        with tarfile.open(shipped) as tf:
            self.assertEqual(tf.extractfile("llama-b11669/llama-server").read(), b"\xcf\xfa\xed\xfeunsigned-signed")

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

    def test_notarization_rejection_prevents_output_publication(self):
        pkg = self.root / "candidate.pkg"
        pkg.write_bytes(b"candidate")
        output = self.root / "release"
        with self.assertRaises(RuntimeError):
            self.release.finish_package(pkg, output, "1.2.3", lambda _: (_ for _ in ()).throw(RuntimeError("Rejected")))
        self.assertFalse(output.exists())

    def test_stapling_failure_prevents_output_publication(self):
        pkg = self.root / "candidate.pkg"
        pkg.write_bytes(b"candidate")
        output = self.root / "release"
        with patch.object(self.release, "run", side_effect=RuntimeError("stapling failed")):
            with self.assertRaises(RuntimeError):
                self.release.finish_package(pkg, output, "1.2.3", lambda _: None)
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
            self.release.finish_package(pkg, output, "1.2.3", lambda _: self.assertFalse(output.exists()))
        target = output / "Recall-1.2.3-darwin-arm64.pkg"
        self.assertEqual(target.read_bytes(), b"verified fixture")
        self.assertEqual(checked, ["xcrun", "xcrun", "pkgutil", "spctl"])
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

    def test_workflow_version_validation_can_import_packager(self):
        workflow = REPO / ".github/workflows/release.yml"
        self.assertTrue(workflow.is_file())
        data = json.loads(subprocess.check_output(["ruby", "-ryaml", "-rjson", "-e", "puts JSON.generate(YAML.load_file(ARGV[0]))", str(workflow)], text=True))
        step = next(s for s in data["jobs"]["installer"]["steps"] if s.get("name") == "Validate version tag")
        result = subprocess.run(step["run"], shell=True, cwd=REPO, env=dict(os.environ, RELEASE_TAG="v1.2.3"), capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_clean_checkout_builds_frontend_before_desktop_go_tests(self):
        workflow = REPO / ".github/workflows/release.yml"
        data = json.loads(subprocess.check_output(["ruby", "-ryaml", "-rjson", "-e", "puts JSON.generate(YAML.load_file(ARGV[0]))", str(workflow)], text=True))
        step = next(s for s in data["jobs"]["installer"]["steps"] if s.get("name") == "Credential-free verification")
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
        path = REPO / ".github/workflows/release.yml"
        self.assertTrue(path.is_file(), "release workflow is missing")
        workflow = json.loads(subprocess.check_output(["ruby", "-ryaml", "-rjson", "-e", "puts JSON.generate(YAML.load_file(ARGV[0]))", str(path)], text=True))
        self.assertEqual(workflow["on"], {"push": {"tags": ["v*"]}})
        self.assertEqual(workflow["permissions"], {"contents": "read"})
        build = workflow["jobs"]["installer"]
        self.assertIn("refs/tags/v", build["if"])
        self.assertEqual(build["permissions"], {"contents": "read"})
        secret_steps = [step for step in build["steps"] if "secrets.APPLE_" in json.dumps(step)]
        self.assertEqual(len(secret_steps), 1)
        self.assertEqual(set(secret_steps[0]["env"].values()), {
            "${{ secrets.APPLE_APPLICATION_CERTIFICATE_P12_BASE64 }}", "${{ secrets.APPLE_INSTALLER_CERTIFICATE_P12_BASE64 }}",
            "${{ secrets.APPLE_APPLICATION_CERTIFICATE_PASSWORD }}", "${{ secrets.APPLE_INSTALLER_CERTIFICATE_PASSWORD }}",
            "${{ secrets.APPLE_ID }}", "${{ secrets.APPLE_TEAM_ID }}", "${{ secrets.APPLE_APP_SPECIFIC_PASSWORD }}",
        })
        publish = workflow["jobs"]["publish"]
        self.assertEqual(publish["needs"], "installer")
        self.assertEqual(publish["permissions"], {"contents": "write"})
        self.assertNotIn("secrets.APPLE_", json.dumps(publish))
        for other in (REPO / ".github/workflows").glob("*.y*ml"):
            if other != path:
                self.assertNotIn("secrets.APPLE_", other.read_text())


if __name__ == "__main__":
    unittest.main()
