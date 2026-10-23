# Operations

## Source, recovery, backups

Back up `content/` and `config.json`; include verified models/runtime if you
want offline disaster recovery. Quiesce all writers (CLI, MCP, TUI, editors)
for a coherent backup. Copying live SQLite/WAL files is not a substitute for a
consistent backup; the index is disposable. For a new destination, first run
`recall --root /absolute/destination init` to create its layout and verified
assets. Then stop writers, restore `content/` and `config.json`, and run
`recall --root /absolute/destination sync --rebuild --wait`. Restoring a complete
quiescent collection backup with its layout/assets avoids another download.
Do not remove a live index while interfaces are using it. Unknown source
metadata/body bytes are preserved; malformed files are diagnosed, not repaired
by force.

Direct editor changes reconcile at startup, explicit sync, and in long-lived
interfaces after a 250 ms quiet period, periodic 30 s scan, or watcher recovery.
Darwin uses 100 ms metadata polling, linear in entries; it is not a constant-cost
watcher. Atomic replacement saves are supported. Best-effort state history
cannot reconstruct every intermediate external edit. Existing history is in
the task source, not a database-only audit log.

Deletion removes managed source and derived rows/history/chunks/vectors/jobs;
there is no application trash/soft-delete record. This is **not secure erasure**
of SSD blocks, SQLite free pages, WAL snapshots, OS snapshots, backups, copied
files, editor swap/backup files, or external agent transcripts.

## Runtime and editing

`init` verifies pinned assets and performs real inference. Keep the complete
package together: bootstrap locates `assets/` relative to `bin/recall`, notably
`bin/assets/runtime/darwin-arm64/llama-b11669-bin-macos-arm64.tar.gz`.
`RECALL_ASSETS_DIR` is an optional absolute developer/admin override.
Weights are not in the release archive. Interrupted downloads are retryable;
checksum failures are rejected. Do not edit pinned identities to accept
unverified files. Metadata CRUD and keyword search remain usable when semantic
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

`config.json` accepts an editor argv array, e.g.
`["nvim","--clean","-u","NONE","-i","NONE","-n"]`. Recall appends the actual
source path as an argument, never interpolates a shell string. It releases
source locks during editing and reconciles afterward, including saved changes
on editor error. Neovim dirty-buffer external-change choices belong to Neovim;
review its warning before reloading or overwriting. Recall cannot protect
unsaved editor buffers against every external writer.

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
Publishing, tags, Developer ID signing/notarization and owner licensing are
separate owner decisions; none are performed here. Never disable Gatekeeper
to make a packaging test pass.

At 10,000 source files, the measured unchanged Darwin watcher consumed about
50% of one core while idle in the TUI. See the verification report before
choosing this build for large collections; no low-idle-CPU SLA is claimed.
