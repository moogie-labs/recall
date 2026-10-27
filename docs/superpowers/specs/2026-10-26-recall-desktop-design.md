# Recall desktop for macOS

Status: approved and implemented; final whole-branch review pending.

## Intent

Add a focused desktop interface to Recall: find notes and tasks, browse their
folders, read rendered Markdown, and edit in place. Preserve the existing
local-first collection and CLI/TUI/MCP behavior rather than creating a second
storage system. The user approved the interactive layout and chose macOS and
Wails, and subsequently approved the complete design before planning.

## Decisions agreed with the user

- macOS-only desktop application, built with Wails and the existing Go backend.
- A global search bar labeled **Search**, focused by **⌘F** (not ⌘K).
- A collapsible left sidebar headed **Recall**, with a basic file tree.
- Folders start closed, have visible expand/collapse chevrons, and animate open
  and closed. The sidebar also animates. Respect reduced-motion preferences.
- Selecting a document opens it in the main pane. A top-right Preview/Edit
  toggle switches between rendered Markdown and Markdown source editing.
- A plus dropdown offers Folder, Note, and Task. With a folder selected, create
  inside it; with a document selected, create alongside it.
- A trash icon next to plus deletes the selected item.
- Autosave is measured from the most recent edit: initially show an unsaved
  state, after one quiet second show **Autosaving**, and after three quiet
  seconds perform the source write. Show **Saved** only after successful saving.
  Further typing clears Autosaving and resets both timers. Three seconds, not
  five, is the final timing agreed through the user's explicit examples.
- Include a saved-indicator light, accompanied by text rather than color alone.
- Switching documents and quitting flush pending edits immediately.

The throwaway interactive reference is
`.superpowers/brainstorm/16345-1793069068/content/desktop-layout-v7.html`.
It is ignored by git; this specification is the durable behavioral reference.
The mockup has a warm neutral sidebar, green selection treatment, native-style
window chrome, centered search, breadcrumb/document toolbar, and a slim status
footer. Its sample storage and simplified Markdown parser must not be reused
as production implementations.

## Scope and non-goals

One collection and one main window with one active document. No tabs, split
editor, drag-and-drop organization, rename/move controls, task dashboard,
account, sync service, generated answers, plugins, or automatic updates in v1.
Task creation and reading/editing task bodies are included; dedicated task
state/history editing is deferred. Existing CLI/MCP task operations remain.
No always-on desktop service, launch agent, public HTTP listener, or Electron.

## Architecture recommendation

The user chose Wails; the following implementation details are the author's
recommendations, not separately user-selected technologies:

- Use stable Wails v2, a TypeScript frontend with React, CodeMirror 6 for plain
  Markdown editing, and a maintained CommonMark/GFM renderer with sanitized
  output. Validate current toolchain compatibility before pinning dependencies.
- Add an independent desktop entry point (proposed `cmd/recall-desktop`) and a
  thin desktop adapter (`internal/desktop`). Keep `cmd/recall` and its default
  TUI invocation unchanged.
- Bind a narrow typed facade over `internal/app`; do not shell out per edit,
  expose generic filesystem access, or copy application rules into JavaScript.
- Use existing `app.Open`, `List`, `Read`, `Create`, `CreateFolder`, `Update`,
  `Delete`, `DeleteFolder`, `Search`, `Sync`, and `IndexPending` services.
  Bound discovery responses should use desktop-specific lightweight summaries,
  not ship `model.Snapshot` with every document body to the renderer. Fetch a
  full document only on selection. Do not change CLI/MCP output contracts.
- All backend work runs off the UI thread. Serialize saves per document and
  use generation tokens for loads/searches so stale results cannot replace
  newer selections or queries.
- Before any Wails initialization, the desktop executable recognizes the
  existing `_embedding-worker` invocation and hands it to the established CLI
  worker dispatcher. `embedding/client.go` relaunches `os.Executable()`; both
  manager and guardian paths must work headlessly in the packaged executable.
- Reuse the existing singleton embedding lifecycle and private Unix sockets.
  Metadata work and keyword search must not require a loaded model. No GUI
  change should force a shared worker to stop when another client is using it.

### Platform, collection, and lifecycle

Initially target Apple Silicon macOS, matching the shipped native inference
runtime. Intel support is not implied by macOS-only. Determine the minimum
macOS version from tested Wails/editor/runtime compatibility and document it.

