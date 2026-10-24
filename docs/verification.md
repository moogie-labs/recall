# Verification — baseline 2026-10-21–22; correction 2026-10-23–24

## Environment and scope

Apple M1 **Pro**, 16 GiB, Darwin/arm64, macOS 26.6.2 (25G83); Go 1.27.1;
Python 3.14.7; installed Neovim 0.12.4 (Release, LuaJIT 2.1.1782726002).
This is not a measurement of a baseline M1 or every supported macOS version.
llama.cpp reports `0.7.0-dev`, build 11669, commit `620d2225c`, AppleClang
21.0.0.21000101. Model: pinned Nomic Embed Text v1.5 FP16, 768 dimensions.

All data/editor/inference fixtures used isolated HOME and temporary collections.
Only the inference fixture used a short 0700 HOME beneath `/private/tmp` because
the harness's normal temporary path plus Unix-socket suffix exceeds Darwin's
103-byte pathname limit. Fixtures/processes were owned by the harness, not the
user's Recall root or desktop. No user configuration, firewall, networking,
launch agents or global installation was changed. Nothing was published/tagged.

## Final correction wave (I1/I2/M1)

At base `364862d` plus the final output/filter correction, fresh checks passed:

| Command / gate | Actual result |
| --- | --- |
| `go test ./... -count=1` | All 17 test packages pass (assets/cmd have no tests) |
| `go test -race ./internal/model ./internal/index ./internal/search ./internal/app ./internal/cli ./internal/mcpserver -count=1` | All six affected packages pass, no race report |
| `go vet ./...`; `gofmt -l ./cmd ./internal ./integration`; `git diff --check` | Exit 0; no output |
| Focused I1 RED/GREEN | Public MCP/JSON CLI, keyword/vector and all three retrieval modes: large titles/IDs, encoded bytes, whole pages and auxiliary arrays; exact references/get/source preserved |
| Focused I2 RED/GREEN | Actual executable, pipe-captured list/folder/search/diagnostic/error controls escaped; passage newlines preserved; full raw and decoded JSON unchanged; status/sync covered too |
| Focused M1 RED/GREEN | Keyword/vector/semantic/hybrid and CLI/MCP exclude notes when state-filtered; authored note state retained |
| Final-score row boundary | RED reproduced a 4,097-byte final hit; GREEN checks scored/fused rows as well as initial vector rows |
| Hidden evidence versus returned page | RED rejected a representable one-hit hybrid page; GREEN streams row-bounded keyword evidence and applies the payload budget to the actual final page |
| Cached native executable workflow | Real CLI and MCP keyword/semantic/hybrid, 768-component normalized vector, init verification and owned worker cleanup pass (12.56 s test under concurrent verification); no new weights downloaded |
| `go build -o ./build/recall ./cmd/recall` | Exit 0 |
| Cached release rebuild + `sh scripts/verify-package.sh` | Exit 0; checksum/modes/links/notices/manifests/no weights, 41 relocated Mach-O files, native `--version` |
| Pipe-captured rebuilt release CLI/MCP smoke | Title bounds, exact raw/revisions, inert human previews, task-only state filtering and inference-free keyword discovery pass |

Exact commands/output and local logs are recorded in
`.superpowers/sdd/2026-10-18-recall-v1/final-fix-report.md` and
`build/final-fix-*.log`. Packaging used the existing verified runtime, cached
notice files with a cache-only fetch function (missing cache fails), and Go's
offline module settings. No new asset download, global install, registration,
publication or user collection/configuration change occurred.

The output contract measures compact Go JSON **data values**: 256 UTF-8 bytes
per display title including any ellipsis, explicit `title_truncated`, 4 KiB per
summary/hit/diagnostic/folder reference, 32 KiB per list/search data payload
including auxiliaries. CLI/MCP envelopes, SDK text/structured copies and human
escaping add overhead. An unrepresentable/oversized exact reference or page
returns bounded `output_too_large`, never a clipped identity or skipped rows.
Full explicit document/raw/history operations remain unbounded; source bytes,
inference inputs, fingerprints and captured revision checks are unchanged.
State filtering adds a task-kind condition, not a source-schema restriction.

The existing source/query deletion, lease/reset, stale revision, TUI and other
default regressions passed in the full suite; the six-package race run includes
the owning app/index/search race regressions. Synthetic vectors establish
contracts, not semantic quality. New terminal tests use pipes, **not a PTY**:
no malicious terminal action is executed. Source-output safety and the cached
native smoke test are **not dynamic active-request log privacy verification**.
The longer real packaged offline/idle/Neovim/scale gates below are prior baseline
evidence, not rerun in this correction; unchanged long benchmarks/idle waits were
not repeated. Their existing limits, including ~50% one-core idle CPU at 10k
files and the unrun active-log privacy gate, still apply.

