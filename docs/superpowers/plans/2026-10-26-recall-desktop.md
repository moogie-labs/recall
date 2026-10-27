# Recall Desktop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship an Apple Silicon macOS Recall app matching the approved tree, Markdown preview/editor, search, and safe autosave experience.

**Architecture:** A Wails v2 host binds a narrow Go desktop facade to a React/TypeScript interface, reusing `internal/app` for every collection operation. The desktop executable dispatches existing embedding-worker invocations before initializing Wails. A frontend save coordinator serializes draft writes and flushes before navigation and shutdown.

**Tech Stack:** Existing Go 1.27+ backend; stable Wails v2; React, TypeScript, Vite, CodeMirror 6; `react-markdown`/`remark-gfm`/`rehype-sanitize`; Vitest, React Testing Library, Playwright for browser-only UI tests; native packaged-app smoke tests on macOS.

**Spec:** `docs/superpowers/specs/2026-10-26-recall-desktop-design.md` (approved; read it with this plan).

**Execution result (2026-10-27):** All nine tasks completed via user-selected native execution, followed by one independent whole-branch review and one regression-tested safety-fix pass. Direct desktop installation commands are included in `README.md`. See `docs/desktop.md` for current verification evidence; the original step checkboxes below describe the execution recipe rather than current status.

## Global Constraints

- macOS-only desktop application, built with Wails and the existing Go backend.
- Initially target Apple Silicon macOS, matching the shipped native inference runtime.
- A global search bar labeled **Search**, focused by **⌘F** (not ⌘K).
- A collapsible left sidebar headed **Recall**, with a basic file tree.
- Folders start closed; animate sidebar, folders, and chevrons; respect reduced motion.
- **Autosaving** after one quiet second; source write after three quiet seconds; **Saved** only after successful saving; further typing resets both timers.
- Switching documents and quitting flush pending edits immediately.
- Edit the Markdown body, not raw YAML frontmatter.
- No always-on desktop service, launch agent, public HTTP listener, or Electron.
- Preserve CLI/TUI/MCP behavior, source authority, expected-revision checks, and private shared-worker ownership.
- Signing/notarization and publication need owner credentials/approval and are not performed as part of this feature.
- Never use or modify the owner's actual collection for automated validation.

## Review Focus

- Unicode, punctuation, and same-named files in different folders must retain their real paths/IDs, not collide as UI keys (Task 3).
- IME composition must not save a half-composed character or let ⌘F/draft refresh corrupt editor input (Tasks 5 and 6).
- Out-of-order search, document-load, save, and old-collection events must not replace the active document or report a newer draft Saved (Tasks 4, 6, and 8).
- A source write followed by indexing failure must not strand a stale revision, duplicate a write, or falsely claim source loss (Task 2).
- Repeated Quit/close requests while a save is blocked or failing must preserve the draft, prevent shutdown, and offer recovery (Task 8).

## File map and execution conventions

Keep the root Go module headless and its existing tests unchanged. Put Wails in
a nested `desktop/` module (`module recall/desktop`, `require recall v0.0.0`,
`replace recall => ..`), so `go test ./...` at the root does not need generated
frontend assets, Cocoa, or a webview. This import path is beneath `recall`, so
the host can import `recall/internal/desktop` and `recall/internal/cli`. Pin the
same Go requirement in both modules. The first task must validate this layout
with the actual Wails build tool, not assume it works.