Use `config.Resolve`: an explicit root wins over `RECALL_HOME`, then
`~/.local/share/recall`. Provide a native collection-folder picker for a custom
root and remember that explicit choice in desktop preferences, separate from
collection metadata. Finder launches may not inherit shell environment values.
Never silently create or initialize a missing collection. Show an actionable
setup screen with the matching `recall --root … init` instruction; an in-app
model download/setup wizard is deferred. Package bootstrap assets so the CLI's
existing distribution remains functional; desktop startup must not download.

Closing the main window saves and exits rather than retaining a hidden
background app in v1. Use asynchronous Wails close/quit interception: await
successful flush, stop owned watcher/index tasks, close the application service,
then complete termination. A failed flush cancels termination. Force Quit,
crashes, and power loss cannot guarantee saving; do not claim otherwise.

## Interaction details and author-proposed defaults

### Tree and selection

- Build the tree from Recall's managed documents and folders, including empty
  folders, not arbitrary `.md` files. Display friendly base filenames, with
  note/task icons; retain full type-bearing paths as identity. Sort folders
  first, then names using a consistent case-insensitive ordering.
- Folders start collapsed on each launch; expansion persists during refreshes
  within a session. Opening a search result reveals its ancestor folders.
- Folder row click selects and toggles expansion. Folder selection does not
  discard the currently open document; track active tree selection separately
  from open document. Create/delete always target the active tree selection.
- With no tree selection, create at collection root and disable trash. The
  collection root itself is not deletable. A create dialog requests a name;
  use the existing naming/validation rules and never overwrite duplicates.
- After creating a document, reveal it and open it in Edit; new tasks default
  to Planned. After creating a folder, select/reveal it without opening a file.
  Existing documents open in Preview. Preserve draft/undo across preview toggles.
- Confirm file deletion and recursive folder deletion, explicitly describing
  permanent deletion, not the macOS Trash. Flush any dirty document affected by
  deletion before confirmation and stop if that fails. On success cancel stale
  timers/jobs for deleted documents; deleting a containing folder clears the
  current document. Unrelated deletion leaves the current editor unchanged.
- Keyboard-accessible tree, toggles, menus, dialogs, focus indicators, and
  descriptive expand/collapse labels are required. Hidden rows cannot receive
  focus. Animations should be short (roughly 200 ms) and interruptible.

### Search

- Search the whole collection through Recall's hybrid engine; debounce queries
  by about 250 ms, suppress empty-query searches, and ignore late responses.
- Show results in the main pane with title, path, and passage previews. Choosing
  one flushes the active draft first and then opens the actual document.
- Starting/clearing search must not destroy the draft. Esc clears search and
  restores the open document. ⌘F focuses/selects the global search input even
  while editing; it deliberately replaces the standard local find shortcut.
- Show loading, no-result, indexing, and runtime-unavailable states explicitly.
  Offer an explicit keyword-only retry when semantic search is unavailable;
  never silently substitute keyword results for hybrid results.

### Editing and preview

Edit the Markdown body, not raw YAML frontmatter. On the Go side, parse the
revision originally read, change only `document.Parsed.Body`, encode with the
existing codec, and call `app.Update` with the expected revision. Preserve IDs,
titles, task state/history, unknown metadata, and unchanged header bytes.
Changing a Markdown heading does not implicitly rename the file or its title
metadata. The first release does not expose a separate metadata editor.

Preview renders headings, paragraphs, lists, task lists, links, fenced code,
tables, and blockquotes without displaying YAML. No executable raw HTML,
scripts, remote embeds, or automatic remote image loads. Use a restrictive
content security policy. External http/https links may open in the system
browser on explicit click; block unsafe/custom schemes. Attachment loading and
relative-link navigation are deferred; render unavailable images as descriptive
placeholders rather than permitting arbitrary filesystem reads.

### Autosave state machine

1. Every body edit increments a draft generation, marks dirty, changes the light
   to amber with **Unsaved changes**, and resets both idle timers.
2. At one second without typing, change text to **Autosaving**, remaining amber.
   This is the requested pending-save message, not a claim that disk I/O began.
3. At three seconds without typing, write the latest draft. Keep Autosaving
   until the request succeeds; never show Saved solely because a timer fired.
4. On success record the new revision. Green **Saved** is valid only if the
   acknowledged generation is still current. If newer edits exist, retain dirty
   status and their timer schedule, using the new revision for the next save.
