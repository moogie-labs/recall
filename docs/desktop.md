# Recall desktop

## Installation and first use

See the README's [desktop installation instructions](../README.md#desktop-app-macos)
for the complete build, install, and launch commands. The CLI and desktop app
can be installed independently. Keep the CLI's packaged assets together for
`recall init`; the desktop app uses the already initialized collection's verified
model/runtime and embeds its own frontend assets.

The desktop build requires Apple Silicon macOS, Go 1.27+, Node.js 22.12+ with
npm, and Xcode command-line tools. `sh scripts/build-desktop.sh` produces
`build/Recall.app` with pinned Wails v2.17.0. A global Wails CLI is unnecessary.
Build prerequisites may need network access; normal app use does not.

The executable and Info.plist target macOS 13.0. Native tests ran on macOS 26.6.2
arm64 with Go 1.27.3 and Node 26.12.1. Earlier macOS versions have not been
individually verified. This build is ad-hoc signed, not Developer ID signed or
notarized. No signing credentials, publication, launch agent, or automatic updater
are configured. Use macOS's approval flow without disabling Gatekeeper.

## Collection and runtime

With no explicit choice, the collection is `~/.local/share/recall` (or absolute
`RECALL_HOME`). The **Collection…** button picks an existing collection root and
remembers it in `~/Library/Application Support/recall/desktop.json`. Finder
launches may not inherit your shell environment. An explicit executable
`--root /absolute/path` takes precedence at startup:

```sh
"$HOME/Applications/Recall.app/Contents/MacOS/Recall" --root /absolute/collection
```

The app never implicitly initializes or downloads assets. For a missing
collection, use the matching CLI `recall --root /absolute/collection init`, then
choose/reopen that collection. Do not select its `content/` subdirectory.

The existing lazy, per-collection embedding worker is shared with CLI/MCP clients.
Its private worker invocation never opens another GUI window; it can outlive the
desktop app for the established 120-second idle period. Closing the main window
quits the desktop app, rather than hiding it in the background.

## Browsing, editing, and deletion

- Folders start collapsed. Click a folder to select and expand/collapse it; tree
  expansion survives refreshes within the session. The sidebar and folders
  animate, respecting reduced-motion settings.
- The plus dropdown creates Folder, Note, or Task inside a selected folder,
  alongside a selected document, or at root with no selection. New tasks start
  Planned; new documents open in Edit, existing documents in Preview.
- ⌘F focuses global **Search**, replacing local editor find. Search is hybrid;
  semantic errors offer an explicit keyword-only retry. Escape clears search
  and restores the open draft. Selecting a result reveals its folder ancestors.
- Preview renders Markdown/GFM without running raw HTML or fetching images.
  External http/https links open only on explicit click. Attachments, relative
  link navigation, and executable embeds are not supported.
- Edit changes only the Markdown body. IDs, YAML comments/custom metadata, title
  metadata, task state and history are preserved. A heading edit does not rename
  the file or its metadata title. Use CLI/MCP for task state/history controls.
- The trash icon permanently deletes the selected item after confirmation.
  Folder deletion recursively deletes its contents; this is **not macOS Trash**.
  Keep backups. Do not confuse the active tree selection with the document open
  in the editor; selecting a folder leaves that document open.

## Saving and conflict recovery

Each edit resets a three-second quiet interval. The light is amber with
**Unsaved changes**, changes text to **Autosaving** after one second, and turns
green **Saved** only after the source write completes. Typing again clears the
Autosaving message and restarts both timers. The message describes a scheduled
save until disk I/O begins at three seconds.

Switching documents/collections, deleting affected source, closing, and quitting
flush pending edits immediately. Preview toggles and search do not discard the
draft or reset its save timers. A file can be saved while search indexing still
needs attention; indexing warnings do not masquerade as source-save failures.

Save failure is red with **Save failed**. The draft remains in memory and unsafe
navigation/quit is blocked. Retry a transient failure after fixing its cause, or
copy the draft. If CLI/MCP/external-editor changes conflict, autosaving pauses:
copy the draft, then **Reload file…** only when willing to discard local changes.
There is no force-overwrite or automatic merge. Externally deleted/malformed
sources are not automatically recreated. No unsaved-draft guarantee covers
Force Quit, a crash, or power loss.

The existing Darwin watcher polls metadata; large collections retain its
[documented idle-CPU limitation](operations.md). There is no new low-idle-CPU SLA.
V1 has no tabs, split pane, drag-and-drop moves, rename UI, or task dashboard.

## Verification

Root Go tests remain independent of Cocoa/frontend artifacts:

```sh
go test ./...
go test -race ./internal/desktop
```

In `desktop/frontend/`:

```sh
npm ci
npm run typecheck
npm test
npx playwright install chromium
npm run test:e2e
npm run build
```

After frontend build, `go test ./...` in `desktop/` checks native host contracts.
From the repository root:

```sh
sh scripts/build-desktop.sh
sh scripts/verify-desktop.sh
```

The verifier checks the **production** bundle's identity, arm64 architecture,
deployment target, relocation beneath a path with spaces, headless worker
dispatch, and absence of fixture bindings/weights. It separately builds
`build/Recall.verification.app` with `desktopverify`-only automation. Do not install
or distribute that verification bundle; `build/Recall.app` remains the release
candidate. Native tests use disposable roots/HOME, actual WKWebView/CodeMirror
input, the Go facade, on-disk saves, search, conflicting external changes, and
repeated Quit interception. Browser acceptance tests use only a fake bridge and
are not proof of native bindings.

An optional real-model native gate verifies the desktop executable itself
launches manager/guardian/native inference and successfully performs hybrid
search. Set `RECALL_VERIFY_MODEL` and `RECALL_VERIFY_RUNTIME` to files matching
the repository's pinned manifests before running the verifier. It verifies
their sizes/checksums, never downloads implicitly, and waits for the owned worker
lifetime lease to release after its normal idle timeout. That one fixture uses
a short `/private/tmp/recall-native-*` HOME because Darwin Unix socket paths
cannot fit the normal long test temp prefix. No PID guessed from a pidfile is
signalled, and no real Recall collection is used.

Automated native Quit verification exercises the same guard used by the window
close button. Human visual/accessibility checks and Gatekeeper approval remain
separate checks; a browser screenshot is not a native visual inspection.

### Recorded checks (2026-10-27)

- Root `go test ./...` and desktop facade race tests passed.
- Frontend typecheck/build, 29 unit/component tests, and 4 Chromium acceptance
  tests passed; npm audit reported zero vulnerabilities.
- Production bundle inspection and relocated headless worker dispatch passed.
- Native WKWebView fixtures passed actual 1s/3s autosave, Markdown modes, source
  preservation, nested folder/note/task creation and recursive deletion,
  keyword fallback, repeated conflict-blocked Quit, and immediate quit flush.
- A separate native fixture passed real pinned-model hybrid search through a
  manager/guardian launched by the desktop executable. Its lifetime lease was
  observed released after the existing idle timeout.
- The existing CLI archive verifier passed checksum/relocation/runtime checks;
  its long offline/scale benchmarks were not rerun for this desktop change.