| Files | Responsibility |
| --- | --- |
| `desktop/{go.mod,go.sum,wails.json,main.go,dispatch.go,dispatch_test.go}` | Isolated Wails host and headless worker entry |
| `desktop/{host.go,host_test.go,assets.go}` | Native dialogs, events, quit handshake, bundled frontend |
| `desktop/frontend/{package.json,package-lock.json,index.html,tsconfig.json,vite.config.ts,vitest.config.ts,playwright.config.ts}` | Pinned frontend tooling and tests |
| `desktop/frontend/src/{main.tsx,App.tsx,app.css}` | Application shell and approved visual system |
| `desktop/frontend/src/api/{types.ts,bridge.ts,fakeBridge.ts}` | Narrow typed Wails boundary and browser-test substitute |
| `internal/desktop/{types.go,service.go,service_test.go}` | DTOs, collection sessions, lifecycle, shared-app facade |
| `internal/desktop/{documents.go,documents_test.go,tree.go,tree_test.go}` | Revision-safe body saves, tree summaries, create/delete |
| `internal/desktop/{search.go,search_test.go,watch.go,watch_test.go,preferences.go,preferences_test.go}` | Search, owned watcher/index work, explicit-root preferences |
| `desktop/frontend/src/saving/{coordinator.ts,coordinator.test.ts}` | Timers, generations, save serialization, flush |
| `desktop/frontend/src/navigation/{workspace.ts,workspace.test.ts}` | Active/open selection, draft retention, guarded transitions |
| `desktop/frontend/src/tree/{Tree.tsx,Tree.test.tsx,CreateDialog.tsx,DeleteDialog.tsx}` | Animated accessible tree and CRUD dialogs |
| `desktop/frontend/src/document/{DocumentPane.tsx,DocumentPane.test.tsx,MarkdownPreview.tsx,MarkdownPreview.test.tsx}` | CodeMirror body editor and safe preview |
| `desktop/frontend/src/search/{Search.tsx,Search.test.tsx}` | Global search and results |
| `desktop/frontend/src/{App.test.tsx,shutdown.ts,shutdown.test.ts}` | UI integration and quit flush protocol |
| `desktop/frontend/tests/{workspace.spec.ts,security.spec.ts}` | Browser UI acceptance tests against fake bridge |
| `scripts/{build-desktop.sh,verify-desktop.sh}` | Reproducible desktop build and isolated packaged smoke gate |
| `desktop/build/{darwin/Info.plist,appicon.png}` | Native identity/resources, only if required by verified Wails configuration |
| `docs/desktop.md`, `README.md`, `docs/operations.md` | Installation, build, behavior, supported macOS, limitations |

Tests in each task use injected app/runtime/clock dependencies; never test via
the user's collection. Each task follows red → green and ends with a focused
commit of its listed files, not `git add .`. The commit instructions below are
suggested messages; do not commit other work. Run shell commands separately.

## Shared interface contract

Define these in Task 1/2 before dependent tasks. Go exported DTO fields have
lower-camel JSON tags; frontend interfaces use those names. Optional values
are explicit nullable fields, not JSON-encoded error strings.

- `Problem { code, message, path: string }`.
- `DocumentView { id, path, name, title, kind, state, body, revision: string }`.
- `TreeEntry { id, path, name, kind: string }`, where folder `id` is `folder:<path>`
  and document `id` is its actual Recall ID; `kind` is `folder`, `note`, or `task`.
- `TreeSnapshot { session: string; entries: TreeEntry[]; diagnostics: Problem[] }`.
- `DocumentReply { document: DocumentView|null; problem: Problem|null }`.
- `TreeReply { tree: TreeSnapshot|null; problem: Problem|null }`.
- `SaveBodyInput { session, id, expectedRevision, body: string; generation: number }`.
- `SaveBodyReply { document: DocumentView|null; generation: number; saved: boolean;
  problem: Problem|null; indexingWarning: Problem|null }`.
- `CreateInput { session, kind, parent, name: string }` (folder parent `""` means root).
- `CreateReply { entry: TreeEntry|null; document: DocumentView|null; problem: Problem|null }`.
- `DeleteInput { session, id, path, expectedRevision: string; recursive: boolean }`.
- `OperationReply { problem: Problem|null }`.
- `SearchInput { session, query, mode: string; limit: number }`, mode `hybrid` or `keyword`.
- `SearchHit { id, path, title, snippet, kind, state: string }`.
- `SearchReply { hits: SearchHit[]; problem: Problem|null }`.
- `CollectionReply { session, root: string; problem: Problem|null }`.

Go uses `uint64` for frontend generations, validated as safe nonnegative JS
integers at the bridge. `Service` methods are Wails-bindable, use their owned
session context, and return DTO replies (not `model.Document.Raw`). Bind only
the facade and `Host` methods explicitly listed below. Runtime event payloads
always include the collection session token; tokens change on collection switch.

### Task 1: Prove the packaged Wails integration

**Files:** Create `desktop/{go.mod,go.sum,wails.json,main.go,dispatch.go,dispatch_test.go,host.go,host_test.go,assets.go}`, frontend tooling files, and `scripts/build-desktop.sh`; modify `.gitignore` for desktop build/node/generated outputs. Initial frontend is a minimal host fixture, not the finished UI.

