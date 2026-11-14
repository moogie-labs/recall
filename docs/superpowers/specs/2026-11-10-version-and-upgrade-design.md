# Recall: version reporting and upgrades

Date: 2026-11-10  
Status: Approved; implemented on branch feat/version-and-upgrade.

## 1. Purpose

Make upgrading Recall easy for people who installed it from the signed GitHub
release installer, without weakening the release's signing boundary or the
local-first promise more than necessary.

## 2. Agreed requirements

- The desktop app shows the running Recall version.
- `recall version` prints the running version.
- `recall upgrade` upgrades to the newest release.
- The desktop app checks for an upgrade when it launches. When one exists it
  shows a pop-up asking whether to upgrade. If the user declines, an
  **Upgrade available** control appears next to the current version; clicking it
  starts the upgrade.
- Upgrades come from the GitHub Releases page of `moogie-labs/recall`.
- Installation is handed to the macOS Installer app (Installer.app) after
  Recall has downloaded and verified the package. Recall never runs a
  privileged install itself.
- The launch check is on by default and can be turned off. A new ⚙ **Settings**
  button in the desktop app opens a settings view containing that toggle and
  the current version.
- Declining the pop-up does not suppress it: it appears on every launch while
  an upgrade is available and the launch check is on.
- Only stable releases are offered; prereleases are never offered.

## 3. Scope and non-goals

In scope: version embedding in release builds, a shared update module, the two
CLI commands, the desktop launch check, pop-up, version footer, settings view,
release-pipeline and documentation changes.

Not in scope:

- Silent or unattended installation, privileged helpers, or `sudo installer`.
- Automatic checks from the CLI or MCP server (the CLI checks only on request).
- Prerelease channels, downgrades, rollback, delta updates.
- Upgrading copies that were not installed by the release installer (source
  builds, the legacy CLI archive, or an app moved out of `/Applications`).
- Cancelling an in-progress download (quitting the app abandons it).
- Rendering release notes inside the app (a link opens the GitHub page).

## 4. Architecture

One new Go package does all update work; the CLI and desktop app are thin
adapters over it.

```
internal/version   Version string (ldflags-injected; "dev" otherwise)
internal/update    find latest release → compare → download → verify → hand off
internal/cli       `version`, `upgrade` commands
internal/desktop   update bindings, preference, progress events
desktop (main)     host: quit-then-open-Installer handoff, `_version` dispatch
desktop/frontend   launch pop-up, version footer, ⚙ settings dialog
scripts            release build injects and checks the version
```

### 4.1 `internal/version`

`var Version = "dev"`. Release builds set it to the tag without its `v`
(for example `0.3.0` or `0.3.0-rc.1`) with
`-ldflags "-X recall/internal/version.Version=<release>"`.
`Development()` reports whether the value is `dev` (or otherwise not valid
SemVer). Source and local builds therefore report `dev`.

### 4.2 `internal/update`

An `Updater` value with injectable dependencies (HTTP client, API base URL,
command runner, executable/bundle path, cache directory) so every step can be
tested without the network or Apple tools.

**Find the latest release.** `GET https://api.github.com/repos/moogie-labs/recall/releases/latest`
with `Accept: application/vnd.github+json` and `User-Agent: recall/<version>`
(no user-identifying data). That endpoint excludes drafts and prereleases,
which implements "stable only". The response is capped at 1 MiB. The release
must have a tag matching the release tag grammar in `scripts/release-macos.py`
and the assets `Recall-<v>-darwin-arm64.pkg` and
`Recall-<v>-darwin-arm64.pkg.sha256`; otherwise the check fails with a clear
message. The check has a 15-second timeout.

**Compare.** SemVer 2.0 precedence, implemented in a small internal function
(no new dependency): `0.3.0 > 0.3.0-rc.1 > 0.2.9`. An upgrade is available only
when latest > current. A development build never reports an upgrade; it reports
`unsupported`.

**Check result.** `{Current, Latest, Available bool, ReleaseURL, State}` where
`State` is `current`, `available` or `unsupported`.

