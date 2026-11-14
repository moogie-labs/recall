# Operations

## Source, recovery, backups

Back up `content/` and `config.json`; include verified models/runtime if you
want offline disaster recovery. Quiesce all writers (CLI, MCP, desktop, editors)
for a coherent backup. Copying live SQLite/WAL files is not a substitute for a
consistent backup; the index is disposable. For a new destination, first run
`recall --root /absolute/destination init` to create its layout and verified
assets. Then stop writers, restore `content/` and `config.json`, and run
`recall --root /absolute/destination sync --rebuild --wait`. Restoring a complete
quiescent collection backup with its layout/assets avoids another download.
Do not remove a live index while interfaces are using it. Unknown source
metadata/body bytes are preserved; malformed files are diagnosed, not repaired
by force.

Discovery output is not source validation: long titles remain authoritative in
files, fingerprints, inference inputs and explicit get/raw. Summary/hit
titles use a 256-byte UTF-8 display preview with `title_truncated`; exact IDs,
paths and revisions are never clipped. Compact JSON discovery data is capped at
32 KiB (including auxiliary arrays), individual rows at 4 KiB. Oversized pages
fail explicitly with `output_too_large`; lower limits/narrower scope or explicit
full get by known path are recovery options. CLI document-list auxiliary arrays
are not paged, so narrow folder scope when those dominate. Oversized error paths
are omitted via a fixed bounded error, not substituted preview references.
See `cli.md` for budget layers and terminal-display boundaries. Human previews
escape authored terminal controls; full raw document output deliberately does
not. Use redirection or JSON when inspecting untrusted raw source.

Direct editor changes reconcile at startup, explicit sync, and in long-lived
interfaces after a 250 ms quiet period, periodic 30 s scan, or watcher recovery.
Darwin uses 100 ms metadata polling, linear in entries; it is not a constant-cost
watcher. Atomic replacement saves are supported.

Deletion removes managed source and derived rows/chunks/vectors/jobs;
there is no application trash/soft-delete record. This is **not secure erasure**
of SSD blocks, SQLite free pages, WAL snapshots, OS snapshots, backups, copied
files, editor swap/backup files, or external agent transcripts.

## Runtime and editing

The optional [macOS desktop app](desktop.md) uses the same collection and shared
inference worker. Its built-in body editor autosaves after three quiet seconds
and flushes before switching or quitting. Conflicts preserve the draft and block
unsafe navigation/quit. Desktop installation commands are in the README.

`init` verifies pinned assets and performs real inference. Keep the complete
package together: bootstrap locates `assets/` relative to `bin/recall`, notably
`bin/assets/runtime/darwin-arm64/llama-b11669-bin-macos-arm64.tar.gz`.
`RECALL_ASSETS_DIR` is an optional absolute developer/admin override.
Weights are not in the release archive. Interrupted downloads are retryable;
checksum failures are rejected. Do not edit pinned identities to accept
unverified files. Before every inference-worker launch, Recall rehashes the model
and the complete installed runtime payload (`llama-server`, dylibs and links)
against digests compiled into the binary (`tree_sha256` in the runtime lock); a
modified or collection-supplied runtime is refused, never executed. Metadata CRUD and keyword search remain usable when semantic
assets fail. Retry `init` after fixing asset availability, not on every command.

Inference launches lazily, with a collection singleton manager, guardian and
native child. It uses private Unix sockets beneath `$HOME/.cache/recall/sockets`
(0700 directory, 0600 sockets/locks) keyed by canonical root. Darwin socket
paths are limited to 103 bytes; a very long HOME gives an explicit error.
The worker idles out after **120 seconds since its last completed inference
request**, only when no request is active. Health probes do not renew that
timer. A live shared worker is not a leaked process. There is no service
installation/launch agent. Do not kill a PID guessed from a stale pidfile;
wait for owned fixture workers to idle, or cancel a manager you actually own.
The guardian reaps its native child if the owning manager exits.

The editor is a **per-user** setting in
`~/Library/Application Support/recall/config.json` (default `["nvim"]`), e.g.
`{"editor": ["nvim","--clean","-u","NONE","-i","NONE","-n"]}`. A collection's own
`config.json` never selects the editor: a shared, synced or cloned collection must
not choose a program for Recall to run. A legacy `editor` key there is ignored,
with a warning from `document edit` when it differs. Recall appends the actual
source path as an argument, never interpolates a shell string. It releases
source locks during editing and reconciles afterward, including saved changes
on editor error. Neovim dirty-buffer external-change choices belong to Neovim;
review its warning before reloading or overwriting. Recall cannot protect
unsaved editor buffers against every external writer.

## Migrating a collection from `.note.md` to plain `.md`

Do this once on each computer, on the release that removes `.note.md`. Use one
Terminal window throughout, because later steps reuse its variables. Nothing
here needs `sudo` except the installer.