**Interfaces:** `dispatch(args []string, stdin io.Reader, stdout,stderr io.Writer, runWorker func([]string,io.Reader,io.Writer,io.Writer) int) (handled bool, code int)` routes only a leading `_embedding-worker` to `cli.Run`; normal launch calls Wails. `Host.BeforeClose(ctx context.Context) bool` synchronously cancels normal close and emits `desktop:quit-requested` with `{token: string}`. `Host.CompleteQuit(token string) OperationReply` authorizes a matching request and invokes the injected quit function; unauthorized/stale tokens cannot terminate. `Host.Ready()` marks the frontend able to handle quit events. `Host.OpenExternal(url string) OperationReply` validates http/https before the injected native browser opener; test blocked schemes now so Task 5 can consume it. The final host cleanup is added in Task 8. Declare the shared `Problem`/`OperationReply` DTOs in `internal/desktop/types.go` now if needed by the host; complete them in Task 2.

- [ ] **Step 1: Write failing host tests.** `TestWorkerDispatchBeforeGUI`: worker and guardian environments reach the CLI dispatcher unchanged, streams/descriptors preserved, no GUI callback invoked. `TestBeforeCloseRequiresHandshake`: close returns cancellation, issues one token for repeated requests, and only matching completion enables quit. A pre-Ready close with no editor can exit without waiting for JavaScript.
- [ ] **Step 2: Run red.** `go test ./...` in `desktop/`; expect missing dispatch/host symbols. Establish the nested module and minimal test harness only as needed to run tests.
- [ ] **Step 3: Implement minimal host.** Select/pin an available stable Wails v2 release after inspecting its actual options/runtime APIs; pin React/TypeScript/Vite/test dependencies and lockfiles. Check `OnBeforeClose`, event delivery, menu Quit behavior, dev origin, CSP allowances, `-tags`/module-root build behavior rather than relying on guessed documentation. Configure frontend build and embed `frontend/dist`; keep generated assets ignored. Do not modify existing worker protocol. Add a fixture frontend with simulated pending-save acknowledgement so the asynchronous close gate is observable.
- [ ] **Step 4: Verify green and compatibility gate.** Run `npm ci` and `npm run build` in `desktop/frontend/`, then `go test ./...` in `desktop/`, then `sh scripts/build-desktop.sh`. Use an isolated temporary HOME/root and the packaged executable's worker entry: verify invalid worker arguments terminate without opening a window; test real manager/guardian inference with verified local fixture assets when available. Manually launch the fixture app and close it during a delayed acknowledgement; it must remain open until allowed. Record actual Go/Wails/Node versions, tested macOS minimum, resource location, and any unrun real-asset gate in the plan. If module layout, worker mode, or asynchronous quitting cannot work, stop and report; do not continue building UI or silently switch frameworks.
- [ ] **Step 5: Commit.** `build: establish isolated Wails desktop host and compatibility gate`.

### Task 2: Add the collection facade and revision-safe body saves

**Files:** Create `internal/desktop/{types.go,service.go,service_test.go,documents.go,documents_test.go}` and `desktop/frontend/src/api/{types.ts,bridge.ts,fakeBridge.ts}`. Bind the service in `desktop/main.go`.

**Interfaces:** `NewService(open func(context.Context,config.Config)(Application,error), preferences Preferences) *Service`; `Application` is a narrow interface matching existing `app.App` methods from the spec. Declare `Preferences` now with `LoadRoot() (string,error)` and `SaveRoot(root string) error`; use an in-memory implementation in tests until Task 8. `Service.OpenCollection(root string) CollectionReply`, `Service.OpenDocument(session,id string) DocumentReply`, `Service.SaveBody(input SaveBodyInput) SaveBodyReply`, `Service.Close() error`. Task 1's production host supplies an `app.Open` wrapper returning `Application` (Go function return types are not covariant) and lifecycle context. `bridge` exports `DesktopAPI` with Promise-returning equivalents and a separately typed host API for `ready`, `completeQuit`, and `openExternal`; `fakeBridge` implements the same boundary for tests, never imported in production bundles.