**Eligibility.** Before downloading, the running copy must be an installer
copy: the CLI's resolved executable must be `/usr/local/lib/recall/recall`; the
app's bundle must be `/Applications/Recall.app`. The running copy must also be
Developer ID signed: its team identifier is read with
`/usr/bin/codesign -d --verbose=2 <path>` (`TeamIdentifier=…`). Ad-hoc or
unsigned copies are refused. Refusals explain how to install manually from the
Releases page.

**Download.** Into a fresh `0700` directory under
`~/Library/Caches/recall/upgrade-*`. Only `https` URLs on `github.com`,
`objects.githubusercontent.com` and `release-assets.githubusercontent.com` are
followed, including on redirects. The checksum file is capped at 4 KiB; the
package at the size the API reports, and never more than 512 MiB. Progress is
reported through a callback `(done, total int64)`. Leftover `upgrade-*`
directories older than one day are removed when a new download starts (the
package from the last run may still be open in Installer, so it is not removed
immediately).

**Verify.** All must pass, in order, or the package is deleted and the upgrade
fails:

1. The checksum file is exactly `<64 hex>  Recall-<v>-darwin-arm64.pkg\n` and
   the SHA-256 of the downloaded package matches.
2. `/usr/sbin/pkgutil --check-signature <pkg>` succeeds, reports a developer
   certificate issued by Apple for distribution and a notarization trusted by
   the Apple notary service, and its leaf certificate is
   `Developer ID Installer: … (<TEAM>)` with `<TEAM>` equal to the running
   copy's team identifier.
3. `/usr/sbin/spctl --assess --type install <pkg>` succeeds.

The checksum guards only against corruption (it comes from the same release);
steps 2 and 3 provide authenticity. Matching the running copy's team means no
team ID needs to be compiled in, and the credential-free build job is unchanged.

**Hand off.** `/usr/bin/open -b com.apple.installer <pkg>`. Installer.app shows
its usual screens, asks for administrator authorization and re-verifies the
signature itself.

All external tools are invoked by absolute path, without a shell.

### 4.3 CLI

Neither command opens or requires a collection.

- `recall version` prints `recall <version>`; with `--json`, `{"version": "…"}`
  in the CLI's standard result envelope. `recall --version` prints the same text.
- `recall upgrade --check` reports without downloading:
  `Recall 0.3.0 is available (installed: 0.2.0)`, the release URL and a hint to
  run `recall upgrade`; or `Recall 0.2.0 is up to date.` With `--json`:
  `current`, `latest`, `update_available`, `release_url`.
- `recall upgrade` does the check, then downloads (percentage progress on stderr
  in 10% steps, like `recall init`), verifies and opens Installer, printing
  `Opened Installer for Recall 0.3.0. Quit the Recall app before continuing.`
  No confirmation prompt: running the command is the request, and Installer asks
  again. When already current it prints the up-to-date message and exits 0.
- Development builds, ineligible copies, network failures and failed
  verification are errors with the codes `update_unavailable` (network/GitHub
  failures), `upgrade_unsupported` (development builds and copies not installed
  by the release installer) and `verification_failed` (checksum, signature,
  notarization or Gatekeeper failures), exiting 1.

### 4.4 Desktop backend

**Preference.** `desktop.json` gains `"checkForUpdates": bool` (missing means
true). `SaveRoot` currently rewrites the whole file; it and the new
`SaveUpdateCheck` both become read-merge-write (preserving unknown keys,
keeping the atomic temp-file rename) so neither setting erases the other.

**Bindings** (added to the renderer allowlist):

- `AppInfo() AppInfo` — `{version, development, checkForUpdates}`.
- `SetCheckForUpdates(bool) OperationReply`.
- `CheckForUpdate(manual bool) UpdateReply` — `{state, current, latest,
  releaseURL, problem}`, with `state` one of `current`, `available`,
  `unsupported`, `disabled`, or `failed` (with `problem`). A non-manual call returns `disabled` without
  network access when the preference is off.
- `StartUpgrade() OperationReply` — downloads and verifies the latest release
  (re-checking it), emitting `desktop:upgrade-progress`
  `{stage: "downloading"|"verifying", done, total}`. On success the verified
  package is recorded as the host's pending install and the host requests a quit.
  Only one upgrade may run at a time.