Why this order: the new build treats every `*.md` file under `content/` as a
note, and its first sync happens on any `recall` command or app launch. An
unrenamed `x.note.md` is safe (it simply appears as a note named `x.note`, and
can still be renamed later without re-embedding). What cannot be undone by a
later rename is that sync writing an `id:` block into stray plain `.md` files
and turning `*.task.md` files into notes, so review those **before the new build
runs**.

### 1. Stop everything that touches the collection

Quit the Recall app, exit every MCP client that runs `recall mcp` (Claude Code,
OpenCode and similar), and close any editor with a note open. Then check:

```sh
pgrep -lx 'recall|Recall|llama-server'
```

It should print nothing. The embedding worker exits by itself within about two
minutes of its last use, so wait and re-check if only `recall` or `llama-server`
remains.

### 2. Find the collection

Do not use `recall status` for this if the new build is already installed,
because it syncs. These commands only read settings:

```sh
echo "RECALL_HOME: ${RECALL_HOME:-not set}"
plutil -extract root raw -o - "$HOME/Library/Application Support/recall/desktop.json" 2>/dev/null; echo
```

The second line prints the collection the desktop app remembers, if any. The CLI
uses `RECALL_HOME` when set, otherwise `~/Documents/recall`. An older collection
may be at `~/.local/share/recall`. If two different paths appear, migrate each
one. Set the path, editing the first line if yours differs:

```sh
COLLECTION="$HOME/Documents/recall"
CONTENT="$COLLECTION/content"
ls -d "$CONTENT"
```

Only `content/` is touched below. `runtime/`, `models/`, `run/` and the index sit
beside it and are never scanned for notes.

### 3. Back up the notes

```sh
BACKUP="$HOME/recall-content-backup-$(date +%Y%m%d-%H%M%S)"
ditto "$CONTENT" "$BACKUP"
echo "$BACKUP"
```

### 4. Pre-flight (read-only)

```sh
echo "== Notes to rename:"
find "$CONTENT" -mindepth 1 -name '.*' -prune -o -type f -name '*.note.md' -print | wc -l
echo "== Collisions (x.note.md and x.md both exist):"
find "$CONTENT" -mindepth 1 -name '.*' -prune -o -type f -name '*.note.md' -print0 |
  while IFS= read -r -d '' file; do
    target="${file%.note.md}.md"
    if [ -e "$target" ] || [ -L "$target" ]; then printf '%s\n' "$file"; fi
  done
echo "== Plain .md files that will be adopted:"
find "$CONTENT" -mindepth 1 -name '.*' -prune -o -type f -name '*.md' ! -name '*.note.md' ! -name '*.task.md' -print
echo "== Task files that will become notes named x.task:"
find "$CONTENT" -mindepth 1 -name '.*' -prune -o -type f -name '*.task.md' -print
echo "== Notes in hidden paths, which stop being notes:"
(cd "$CONTENT" && find . -type f -name '*.note.md' -path '*/.*' -print)
```

Deal with whatever it lists, then run it again until you are satisfied:

| Listed under | What happens if you do nothing | Your options |
| --- | --- | --- |
| Collisions | The rename skips that file. It stays a separate note named `x.note`, so nothing is lost. | Compare with `diff`, merge by hand and delete one; or give one a different name first, keeping `.note.md` on the note you want renamed. |
| Plain `.md` files | Recall inserts an `id:` frontmatter block at first sync. The body is unchanged. A file with malformed frontmatter is left alone and listed as a diagnostic. | Keep them as notes; move them out of `content/`; or rename them to another extension such as `.txt`, which Recall leaves alone and reports as a diagnostic. |
| Task files | Each becomes a note named `x.task` with its text and old `state` keys intact. There is no task behaviour any more. | Keep them; drop the `.task` from the name with `mv -n -- 'x.task.md' 'x.md'`; move them out of `content/`; or delete them. |
| Hidden paths | They are left alone and listed as diagnostics. | Move them out of the hidden path if you want them as notes. |

### 5. Rename

This skips hidden paths, handles spaces, and refuses to overwrite. It is safe to
run twice.

```sh
find "$CONTENT" -mindepth 1 -name '.*' -prune -o -type f -name '*.md' -exec shasum -a 256 {} + | cut -d ' ' -f 1 | sort > "$BACKUP.sha256"
find "$CONTENT" -mindepth 1 -name '.*' -prune -o -type f -name '*.note.md' -print0 |
  while IFS= read -r -d '' file; do
    target="${file%.note.md}.md"
    if [ -e "$target" ] || [ -L "$target" ]; then
      printf 'SKIPPED, target exists: %s\n' "$file"
    else
      mv -n -- "$file" "$target"
    fi
  done
```

### 6. Install the new release and initialize

Install it as in the [README](../README.md):

```sh
shasum -a 256 -c Recall-VERSION-darwin-arm64.pkg.sha256
open Recall-VERSION-darwin-arm64.pkg
```

When Installer finishes, run this without `sudo`:

```sh
recall init
```

If your collection is not at the default path, add `--root "$COLLECTION"` to
every `recall` command in this and the next step.