- [ ] **Step 1: Write failing tests.** `TestBodySavePreservesHeaderAndID`: save changed body in LF/CRLF fixtures with comments, custom YAML, task history and unchanged ID; compare header bytes exactly, verify returned revision matches disk, and reject empty/mismatched expected revision. `TestPostWriteIndexFailureAcknowledgesExactSource`: fake `Update` writes then returns indexing error; exact reread adopts revision with `saved=true,indexingWarning!=nil`; failed reread/nonmatching content remains `saved=false`. `TestStaleSessionCannotWrite`: old-session and unknown-document operations cannot mutate either collection. `TestOpenDoesNotInitializeOrLoadModel`: missing/uninitialized root yields actionable failure without creating or downloading.
- [ ] **Step 2: Run red.** `go test ./internal/desktop -run 'TestBodySave|TestPostWrite|TestStaleSession|TestOpen' -count=1`; expect undefined facade/types.
- [ ] **Step 3: Implement the DTO contract.** Parse the original document with `document.Parse`, replace `Parsed.Body`, `document.Encode`, call `app.Update` using ID and expected revision. On ambiguous error reread and compare exact encoded source, not body alone. Serialize source mutation against collection switching/closure; never hold a global mutex during UI callbacks. Return structured public errors. Keep full raw source in Go only. Reject stale sessions before acquiring a collection reference. Preserve direct-open root configuration without implicit initialization.
- [ ] **Step 4: Run green.** Repeat targeted tests and `go test ./internal/app ./internal/document ./internal/desktop`. Run `npm run typecheck` in `desktop/frontend/`; fixture bridge and Go bindings must agree on names/nullability. Add scripts for `typecheck` and tests if not established by Task 1.
- [ ] **Step 5: Commit.** `feat: add desktop collection facade and revision-safe body saves`.

### Task 3: Expose lightweight tree discovery and safe CRUD

**Files:** Create `internal/desktop/{tree.go,tree_test.go}`; extend facade types/bridge only within the shared contract.

**Interfaces:** `Service.Tree(session string) TreeReply`, `Service.Create(input CreateInput) CreateReply`, `Service.Delete(input DeleteInput) OperationReply`. Produces sorted flat entries plus diagnostics, including empty folders; source identity is never derived from a truncated title. Create uses parent selected by the UI; Go independently validates parent/path/name and protected root.

- [ ] **Step 1: Write failing tests.** `TestTreeHasManagedEntriesAndEmptyFoldersOnly`: typed notes/tasks and empty directories present, ordinary Markdown not adopted, discovery DTO contains no body/raw content. `TestTreeUnicodeAndDuplicateBasenames`: `Résumé`, emoji, HTML-like filenames and duplicate `ideas.note.md` under two folders have distinct exact identities and deterministic folder-first ordering. `TestCreateAndDelete`: create folder/note/task at root/nested parent, note/task suffixes and Planned default preserved, duplicate/traversal/symlink names rejected, document deletion requires current revision, folder deletion requires explicit recursive flag, root deletion rejected. `TestDiagnosticsSurviveDiscovery`: malformed files appear as diagnostics without repair or valid selectable document impersonation.
- [ ] **Step 2: Run red.** `go test ./internal/desktop -run 'TestTree|TestCreateAndDelete|TestDiagnostics' -count=1`; expect missing discovery/CRUD methods.
- [ ] **Step 3: Implement tree projection and CRUD.** Delegate to existing app methods; friendly names strip only the exact type-bearing filename suffix. Preserve diagnostic paths, avoid title-based identity, and validate deletion references on the Go side. If a mutation succeeded but indexing failed, refresh actual source state and expose a warning instead of inviting duplicate creation/deletion.
- [ ] **Step 4: Run green.** Repeat targeted tests and `go test ./internal/app ./internal/desktop`.
- [ ] **Step 5: Commit.** `feat: expose desktop tree and collection CRUD`.

### Task 4: Implement the autosave coordinator and guarded workspace

**Files:** Create frontend `saving/{coordinator.ts,coordinator.test.ts}` and `navigation/{workspace.ts,workspace.test.ts}`.