5. Saves are serialized per document; timer-triggered and immediate flushes
   share the same coordinator. A completion for document A cannot mark B saved.
6. Navigation to another document, custom-root changes, closing, and quitting
   await an immediate flush of all pending edits and any in-flight save. Recheck
   the latest generation before completing navigation. Disable editing briefly
   during final navigation/quit flush so no final keystrokes are lost.
7. Preview toggles do not force a write or reset the debounce: preview the latest
   draft. Search interaction also preserves the draft and pending timers.
8. Errors produce a red light and **Save failed** with an actionable message;
   preserve the draft, allow retry/copy, and block destructive navigation/quit.
   Never automatically discard the buffer or overwrite a conflicting source.

Source-write success and indexing success are different. `app.mutate` can
return an error after the source write if reconciliation/indexing fails. On
ambiguous failure, reread and compare the actual source with the intended
encoded content. If the exact intended source was written, adopt its revision,
acknowledge the saved generation, and separately display an indexing warning.
Otherwise retain the dirty draft; do not blindly retry with an obsolete revision.
Semantic embeddings need not finish before a source is Saved.

### External changes and conflicts

Reuse the collection syncer only while the desktop app is running, plus refresh
on activation and after local mutations. Keep backend callbacks marshaled to the
UI safely. Preserve tree expansion and drafts during refreshes. Avoid continuous
full-document transfers or redundant indexing calls; the existing Darwin
polling watcher has a documented large-collection CPU limitation, not a new
performance guarantee.

For clean documents, reload external changes. For dirty documents, keep the
draft and use expected-revision checks; never overwrite external changes or
replace a draft with watcher output. On conflict pause autosaving, show a
conflict state, and offer copying the draft and explicitly reloading the current
file (with a discard confirmation). No automatic merge or force-overwrite in v1.
Externally moved documents are tracked by ID; externally deleted or malformed
documents show an error and retain any draft for copying rather than recreating
the source automatically. Expose scanner diagnostics without trying to repair
malformed files from the desktop editor.

## Packaging, security, and verification

Ship `Recall.app` with bundled frontend assets, no CDN dependencies, and the
existing verified inference integration. Keep existing CLI distribution and
asset-layout assumptions working. Document the developer prerequisites added
by Wails (Node/package manager and Xcode command-line tools), build commands,
installing/launching the app, custom-root behavior, supported macOS version,
and unsigned-build limitations. Signing/notarization and publication need owner
credentials/approval and are not performed as part of this feature.

Before building the full UI, the implementation plan must include an integration
gate for a minimal packaged Wails app: headless worker/guardian dispatch, Go
version compatibility, resource location, and asynchronous quit interception.
If those fail, report the blocker rather than silently changing frameworks.

Required verification:

- Existing Go tests and CLI/MCP regressions, using isolated roots/HOME.
- Backend body-only saves preserve metadata and enforce revisions; create/delete
  selection rules, empty folders, duplicate names, path confinement, diagnostics.
- Fake-clock frontend tests for 1s/3s timing, resumed typing, edits during saves,
  stale completions, errors, retry, switching, closing, and immediate flushes.
- Concurrent CLI/MCP edits, post-write indexing failure, deleted/moved sources,
  and watcher refreshes must not lose drafts or falsely report Saved.
- UI tests for preview/edit, sanitized Markdown, ⌘F, tree and sidebar animation,
  reduced motion, focus, dialogs, result navigation, and error states.
- A packaged macOS smoke test using a temporary initialized collection, real
  save/reopen, search, tree CRUD, and quit flush. Verify a worker launch never
  creates an extra GUI window and closing the app does not leave an owned
  watcher or GUI process running. Shared inference may remain until idle timeout.
- Never use or modify the owner's actual collection for automated validation.

## Design workflow checklist

- [x] Explore the existing application and collection APIs.
- [x] Capture agreed behavior and label author-selected defaults.
- [x] Fresh builder review; no important unanswered questions identified.
- [x] Present the reviewed design for user approval; user approved.
- [x] After approval, write the implementation plan; implementation followed approval.

Details left to the builder: component names and frontend folder organization,
dependency versions after compatibility checks, exact typography/spacing close
to the mockup, and test harness selection. Changes to scope, saving safety,
framework, or source authority require returning to the user.