## Prior baseline verification matrix (2026-10-21–22)

| Command / gate | Actual result |
| --- | --- |
| `gofmt -l ./cmd ./internal ./integration` | No files listed |
| `go vet ./...` | Exit 0 |
| `go test ./...` | All 17 test packages pass; assets/cmd have no tests; opt-in measurements/real model excluded |
| `go test -race ./...` | All 17 test packages pass, no race report |
| `go build -o ./build/recall ./cmd/recall` | Exit 0 |
| `sh scripts/package.sh` | Relocatable Darwin/arm64 archive and SHA-256 sidecar built |
| `sh scripts/verify-package.sh` | Checksum, safe paths/links, modes, manifests, notices, no weights; 41 Mach-O files checked with `otool -L`; clean-location `recall --help` and native `--version` pass |
| Real packaged CLI/MCP + network denial | Passed the opt-in workflow below; semantic related query, hybrid/keyword, source CRUD and shared worker reuse |
| Installed Neovim CLI/TUI PTYs | Automated actual-editor checks pass; human visual review is **not run** |
| Scale/idle | Measured below, including significant large-collection idle CPU; no invented SLA |

Default executable tests cover uninitialized exit/JSON, two project cwd values
sharing a central collection, typed-file adoption/plain Markdown preservation,
source state/history after index removal/rebuild, concurrent writers, all 14
MCP tools and cross-interface CRUD. Hostile runtime archive classes include
traversal, absolute paths, wrong prefixes, escaping/dangling/cyclic/arbitrary
links, hardlinks and FIFOs. Existing bootstrap link-chain/write-through gates
remain in place.

Default metadata fixtures are explicitly synthetic: 0700 `content/`, `run/`,
`models/` and `runtime/` directories plus 0600 `config.json` containing
`{"timezone":"UTC"}`. They have no asset identities, weights, preowned worker or
precreated index, and are not claimed as public `init` output. The executable
creates the derived index on first use. Setup uses this on-disk contract, not
private config/application helpers. Only the separately built executable can
host inference; the integration test executable rejects worker dispatch before
testing flags, rather than entering the CLI in process.

The lazy compiler subprocess alone retains its original startup environment,
including the effective Go cache/config paths. Every Recall subprocess still
inherits the isolated runtime HOME/root. This avoids directing a child build
into a new empty temporary-HOME module cache when GOPATH/GOMODCACHE are unset.
With developer dependencies already populated, reproduce the network-denied
default-workflow regression gate without explicit cache variables:

```sh
sh scripts/verify-default-offline.sh -v -count=1
```

This Darwin developer harness proves outbound TCP denial with EPERM/EACCES and
runs the default integration workflows with GOPATH/GOMODCACHE unset. It does
not download dependencies to make the tests pass. The metadata-only PTY import
also works with no editor on PATH; Neovim discovery occurs only on actual use.

The added 80-candidate search test streams increasingly good, differently
ranked vectors, fills the 50-slot heap, forces 30 evictions, and asserts every
retained ID in order (late best `079`, last retained `030`). Existing owning
tests for deletion during embedding, source/state deletion during queries,
rebuild generation races and stale source revisions remain intact and passed.
Synthetic vectors are not semantic-quality evidence.
The real executable gate also creates a large task, starts `sync --wait`,
requires a second public semantic query to report an already leased `indexing`
job, then deletes the task while that indexing is active. The indexing process
finishes without resurrecting its source, searchable data or task history.

## Cold onboarding and actual network-denied execution

```sh
RECALL_REAL_MODEL_TEST=1 go test ./integration -run 'TestReal|TestOffline' -v -count=1 -timeout=15m
# Subsequent runs reuse the verified cache instead of downloading weights again:
RECALL_REAL_MODEL_TEST=1 \
RECALL_REUSE_MODEL="$PWD/build/validation-assets/nomic-embed-text-v1.5.f16.gguf" \
go test ./integration -run 'TestReal|TestOffline' -v -count=1 -timeout=15m
```

One genuinely nonexistent collection was initialized using the extracted
package, without an assets override, preinstalled model/runtime, or preowned
manager. `init` downloaded all 274,290,560 model bytes, verified identities,
extracted the runtime, automatically launched its manager/guardian/native chain,
and returned `semantic_ready:true` in **14.358 s**. Later runs explicitly seed
only a checksum-verified model, not config/runtime/manager; these are **not**
described as pristine downloads. No second model download was needed.