**Interfaces:** `Clock` exposes `setTimeout(callback:()=>void,delayMs:number):unknown` and `clearTimeout(handle:unknown):void`. `SaveCoordinator(api: Pick<DesktopAPI,'saveBody'>, clock: Clock)` provides `open(session:string,doc:DocumentView):void`, `edit(body:string):void`, `setComposing(active:boolean):void`, `flush():Promise<void>`, `retry():Promise<void>`, `snapshot():SaveSnapshot`, `subscribe(listener:(state:SaveSnapshot)=>void):()=>void`, `dispose():void`. `SaveSnapshot` contains `status: 'saved'|'dirty'|'autosaving'|'error'|'conflict'`, `body:string`, `generation:number`, `busy:boolean`, `problem:Problem|null`, `indexingWarning:Problem|null`. `Workspace(api,coordinator)` provides `selectTree(entry:TreeEntry):Promise<void>`, `openDocument(id:string):Promise<void>`, `setMode(mode:'preview'|'edit'):void`, `beginSearch():void`, `clearSearch():void`, `reloadCurrentAfterConfirmedDiscard():Promise<void>`, `flushBefore(action:()=>Promise<void>):Promise<void>` and observable `WorkspaceState` with separate `treeSelection`, `document`, `mode`, `searchActive`. Only workspace owns document replacement; search/refresh cannot reset its draft. Retry cannot force-write conflicts; confirmed reload fetches current source and resets the baseline through `open`.

- [ ] **Step 1: Write failing fake-clock tests.** `shows requested timing`: edit→999ms dirty/no write→1000ms Autosaving/no write→2999ms no write→3000ms one write→Saved only after resolved promise. `resumed typing resets both timers`: edit at t=2000 clears Autosaving; it reappears at t=3000 and writes at t=5000. `flush serializes and drains latest generation`: timer-save in flight, type again, immediate flush waits then saves new body with acknowledged revision; no overlap, no false green. `failed and conflicting saves preserve body and reject navigation`: no retry loop; explicit retry uses correct current baseline only when safe. `switch and stale completion`: document A cannot mark B Saved; old load results and old-session responses ignored. `preview and search preserve timers`: neither transition resets/disposes the draft. `folder selection retains open editor`: creation/deletion selection changes without replacing document. `composition postpones save`: no timer write during active composition; composition end restarts the quiet interval.
- [ ] **Step 2: Run red.** `npm test -- src/saving/coordinator.test.ts src/navigation/workspace.test.ts` in frontend; expect missing modules/exports.
- [ ] **Step 3: Implement the coordinator and workspace contracts.** Maintain a generation and a single write queue per active document. A resolved write updates the baseline revision even if new edits arrived; status uses the current generation. Flush joins existing work and loops until the latest generation is acknowledged; do not replace a dirty draft on watch events. Workspace locks transitions and rejects them on flush failure; the UI disables editing during final transition flush. Expose explicit conflict reload only after confirmed discard, and a copy-draft path rather than force overwrite. Hold no long-running search in the save queue.
- [ ] **Step 4: Run green.** Run the two suites and `npm run typecheck`; no real timers, network, or filesystem in these tests.
- [ ] **Step 5: Commit.** `feat: implement debounce autosave and guarded document navigation`.

### Task 5: Build Markdown preview/edit and the saved indicator

**Files:** Create frontend `document/{DocumentPane.tsx,DocumentPane.test.tsx,MarkdownPreview.tsx,MarkdownPreview.test.tsx}`; add `app.css` document/status rules and approved editor dependencies.

**Interfaces:** `DocumentPane({workspace,coordinator})` subscribes to existing state; `MarkdownPreview({body:string})` renders only body Markdown. CodeMirror's document is controlled by the draft coordinator, but its editor instance/undo history survives preview toggles. The pane emits composition changes through `setComposing`.

- [ ] **Step 1: Write failing component tests.** `preview renders Markdown without YAML`: headings, task lists, lists, links, tables, code fences, blockquotes render semantically. `unsafe content is inert`: scripts, inline handlers, javascript/data/custom URLs, raw HTML, remote/relative images, and arbitrary local-file links cannot execute/load/navigate; images produce descriptive placeholders. `mode toggle retains draft undo`: pending content appears in preview and switching back preserves editor state. `save indicator is truthful`: amber Unsaved changes/Autosaving, green Saved, red Save failed, text and live-region labels accompany color; indexing warning is independent. `IME and shortcut`: composed text is not truncated by controlled updates, and global ⌘F does not insert a character or destroy a composition draft.
- [ ] **Step 2: Run red.** `npm test -- src/document` in frontend; expect missing components.
- [ ] **Step 3: Implement the document pane.** Use CodeMirror 6 Markdown mode without loading remote language bundles. Use `react-markdown`, GFM and sanitization; do not enable raw HTML. Provide a strict URL policy; external http/https links call only the host's validated browser-opening method. Do not render image `src` attributes. Use CSS hiding/persistent instances for undo continuity. Render previews from the current draft, never an earlier saved body.
- [ ] **Step 4: Run green.** Repeat component tests, `npm run typecheck`, and `npm run build`. Verify resource requests are local in the later security browser suite.
- [ ] **Step 5: Commit.** `feat: add Markdown editor preview and truthful save status`.