**Quit handoff** (host, `desktop/host.go`). The host's internal `installOnQuit(pkg)`
handoff (not exposed to the renderer) records the verified package and emits the
existing `desktop:quit-requested` event, so the renderer runs the same
draft-flush sequence as ⌘Q. In `CompleteQuit`, if an install is pending, the
host opens it in Installer **before** closing the collection. If opening fails,
the quit is aborted, nothing is closed and the problem is returned. If the
renderer cancels the quit (for example, a save failed), the pending install is
cleared and the renderer reports
`Upgrade cancelled: unsaved changes could not be saved.`

**Desktop binary.** `dispatch` handles a hidden `_version` argument that prints
the version, so the release build can check the app binary's embedded version.

### 4.5 Desktop UI

- **Launch check.** After startup (not blocking the collection), the renderer
  calls `CheckForUpdate(false)`. When the result is `available`, a modal dialog
  says "Recall 0.3.0 is available. You have 0.2.0." with **Upgrade**,
  **Not now** and a **Release notes** link (opened with the existing
  `OpenExternal`). This repeats on every launch. Network failures on the launch
  check are silent.
- **Version footer.** The sidebar footer shows `Recall 0.2.0` (or
  `Recall dev`). When an upgrade is known to be available, an
  **Upgrade available** button sits next to it and starts the upgrade.
- **⚙ Settings.** A gear button in the header (visible even with the sidebar
  collapsed) opens a Settings dialog: the version, a
  **Check for updates on launch** toggle, and **Check now**, which runs a manual
  check and shows its result or error inline (and offers **Upgrade** when one is
  available). For development builds the toggle and button are disabled with
  "Updates are only available in release installs."
- **Progress and errors.** While upgrading, the footer shows
  "Downloading Recall 0.3.0… 42%", then "Verifying…". Failures are shown inline
  with a **Retry**; the app remains fully usable throughout. On success the app
  quits through the normal draft-protected sequence and Installer appears.

## 5. Release pipeline

In `build_stage` of `scripts/release-macos.py`:

- Build the CLI with `-ldflags "-X recall/internal/version.Version=<release>"`.
- Pass the same flag to `scripts/build-desktop.sh`, which forwards its
  arguments to `wails build`.
- After building, require `recall version` to print `recall <release>` and the
  app binary's `_version` to print `<release>`.

Signing jobs are unchanged and still never execute built code. Release
contract tests (`scripts/test_release_macos.py`) cover the new flags and checks.

## 6. Privacy and documentation

The launch check is the desktop app's only routine network contact: one
anonymous request to the GitHub API, plus downloads from GitHub when the user
chooses to upgrade. README, `docs/desktop.md`, `docs/cli.md` and
`docs/releases.md` state this, describe the Settings toggle, the commands, the
verification steps, and that `recall upgrade` applies only to installer copies.
The README's "Quit Recall before installing" guidance remains true and the app
now quits itself before Installer continues.

## 7. Testing

- `internal/update`: fake GitHub server (latest release, missing assets,
  prerelease-shaped tags, oversized responses, redirects to disallowed hosts),
  SemVer precedence table, checksum format and mismatch, `pkgutil`/`spctl`/
  `codesign` outputs through a fake command runner (wrong team, unsigned, not
  notarized, ad-hoc running copy), eligibility paths, stale-directory cleanup.
- `internal/cli`: `version` text/JSON, `--version`, `upgrade --check` and
  `upgrade` against a fake updater, and that neither opens a collection.
- `internal/desktop`: preference merge (both directions, unknown keys kept),
  disabled launch check makes no request, single-upgrade guard, progress events.
- `desktop`: host pending-install handoff (open before close, open failure
  aborts quit, cancelled quit clears pending install) and `_version` dispatch.
- Frontend (Vitest): pop-up on `available`, Not now → footer button, footer
  button starts upgrade, Settings toggle persistence, Check now states,
  development-build disabled state, progress and error display.
- Release contract tests for version injection.

A real end-to-end upgrade (installed release → newer signed release) can only
be verified after two signed releases exist; it is recorded as a manual check
in `docs/verification.md` once performed.