The first full real run subsequently failed a **test assumption**: the keyword
query used words absent from the source. Keyword now uses literal `password`;
semantic/hybrid still test the related query `forgot my password`. The initial
successful cold-init observation is retained, not misreported as a full-suite
pass. The corrected repeated workflow passes.

After initializing/indexing, the harness waits for the **actual production
120 s idle policy**, absent manager/native sockets, a free lifetime flock and
no matching process. It then launches CLI and MCP through:

```text
/usr/bin/sandbox-exec -p '(version 1)(allow default)(deny network-outbound (remote ip "*:*"))' <packaged-recall> ...
```

A TCP connection to a harness-owned loopback listener succeeds outside the
sandbox. Within the same profile both that loopback connection and an external
`1.1.1.1:443` connection fail with **errno 1 (EPERM)**, not a DNS/route timeout.
Private Unix sockets remain usable. The first sandboxed query has no existing
worker and lazily launches a fresh restricted manager; its guardian/native
descendants inherit the restriction. Thus this is not merely native `--offline`
or a disconnected client reusing an unsandboxed process. Both packaged CLI and
MCP perform semantic/hybrid/keyword retrieval and source CRUD under denial.
No API credential is used or required.

## Observed latency, indexing and memory

Successful repeat with a verified seeded model (not pristine download):

| Observation | Time |
| --- | ---: |
| Fresh packaged config/runtime + automatic manager + inference | 2.904 s |
| Initial three-document semantic indexing (`sync --wait`, warm model) | 75.040 ms |
| Warm CLI semantic related query | 27.702 ms |
| Cold **process/model-load** CLI semantic query, networking denied | 350.640 ms |
| Warm denied CLI semantic / hybrid / keyword | 38.354 / 41.077 / 22.064 ms |

These are wall-clock command measurements, including CLI startup, reconciliation
and I/O. “Cold process/model-load” is not a cold OS filesystem cache or a new
download. The latency corpus is two short notes and one task, not a throughput
study. A separate large task was added/deleted for the in-flight deletion gate;
the subsequent worker RSS samples include the effect of that workload.
Observed manager shutdowns occurred after **121.553 s** and **121.698 s** of
monitoring following completed work, with sockets removed, lifetime lease free
and no matching manager/guardian/native chain left. No guessed PID was signalled.
A separate deliberately owned manager was cancelled through its actual process
handle, not a discovered PID; native reaping, socket removal and free lifetime
lease were verified in **46.104 ms**. This manager received no source/query
request: this is lifecycle evidence only, **not active-workload log privacy
evidence**. The CLI convenience helper captures but does not assert stderr on
success. A targeted captured active-worker/source-query log gate was not run.

The largest sampled aggregate worker-chain RSS in that repeat was **424,288
KiB** (1 s `ps` samples). This is **not a true peak/high-water mark**; RSS can
double-count shared pages and does not account for all Metal/device memory.
The genuine-download run's sampled maximum was 424,512 KiB. No kernel peak or
complete allocation profile was obtained; do not present these as either.
The final repeated real workflow, including in-flight deletion, production
idle waits, owned-manager cancellation and network denial, passed in **269.24 s**.

## 10,000 synthetic chunks and actual TUI idle scale

```sh
RECALL_SCALE_TEST=1 go test ./internal/search -run TestExactSearchTenThousand -v -count=1
python3 scripts/verify-scale.py build/recall
```

Publishing 10,000 synthetic 768-float vectors took **383.599 ms**. Five exact
SQLite stream + cosine scan + bounded heap runs took **155.267, 157.153,
151.759, 151.433, 151.399 ms**, retaining and checking the expected best 50
passages. This is a component measurement: no model inference, source scanning,
or semantic-quality claim. It is **not a full-app CPU profile**.

Separately, the actual CLI-launched TUI was monitored idle for about 35 seconds
per metadata-only collection (including 100 ms Darwin metadata polling and a
30 s reconciliation). Process CPU-time deltas and 1 s RSS samples:

| Source files | Initial CLI metadata sync | Idle wall time | CPU time / one-core % | Sampled TUI RSS KiB |
| ---: | ---: | ---: | ---: | ---: |
| 0 | 0.018 s | 36.252 s | 0.290 s / 0.80% | 26,640–27,744 |
| 1,000 | 0.214 s | 35.658 s | 2.880 s / 8.08% | 40,528–40,864 |
| 10,000 | 2.410 s | 35.531 s | 17.830 s / **50.18%** | 111,728–129,392 |

This measures the whole TUI process, not system-wide CPU or native inference;
the fixture had no installed model. Source count is distinct from chunk count.
Polling is linear in entries, and its measured 10k-file idle cost is a material
concern. No unapproved change to the agreed 100 ms policy was made. Exact
scanning also has a measured ~150 ms cost at 10k chunks; consider these costs
when selecting a collection scale, rather than assuming a performance SLA.

