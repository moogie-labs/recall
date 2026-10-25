# Recall

Local-first notes, tasks, and offline hybrid search for Apple Silicon macOS.
Typed Markdown files are authoritative; SQLite and embeddings are rebuildable.
No accounts, cloud API, Ollama, launch agent, or generated answers.

To build from source with Go 1.27+, run `go build -o ./build/recall ./cmd/recall` from the repository root; see [developer packaging](docs/operations.md#developer-packaging) for a complete distributable package.

## First use

Obtain the `recall-darwin-arm64.tar.gz` archive and its `.sha256` from a trusted
distributor. In their containing directory:

```sh
shasum -a 256 -c recall-darwin-arm64.tar.gz.sha256
tar -xzf recall-darwin-arm64.tar.gz
cd recall-darwin-arm64
./bin/recall init
./bin/recall
```

Keep `bin/assets/` beside `bin/recall` when moving the package. `init` extracts
the bundled llama.cpp runtime, downloads the pinned 274,290,560-byte Nomic FP16
model once, verifies both, and performs real inference. Internet is required
for that initial download, not normal indexing/search. Initialization is
retryable. End users need no Go, Python, CMake, compiler, or Ollama. Interactive
editing needs an existing `nvim` (or a configured editor); CLI/MCP CRUD does not.

The central collection defaults to `~/.local/share/recall`, regardless of the
project you run from. Override with absolute `RECALL_HOME` or `--root PATH`
(highest precedence). Use the same root for initialization and subsequent use.
Normal commands never implicitly initialize a collection.

```sh
printf 'Reset credentials in account settings.\n' |
  ./bin/recall document create --type note --name account
./bin/recall document create --type task --name investigate </dev/null
./bin/recall task list --json
./bin/recall search 'forgot my password'
```

In the TUI: Tab changes pane; Enter opens the selected real source in the
editor; `t` opens tasks; `/` opens search; `?` shows contextual keys; `q` backs
out/quits. Destructive TUI actions require confirmation; CLI/MCP do not.

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

## Guides

- [CLI and filters](docs/cli.md)
- [Agents and global OpenCode MCP registration](docs/agents.md)
- [Operations, backups, deletion, developer packaging](docs/operations.md)
- [Measured verification and release limitations](docs/verification.md)

This build is not Developer ID signed or notarized. No credentials were used.
Use macOS's normal approval flow for software you trust; these instructions do
not disable Gatekeeper or remove quarantine. A checksum proves integrity, not
publisher identity. Third-party notices ship in `notices/`; they do not assign
a license to the owner's Recall code. No public distribution permission for
that code is asserted here.