This is the first time the new build opens the collection. It upgrades
`index.sqlite` in place from schema 2 to 3, keeping existing embeddings; drops
any index rows for tasks that no longer exist; records the new paths; and tests
inference. Renames alone do not cause re-embedding, because a note's embeddings
follow its `id`, title and body rather than its path.

### 7. Verify

```sh
find "$CONTENT" -mindepth 1 -name '.*' -prune -o -type f -name '*.md' -print | wc -l
recall status --json | grep -o '"documents":[0-9]*'
recall status --json | grep -o '"diagnostics":.*'
find "$CONTENT" -mindepth 1 -name '.*' -prune -o -type f -name '*.note.md' -print
find "$CONTENT" -mindepth 1 -name '.*' -prune -o -type f -name '*.md' -exec shasum -a 256 {} + | cut -d ' ' -f 1 | sort | diff - "$BACKUP.sha256" && echo "Note bytes unchanged"
recall sync --wait --json
recall search 'a phrase from one of your notes' --mode keyword
recall search 'the same idea in different words' --mode hybrid
```

Success looks like this:

- The two counts on the first two lines match. A difference means a file is
  invalid, or has a duplicate ID.
- `diagnostics` is `[]`, or lists only files you expected, such as `.DS_Store`.
- The `*.note.md` listing is empty, or shows only collisions you chose to keep.
- `Note bytes unchanged` is printed. If you kept stray plain `.md` files, the
  hash comparison shows a changed hash for each, because each gained an `id:`;
  every other file must match.
- Both searches return your notes.

### 8. Restart MCP clients

Start your MCP clients again so they launch the new `recall mcp` and pick up the
reduced tool list. An MCP server or app from an older build still running against
the upgraded index fails with `index_unavailable`, and an older build refuses a
schema 3 index with "unsupported index schema version". Do not point an old build
at a migrated collection.

To roll back, quit everything again, reinstall the old release, then:

```sh
ditto "$BACKUP" "$COLLECTION/content.restored"
mv "$COLLECTION/content" "$COLLECTION/content.migrated"
mv "$COLLECTION/content.restored" "$COLLECTION/content"
rm -f "$COLLECTION"/index.sqlite "$COLLECTION"/index.sqlite-wal "$COLLECTION"/index.sqlite-shm
recall sync --rebuild --wait
```

The rollback rebuilds the index, so that old build re-embeds every note.

## Developer packaging

Requires existing Go 1.27+, Python 3.11+, macOS `otool`, standard archive tools and
Darwin/arm64. No CMake/Ollama installation is required. Packaging uses Go's
resolved modules, `CGO_ENABLED=0`, `-trimpath`, and the complete checksum-verified
native payload; it neither compiles llama.cpp nor includes weights.

```sh
go build -o ./build/recall ./cmd/recall
sh scripts/package.sh
sh scripts/verify-package.sh
```

Outputs: `build/recall-darwin-arm64.tar.gz` and `.tar.gz.sha256`. The package
extracts as `recall-darwin-arm64/`, with executable `bin/recall`. Packaging
reuses `build/validation-assets/llama-b11669-bin-macos-arm64.tar.gz` after
size/SHA-256 verification; otherwise it downloads that pinned runtime.
`RECALL_RUNTIME_ARCHIVE` can select an existing archive (still verified).
First packaging also retrieves third-party notice sources at the pinned
llama.cpp commit and the Apache license; caches are ignored. Go module and Go
standard-library licenses/notices are copied from the actual linked graph.
Native vendor headers are retained in notices where they contain license text.
The runtime's statically linked BoringSSL and Fiat Crypto notices are included;
those dependencies are not apparent from `otool -L` alone.
No license for the owner's Recall code is invented by this process.

After developer dependencies are populated, run
`sh scripts/verify-default-offline.sh -v -count=1` for default executable workflows
with GOPATH/GOMODCACHE unset and
per-process outbound TCP denied. Compiler subprocesses retain their original
Go environment; Recall runtime processes still use isolated temporary HOME.
Notice-bearing Go source files retain their full bytes as `.go.txt`, so an
extracted release beneath the repository cannot enter `go test ./...` discovery.

The verifier checks checksum, paths, modes, links, manifests, notices, absence
of weights, `otool -L` dependency relocation and runtime startup from a clean
directory containing spaces. Native `--version` is not model inference; use
the real-model acceptance gate too. The checksum is not a signature.
These developer archive commands do not publish, tag, or use signing credentials.
The separate [release workflow](releases.md) signs and notarizes the combined
desktop/CLI installer only on version-tag pushes. Owner licensing remains a
separate decision. Never disable Gatekeeper to make a packaging test pass.

At 10,000 source files, a historical measurement of the Darwin watcher in the
former TUI consumed about 50% of one core while idle. The terminal UI has since
been removed, but the desktop still uses the shared polling watcher. See the
verification report before choosing large collections; that measurement is not
a desktop benchmark, and no low-idle-CPU SLA is claimed.