## Actual installed Neovim evidence

```sh
python3 scripts/verify-neovim.py build/recall
```

The harness launches actual CLI/TUI processes with owned PTYs and installed
Neovim, isolated HOME/XDG paths, `--clean -u NONE -i NONE -n` (no user config,
shada or swap). It checks both note/task actual source paths using Neovim's
own buffer filename, saves using `backupcopy=no writebackup nobackup`, verifies
inode replacement/content, and checks source-backed task history afterward.
For each type, a dirty buffer plus external atomic replacement produces native
Neovim **W12**; native OK/keep cancels reload without overwriting disk, and native
Load reloads external content while discarding the dirty draft. Terminal modes
are checked after clean CLI/TUI exit and TUI cancellation with Neovim active.
Only Darwin's transient kernel `PENDIN` bookkeeping bit is excluded; raw/echo,
canonical processing, other flags/speeds/control characters are compared.

Real cancellation exposed a defect: the killed editor left raw state that
Bubble Tea captured as its new original. An owning-package failing PTY test
reproduced the changed input/output/local flags; the fix restores the released
terminal state before TUI recapture. Both the regression and real Neovim gate
then passed. Fake editors are used only for that narrow regression, not as
Neovim evidence.

## Artifacts, notices and remaining limits

Archive: `build/recall-darwin-arm64.tar.gz`; checksum:
`build/recall-darwin-arm64.tar.gz.sha256`. Extracted final handover executable:
`build/handover/recall-darwin-arm64/bin/recall` (local ignored build output,
created from the final verified archive rather than an older test extraction).
The runtime archive is exactly beside it under
`bin/assets/runtime/darwin-arm64/llama-b11669-bin-macos-arm64.tar.gz`.
For first use, see the root README; keep `bin/assets/` with the executable.
These are local artifacts, not download/publication links.

Notices include llama.cpp/MIT, embedded native vendor licenses (including the
CPU kernel notice), statically linked BoringSSL/Fiat Crypto, Nomic/Apache-2.0,
the linked Go module graph, and Go's LICENSE/PATENTS. `nm` exposed static TLS
symbols that `otool -L` cannot show. Apple system frameworks are not bundled.
No license was invented for the owner's own code.

**Not run / not claimed:** human visual judgement, personal Neovim configs,
download-quarantine/Gatekeeper approval UX, other Macs/macOS versions, true
kernel/native-device peak memory, targeted active-request log privacy evidence,
SSD secure erasure, or exhaustive semantic
quality evaluation. This is an automated related-query smoke test, not a
retrieval benchmark. No Developer ID signing/notarization credentials were
used, no Gatekeeper bypass was performed, and no OpenCode config was modified.
The OpenCode V2 registration example was checked against its current official
MCP documentation; actual user registration remains an owner action.

## Independent corrected-commit verification — 2026-10-24

After all branch-review findings were corrected and the scoped correction review
passed, the coordinator ran a fresh matrix at exact revision
`6d399cc01f73cdd6bbe67db38a13a8070aa18487` (product correction `a161dc7`).
The run exited 0 and verified that HEAD did not change while tests ran:

- `gofmt -l ./cmd ./internal ./integration`: no files listed; `go vet ./...`: exit 0.
- `go test -count=1 ./...` and `go test -race -count=1 ./...`: all 17 tested
  packages passed without cached test results; two additional packages have no tests.
- Rebuilt the executable and distribution; standard package verifier passed,
  including 41 relocated Mach-O files, notices, checksums and no bundled weights.
- `sh scripts/verify-default-offline.sh -v -count=1`: passed with the original
  compiler-cache regression constrained offline.
- Cached actual executable CLI/MCP and embedding smoke checks passed with
  768-dimensional vectors; no model redownload was required.
- `python3 scripts/verify-neovim.py build/recall`: actual clean Neovim note/task
  paths, atomic saves, W12 keep/reload and canceled-editor terminal restoration passed.
- The real packaged workflow with `RECALL_REAL_MODEL_TEST=1` and
  `RECALL_REUSE_MODEL` passed in 270.804 seconds, including a new restricted
  worker chain and CLI/MCP under outbound-network denial. This was a verified
  seeded repeat, not another pristine download or a new performance benchmark.
- The 10,000-chunk exact-search check passed. Earlier idle CPU measurements and
  all explicitly unrun limits above remain unchanged.
- Final observations: no real default Recall collection existed and the real
  socket cache had zero entries. The source worktree was clean.

The subsequent documentation-only checkpoint records this evidence; it does not
change the tested product source or turn the unrun limits into passed checks.
