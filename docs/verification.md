# Verification — 2026-10-21–22

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

## Final code verification matrix

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
lease were verified in **46.104 ms**. Its captured logs contained no source text.

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
`build/recall-darwin-arm64.tar.gz.sha256`. Extracted package executable:
`build/task14-package/recall-darwin-arm64/bin/recall` (local ignored build output).
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
kernel/native-device peak memory, SSD secure erasure, or exhaustive semantic
quality evaluation. This is an automated related-query smoke test, not a
retrieval benchmark. No Developer ID signing/notarization credentials were
used, no Gatekeeper bypass was performed, and no OpenCode config was modified.
The OpenCode V2 registration example was checked against its current official
MCP documentation; actual user registration remains an owner action.
