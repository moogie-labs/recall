# Recall

Local-first notes, tasks, and offline hybrid search for Apple Silicon macOS.
Typed Markdown files are authoritative; SQLite and embeddings are rebuildable.
No accounts, cloud API, Ollama, launch agent, or generated answers.

To build from source with Go 1.27+, run `go build -o ./build/recall ./cmd/recall` from the repository root; see [developer packaging](docs/operations.md#developer-packaging) for a complete distributable package.

## Install from a GitHub release (recommended)

On **Apple Silicon, macOS 13 or later**, download the `.pkg` installer and matching
`.pkg.sha256` from [GitHub Releases](https://github.com/moogie-labs/recall/releases).
For example, after downloading the `0.1.0` artifacts, run in their directory:

```sh
shasum -a 256 -c Recall-0.1.0-darwin-arm64.pkg.sha256
open Recall-0.1.0-darwin-arm64.pkg
```

Follow Installer's prompts (administrator authorization is required). Release
installers are Developer ID signed and notarized; do not bypass Gatekeeper if
verification fails. Quit any running Recall app before installing an update.
The installer adds **`/Applications/Recall.app`** and **`/usr/local/bin/recall`**.
The CLI is a symlink to `/usr/local/lib/recall/recall`, with its bundled assets and
notices alongside it; no `RECALL_ASSETS_DIR` setting is needed.

Then initialize **without sudo** and launch the app:

```sh
recall init
recall --help
open /Applications/Recall.app
```

Initialization downloads and verifies the model once (about 274 MB), then tests
real inference. The installer does not initialize or alter your existing collection.
If `/usr/local/bin` is not on your PATH, use `/usr/local/bin/recall` directly.
Release automation and maintainer setup are described in [releases](docs/releases.md).

## First use

For a developer/legacy CLI archive instead of the combined installer:

Obtain the `recall-darwin-arm64.tar.gz` archive and its `.sha256` from a trusted
distributor. In their containing directory:

```sh
shasum -a 256 -c recall-darwin-arm64.tar.gz.sha256
tar -xzf recall-darwin-arm64.tar.gz
cd recall-darwin-arm64
./bin/recall init
./bin/recall # Show CLI help
```

Keep `bin/assets/` beside `bin/recall` when moving the package. `init` extracts
the bundled llama.cpp runtime, downloads the pinned 274,290,560-byte Nomic FP16
model once, verifies both, and performs real inference. Internet is required
for that initial download, not normal indexing/search. Initialization is
retryable. End users need no Go, Python, CMake, compiler, or Ollama. The optional
`document edit` command needs an existing `nvim` (or a configured editor);
non-interactive CLI/MCP CRUD does not.

The central collection defaults to `~/Documents/recall`, regardless of the
project you run from. Override with absolute `RECALL_HOME` or `--root PATH`
(highest precedence). Use the same root for initialization and subsequent use.
Normal commands never implicitly initialize a collection.
Existing collections are not moved automatically. To keep using the former
default, pass `--root "$HOME/.local/share/recall"`, set an absolute `RECALL_HOME`,
or select that collection in the desktop app. A remembered desktop selection
continues to take precedence over the new default.

```sh
printf 'Reset credentials in account settings.\n' |
  ./bin/recall document create --type note --name account
./bin/recall document create --type task --name investigate </dev/null
./bin/recall task list --json
./bin/recall search 'forgot my password'
```

Running `recall` without a subcommand prints CLI help and exits; it does not
open an interactive terminal interface or require an initialized collection.
Use the [desktop app](#desktop-app-macos) for interactive browsing/editing.
CLI/MCP deletion remains explicit and non-interactive, without confirmation.

## Install to `/usr/local/bin`

After verifying and extracting the release archive above, run these commands
from the extracted `recall-darwin-arm64` directory:

```sh
sudo mkdir -p /usr/local/bin /usr/local/share/recall
sudo install -m 755 bin/recall /usr/local/bin/recall
sudo cp -R bin/assets /usr/local/share/recall/
RECALL_ASSETS_DIR=/usr/local/share/recall/assets /usr/local/bin/recall init
/usr/local/bin/recall
```

Because the bundled assets are installed separately from the executable, set
`RECALL_ASSETS_DIR` as shown whenever you run `init`. Run Recall itself without
`sudo` so the collection remains owned by your user. With `/usr/local/bin` on
your `PATH`, use `recall` instead of `./bin/recall` in the examples above.

## Desktop app (macOS)

Recall also has a Wails desktop app with a collapsible file tree, Markdown
preview/edit modes, and global search (⌘F). It uses the **same collection** as
the CLI and MCP, without installing an always-on service.

First initialize your collection using the CLI [first-use steps](#first-use)
above. The desktop app does not initialize collections or download models.

To build and install from this repository, use an Apple Silicon Mac with
macOS 13 or later, Go 1.27+, Node.js 22.12+ with npm, and Xcode command-line
tools (`xcode-select --install` if needed). From the repository root:

```sh
sh scripts/build-desktop.sh
mkdir -p "$HOME/Applications"
ditto build/Recall.app "$HOME/Applications/Recall.app"
open "$HOME/Applications/Recall.app"
```

The build script installs pinned frontend/build dependencies; no separate Wails
installation is required. The app defaults to `~/Documents/recall`. Use
**Collection…** to select an existing custom collection root (not its `content/`
subfolder); this choice is remembered for Finder launches.

Edits autosave after **3 seconds without typing**; “Autosaving” appears after
1 second. Switching documents or quitting saves pending edits immediately.
A green **Saved** indicator confirms the file write. Save failures/conflicts
keep the draft and block switching or quitting; use retry, copy, or explicit reload.
If a dirty source was deleted externally, **Close draft…** lets you explicitly
discard that draft without recreating the file. Click the sidebar's **Recall**
heading to select the collection root for new top-level items.

Local source builds are ad-hoc signed, **not Developer ID signed or notarized**.
The combined release installers use the signing/notarization pipeline above. Use macOS's
normal approval flow for software you trust; do not disable Gatekeeper. macOS
13 is the deployment target; native verification was performed on macOS 26.6.2,
not every earlier supported OS. See [desktop details](docs/desktop.md) for
verification commands and limitations.

## Guides

- [Desktop app](docs/desktop.md)
- [CLI and filters](docs/cli.md)
- [Agents and global OpenCode MCP registration](docs/agents.md)
- [Operations, backups, deletion, developer packaging](docs/operations.md)
- [Measured verification and release limitations](docs/verification.md)

Local development builds and legacy archives are not Developer ID signed or notarized.
Use macOS's normal approval flow for software you trust; these instructions do
not disable Gatekeeper or remove quarantine. A checksum proves integrity, not
publisher identity. Third-party notices ship in `notices/`; they do not assign
a license to the owner's Recall code. No public distribution permission for
that code is asserted here.
