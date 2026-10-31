# Signed macOS Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a signed, notarized Apple Silicon macOS installer containing Recall's desktop app and CLI, with an R icon and installation instructions.

**Architecture:** A version-tag-only GitHub Actions workflow builds and verifies unsigned inputs before importing Apple credentials into a temporary keychain. A dedicated packaging script signs the native runtime, compiles both entry points against its updated manifest in a disposable source staging directory, signs the application and CLI, builds a signed installer, notarizes and staples it, and verifies it before publication. Existing development packaging remains credential-free.

**Tech Stack:** Go 1.27, Wails 2.17, Node 22, Python standard library, macOS codesign/pkgbuild/productbuild/notarytool/stapler, GitHub Actions.

**Spec:** User-approved scope in this conversation: replace W icon with R; signed/notarized .pkg installs desktop and CLI; v* tag publication; Apple organization secrets referenced exclusively by release workflow; no unsigned fallback; README installation guide.

**Completion:** Implemented and locally verified. Apple credential access, notarization,
GitHub publication and real system installation remain unexercised until an authorized
release run. User additionally requested `~/Documents/recall` as the default, without
automatic migration; implemented with preservation regressions. Final review's runtime
upgrade finding is addressed by checksum-qualified side-by-side installation directories,
while retaining the logical runtime ID and preserving existing configuration/runtime files.

## Global Constraints

- Apple Silicon only; macOS 13 deployment target, matching existing support.
- All seven previously specified APPLE_* secret references appear exclusively in `.github/workflows/release.yml`; trigger only `push.tags: ['v*']`, with a defensive job guard for version tags. No branch, pull-request, manual, or reusable workflow triggers.
- Never expose secret values, commit private material, disable Gatekeeper, or silently publish unsigned outputs.
- Install `/Applications/Recall.app`; expose `/usr/local/bin/recall`; preserve user collections and never initialize them as root.
- Bundle pinned runtime and notices, not model weights; model download remains an explicit user `recall init` action.
- Sign nested Mach-O runtime executables/libraries explicitly, before signing their containing products. Do not use `codesign --deep` to sign.
- Runtime signing changes archive bytes: retain upstream checksum validation, then produce a release-local manifest and compile both binaries against it in staging. Never change tracked upstream pins to describe signed bytes.
- Temporary keychain and credential files must be cleaned up even on failure. Signing/notarization failures prevent release publication.

## Review Focus

- Secret isolation: branch pushes and pull requests cannot reach credential-bearing steps.
- Signature failures or notarization rejection: no installer/checksum is published.
- Runtime re-signing: compiled runtime hash and shipped archive agree; extracted runtime signatures verify.
- Installation/upgrade: CLI resolves packaged assets without environment variables, including executable symlink resolution; existing collections stay untouched.
- Version and architecture: invalid tags fail safely; installer rejects unsupported machines; version metadata matches the release.

### Task 1: Recall icon and installable CLI asset discovery

**Files:** Add `scripts/generate-icon.swift` as the reproducible vector source (instead of SVG plus an external renderer); replace `desktop/build/appicon.png`; modify `internal/cli/init.go` and its tests.

**Interfaces:** `/usr/local/lib/recall/recall` is the real signed CLI with adjacent `assets/`; `/usr/local/bin/recall` is a symlink to it. Default CLI asset discovery resolves executable symlinks. Explicit `RECALL_ASSETS_DIR` keeps existing precedence and validation.

- [x] Add a failing regression proving a symlinked CLI finds assets beside the real executable; preserve direct executable and environment-override tests.
- [x] Run focused CLI tests and observe the expected failure.
- [x] Resolve executable symlinks for default assets; generate a simple high-contrast R icon from a checked-in vector source using macOS tools, without adding runtime dependencies.
- [x] Run CLI tests, regenerate icon, build desktop and verify the bundled icon is not the Wails default.
- [x] Commit this independently testable change.

### Task 2: Signed installer packaging and tag-only release workflow

**Files:** Add `.github/workflows/release.yml`, `scripts/release-macos.py`, and `scripts/test_release_macos.py`; reuse `scripts/distribution.py` validation/notices and `scripts/build-desktop.sh`; update the build script only if required for version propagation.

**Interfaces:** `python3 scripts/release-macos.py vVERSION` runs on Darwin arm64 with certificate/notarization credentials supplied only by the release job's signing step. The script owns their temporary keychain import/cleanup. Writes `build/release/Recall-VERSION-darwin-arm64.pkg` and its `.sha256` only after all signature, notarization, staple, and package checks pass. Non-secret identity names are discovered from imported certificates and checked against the requested team.

- [x] Add failing tests for strict version parsing; exact release-only workflow triggers/secret references; payload paths and symlink; upstream pin verification and signed-runtime manifest consistency; fail-closed subprocess execution and artifact publication ordering.
- [x] Run `python3 -m unittest discover -s scripts -p 'test_release_macos.py'` and observe failure before implementation.
- [x] Implement staging, runtime signing/repacking, release-local manifest embedding, CLI/desktop build/version metadata, explicit signing, notices and installer layout. Produce a distribution installer restricted to arm64/macOS 13+, with stable component identifiers for upgrades and no user-data manipulation scripts.
- [x] Implement workflow with pinned action revisions, narrow job permissions, read-only build/test work before credential import, exact seven organization-secret references scoped to signing steps, temporary keychain, and unconditional cleanup. Keep publication permission only where needed; release uploads run only after packaging verification succeeds.
- [x] Implement credentialed release gates: submit signed installer with `notarytool --wait`, fail on non-Accepted results, staple/validate ticket, verify Developer ID signatures and package payload, and run `spctl` assessment. Generate checksum last. Upload only the verified package/checksum to the matching GitHub release; use prerelease status for prerelease versions.
- [x] Run unit tests and YAML/workflow lint; perform unsigned local payload/build tests without fetching or using organization secrets. Implement verification that the embedded runtime manifest matches shipped signed runtime bytes and all extracted runtime Mach-O signatures verify in the credentialed workflow.
- [x] Commit this independently testable change. Do not create/push a tag or publish a release without authorization.

### Task 3: Installation and maintainer documentation; final review

**Files:** Modify `README.md`, `docs/operations.md`, and `docs/desktop.md` as needed; add `docs/releases.md` for maintainers.

**Interfaces:** README links to repository releases and gives download/checksum/Installer/`recall init`/desktop launch steps. Maintainer guide documents the seven agreed secret names, org repository access, version tags, release security boundary, and certificate renewal without showing real credentials.

- [x] Document macOS 13+/Apple Silicon, administrator authorization, both installation locations, symlink target/assets, initial model download and no-sudo initialization. Distinguish signed release installers from ad-hoc development builds and legacy archives.
- [x] Document verification commands and limitations: local checks cannot prove Apple notarization or GitHub secret access; the first authorized release run must establish those gates.
- [x] Run root and desktop Go suites, CLI race tests, frontend tests/build, desktop artifact verifier, release tests, workflow lint and `git diff --check`.
- [x] Request a read-only whole-change security/correctness review; resolve blockers and rerun affected checks.
- [x] Commit the scoped changes; report evidence, limitations and unpublished status. Do not merge or push automatically.