### Task 6: Build the shell, animated tree, and create/delete flows

**Files:** Create frontend `App.tsx`, `App.test.tsx`, tree components/tests, and complete `app.css`; wire `main.tsx` to real bridge in production and fake bridge only under test mode.

**Interfaces:** `Tree({entries,selection,expanded,onSelect,onToggle})`; expansion is a session-owned `Set<string>` separate from source entries. `CreateDialog` returns `{kind,parent,name}` to `api.create`; `DeleteDialog` returns confirmation to `api.delete`. App uses workspace guards for opening/creating another document or deleting dirty source/subtrees. New document creation opens Edit; folder creation changes selection only.

- [ ] **Step 1: Write failing tests.** `tree defaults closed and survives refresh`: folder rows start `aria-expanded=false`, toggles reveal children, discovery refresh preserves session expansion. `separate selection determines creation parent`: folder selection creates inside, file selection alongside, none at root; existing open draft remains when selecting folder. `deletion targets active selection`: flush affected editor before confirmation, cancel is non-destructive, protected root disabled, selected folder confirmation explicitly says permanent recursive deletion, stale-revision failure keeps draft. `new documents and folders`: filename validation feedback, duplicate-name errors, task Planned default, document opens Edit, folder selected/revealed. `accessible collapse`: hidden children unfocusable, keyboard arrows/Enter work, focus returns to trigger after dialogs; reduced motion removes transitions. `stale load after rapid selection`: only latest successful guarded selection becomes visible.
- [ ] **Step 2: Run red.** `npm test -- src/tree src/App.test.tsx` in frontend; expect missing tree/shell behavior.
- [ ] **Step 3: Implement the approved shell.** Match v7 mockup's layout/copy using semantic controls and actual macOS native window chrome (not drawn traffic-light substitutes). Single-row folder click selects/toggles, accessible chevron state, ~200ms interruptible CSS transitions, retained DOM or equivalent stable animation. Show diagnostics without editable malformed files; deterministic escaped filenames, note/task icons, friendly base names. Wire plus/trash and document pane through existing workspace/coordinator, not direct local sample mutations. Cancel deleted document timers and clear affected editor only after successful deletion.
- [ ] **Step 4: Run green.** Repeat suites, `npm run typecheck`, and browser-test fixture build. Manually inspect tree/animation fidelity; do not claim a native visual check if only browser tests ran.
- [ ] **Step 5: Commit.** `feat: build animated desktop sidebar and file operations`.

### Task 7: Connect global search with explicit semantic errors

**Files:** Create `internal/desktop/{search.go,search_test.go}` and frontend `search/{Search.tsx,Search.test.tsx}`; integrate App.

**Interfaces:** `Service.Search(input SearchInput) SearchReply`; frontend `Search({api,workspace,session})` owns a query-generation token and 250ms query debounce. Go limits results to 50 maximum, default 20, requires supported mode, strips full/raw source from hits. Expose loading/no results/errors and explicit keyword retry. Search results reveal ancestor folders before opening selected source.

- [ ] **Step 1: Write failing tests.** Go `TestSearchDelegatesModesAndErrors`: hybrid is requested by default, keyword never starts inference, indexing/runtime-unavailable returned as structured errors without fallback, stale session rejected. Frontend `search debounce and stale replies`: 249ms no call, 250ms one call, empty query none, latest query wins; explicit keyword retry changes mode visibly. `result navigation flushes`: failed save prevents opening result, success reveals ancestors and uses document ID. `shortcut and escape`: ⌘F focuses/selects global Search from tree/editor; Escape restores existing draft and does not change save timers; typing/composition events do not spuriously trigger shortcuts.
- [ ] **Step 2: Run red.** `go test ./internal/desktop -run TestSearch -count=1` and `npm test -- src/search` (in frontend); expect undefined adapter/UI behavior.
- [ ] **Step 3: Implement the search boundary and UI.** Delegate `app.Search(model.SearchQuery)`; no duplicated retrieval implementation. Escape/clear restore the open-document state rather than issuing a reread. Maintain separate search error/loading state from save status. Do not log query/body content.
- [ ] **Step 4: Run green.** Repeat targeted suites, root `go test ./internal/search ./internal/desktop`, and frontend `npm run typecheck`.
- [ ] **Step 5: Commit.** `feat: connect global Recall search and explicit keyword retry`.

### Task 8: Integrate watchers, collection preferences, and safe native shutdown

**Files:** Create `internal/desktop/{watch.go,watch_test.go,preferences.go,preferences_test.go}` and frontend `{shutdown.ts,shutdown.test.ts}`; extend existing service/workspace/host tests.

**Interfaces:** `Service.SetEventSink(func(name string,payload any))`; events `desktop:collection-changed` and `desktop:collection-warning` carry `{session,...}`. `Service.Refresh(session string) TreeReply` reconciles on activation. `Preferences.LoadRoot() (string,error)`, `Preferences.SaveRoot(root string) error` store only an explicit root under `os.UserConfigDir()/recall/desktop.json`, with injectable directory in tests. `Host.ChooseCollection() (string,error)` is a native directory picker, returning a path without opening it. `Host.OpenExternal(url string) OperationReply` independently allows only http/https. `registerShutdown(host,workspace,coordinator):()=>void` handles quit tokens, freezes edits/transitions, flushes, and calls `Host.CompleteQuit(token)` only on success. Host invokes `Service.Close()` once before runtime Quit.

- [ ] **Step 1: Write failing tests.** `TestWatchLifecycle`: one syncer per session, coalesced refresh event, owned indexing queue never overlaps itself, cancel/wait on switch/close, previous-session events ignored. `TestRootPreferences`: explicit picker choice survives Finder-like launch with no RECALL_HOME; command-line root > saved explicit choice > env/default; invalid preference/config errors visible, no implicit collection setup. `dirty external edit conflict`: watcher never replaces dirty body; clean moved document follows ID; externally deleted/malformed file preserves draft for copying; explicit confirmed reload resets baseline, no force overwrite. `shutdown drains or refuses`: repeated close/menu Quit emits one pending request; delayed write keeps window alive; failure/conflict leaves app open and editable with retry/copy; successful flush closes service once and quits once. `collection switch flush and epoch`: save failure blocks picker-selected switch; success cancels old watcher and ignores late events. `activation refresh`: native focus gain schedules refresh without resetting draft or expansion.
- [ ] **Step 2: Run red.** `go test ./internal/desktop -run 'TestWatch|TestRoot' -count=1`; `go test ./...` in desktop after frontend build; `npm test -- src/shutdown.test.ts src/navigation/workspace.test.ts` in frontend. Expect missing lifecycle wiring.
- [ ] **Step 3: Implement lifecycle and conflict recovery.** Reuse `syncer.Run(ctx,cfg.Content,reconcile,report)` with owned cancellation/wait group; use one serialized pending-index runner woken by relevant changes, not unbounded parallel calls. Refresh lightweight data; reread active source by ID only for clean reload/conflict detection and never silently replace a dirty draft. Root changes flush before `OpenCollection` and persist only a successful explicit choice. Wire macOS focus and menu/close paths to Task 1's handshake; if current Wails APIs cannot deliver focus directly, add the smallest native adapter and test it rather than a periodic full-collection UI poll. Pause autosave on conflict and expose copy-draft plus confirmed reload. Window close quits normally; forced OS termination remains outside guarantee.
- [ ] **Step 4: Run green.** Repeat targeted suites and all frontend/root facade tests. In native app, edit→immediately ⌘Q and edit→window close: reopened source contains exact latest text. With an injected failing writer, repeated Quit must not close the app. Verify runtime navigation policy/CSP permits bundled Wails bindings but no remote page can obtain Go bindings.
- [ ] **Step 5: Commit.** `feat: integrate collection refresh preferences and save-before-quit`.

### Task 9: Package, secure, and verify the desktop release candidate

**Files:** Complete `scripts/{build-desktop.sh,verify-desktop.sh}`, `desktop/build` resources as required, frontend Playwright config/browser suites, `docs/desktop.md`; update `README.md` and `docs/operations.md`. Per the user's execution-time request, README must contain desktop installation instructions directly, not just a guide link. Do not change CLI distribution scripts unless a demonstrated shared-asset requirement needs a separately regression-tested adjustment.

**Interfaces:** `sh scripts/build-desktop.sh` produces `build/Recall.app` from pinned local dependencies and frontend assets; `sh scripts/verify-desktop.sh` validates that bundle with an isolated temporary HOME/root and cleans up only owned processes/files. Native smoke exercises real Go bindings, unlike browser acceptance suites. Record evidence and limitations in `docs/desktop.md`.

- [ ] **Step 1: Write failing acceptance checks.** `workspace.spec.ts`: tree default/animation, create note/task/folder, real UI debounce with fake bridge clock, preview/edit, save indicators, ⌘F search, root selection, safe deletion, conflict and quit-request simulation. `security.spec.ts`: authored HTML/URLs and filenames cannot execute, no network requests for remote images or Markdown dependencies, no generic filesystem bridge. Packaging verifier checks bundle architecture, relocated paths with spaces, frontend assets/CSP, no source/sample fixtures in runtime assets, no accidental model download, and worker launch never opens a second window.
- [ ] **Step 2: Run red.** `npm run test:e2e` in frontend and `sh scripts/verify-desktop.sh`; expect missing acceptance artifacts/check failures, not failures from accessing real HOME. Playwright is for browser fixtures only; mark native checks separately.
- [ ] **Step 3: Implement packaging and acceptance fixtures.** Embed production frontend with strict CSP and no CDN assets; exclude fake bridge from production. Bundle a normal signed-identity-ready `.app` without claiming signing/notarization. Runtime/model discovery remains collection-backed; preserve CLI init/archive assets and existing packaging behavior. Document actual tested macOS/Wails/Go/Node versions, unsigned approval flow without disabling Gatekeeper, custom root/Finder environment, body-only editing, conflict recovery, 1s/3s timing, no guaranteed crash saving, watcher CPU limitation, developer commands, and deferred features. Include `docs/desktop.md` in user-facing guide links.
- [ ] **Step 4: Run full verification.** At root: `go test ./...`, then `go test -race ./internal/desktop`. In frontend: `npm ci`, `npm run typecheck`, `npm test`, `npm run test:e2e`, `npm run build`. In desktop: `go test ./...`; at root: `sh scripts/build-desktop.sh`, `sh scripts/verify-desktop.sh`. Run existing `sh scripts/verify-package.sh` when packaged CLI assets are available; distinguish missing real-asset gates from passing tests. Native smoke must save/reopen Unicode Markdown, exercise real keyword/hybrid search against owned fixtures, create/delete nested folders, detect concurrent CLI edits, test immediate quit flush, and observe no owned GUI/watcher after exit; shared embeddings may remain for the existing 120s idle policy. Record commands/results, not an invented performance SLA. Run `git diff --check` and inspect the complete diff.
- [ ] **Step 5: Commit.** `feat: package and verify macOS Recall desktop app`.

## Plan self-review

- [x] Coverage: platform/worker/quit compatibility (1); metadata/source-save safety
  (2); tree and backend CRUD (3); exact timers/flush/navigation (4); rendering,
  editing/status/security (5); approved animated layout/accessibility/CRUD (6);
  global search (7); watcher/conflict/root/lifecycle (8); packaging/docs/native
  verification (9). No spec requirement intentionally omitted.
- [x] Signatures: shared replies are declared before UI consumers; generations
  and sessions are carried through saves/events; pure root facade is separated
  from Wails host and frontend bridge.
- [x] Review Focus conditions have explicit owning tests; fake-browser/native
  checks are distinguished; source-write/indexing failures get separate tests.
- [x] Steps are independently checkable red/implementation/green/commit cycles;
  dependency choices stay conditional on the first integration gate rather than
  pretending Wails/macOS compatibility has already been demonstrated.
- [x] Scope remains one desktop app; existing Recall interfaces are regression
  constraints, not separate projects. No implementation code is included here.

## Execution handoff

Review this plan before implementing. Recommend **subagent-driven** execution:
there are nine reviewable tasks, and a mistake at the draft/revision/quit boundary
can lose user edits. Preserve the shared interface contract in every task brief.
Native inline execution is also available if lower overhead is preferred.
At execution start, create/verify an isolated worktree using the worktree skill,
then load the chosen execution skill. Do not implement before the user chooses.
