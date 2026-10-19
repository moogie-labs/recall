# Recall: first-version design

Date: 2026-10-18  
Status: Approved by the owner for implementation planning on 2026-10-18; execution awaits implementation-plan review.

## 1. Purpose

Recall is a local, Go-based terminal application for keeping knowledge and unfinished work from disappearing. It brings scattered notes and tasks into one central, folder-organized collection that both a person and coding agents can use.

Success means being able to capture something quickly, find it later without remembering its exact wording, and inspect unfinished work by state and date. Recall should help organize work without becoming another elaborate system to maintain.

## 2. Agreed requirements

- The application is named **Recall** and is implemented in **Go**.
- Storage is centralized on one computer. Running Recall inside another project must still use the same Recall collection.
- Folders are shared containers: each can contain subfolders, notes, and tasks.
- Notes and tasks are real files. Compliant filenames declare whether each is a note or task; frontmatter supplies matching type information and structured metadata; Markdown holds the content.
- Automatic adoption is limited to files with compliant, type-bearing filenames. Only then may Recall add missing schema fields; it must not guess the type of arbitrary dropped-in Markdown.
- Neovim must be usable to edit both notes and tasks, including direct edits outside Recall.
- Note bodies remain free-form. There are no required note sections or templates.
- Tasks have a title, a description, a current state, and dated state-change history.
- Initial task states are **Planned**, **In progress**, **Stalled**, and **Completed**.
- Tasks can be permanently deleted, including their history. There is no deleted state, trash folder, or application-level soft deletion.
- State changes made directly in Neovim are synchronized. Missing intermediate transitions during rapid saves or offline edits is acceptable; losing the latest document content is not.
- Sorting and filtering by states and dates are sufficient for initially resurfacing work. A reminder system is not required.
- Semantic search runs locally and offline after setup. The initial target machine is an M1 MacBook Pro with 16 GB RAM.
- Onboarding should manage embedding setup: Recall supplies the inference runtime and initialization installs the model weights.
- Existing Ollama installations do not need to be detected or reused.
- Agents have full permissions over Recall's notes, tasks, and folders, including destructive operations.
- The initial task interface is a sortable/filterable list, not a required Kanban board.
- Explicit cross-document links and backlinks are deferred beyond v1.

The remaining sections turn those requirements into a proposed v1 design. Defaults not explicitly chosen in the conversation are identified as recommendations; they are not additional user requirements.

## 3. Scope and non-goals

### In scope

- Folder browsing and management; note and task creation, reading, editing, moving, renaming, and deletion.
- An interactive TUI, a noninteractive CLI, and a local MCP interface over shared application logic.
- Global and folder-scoped search and task filtering.
- Keyword and semantic retrieval with source references.
- Automatic indexing and reconciliation of external file edits.
- Recovering the index from the authoritative files.

### Outside the initial version

- Accounts, cloud synchronization, collaboration, and cross-computer sharing.
- Calendar integration, notifications, recurring tasks, and automatic reminders.
- Generated answers or a conversational assistant. Retrieval does not require a chat model.
- Ollama integration or multiple selectable inference backends.
- A built-in rich-text editor, enforced note-body structure, or AI extraction of tasks from prose.
- A separate project entity: folders provide the initial organizational structure.
- Explicit document relationships, relationship editing, backlinks, and unresolved-link diagnostics.
- Kanban columns or a second task-board layout.

**Scope default based on the conversation:** date requirements cover state changes and filtering; no deadline, scheduling, priority, tag, or dependency fields were requested. Those additions and a dedicated import wizard are deferred. State-transition timestamps must not be presented as deadlines or reminders. Users can bring existing Markdown into the content tree directly under the discovery rules below.

## 4. Sources of truth and central storage

The content tree is authoritative. SQLite is derived and rebuildable.

| Information | Authoritative location |
| --- | --- |
| Folder hierarchy and membership | Actual directories and file locations |
| Note and task content | Markdown files |
| Document identity and metadata | Frontmatter |
| Current task state and state-change history | Task frontmatter |
| Selected embedding model and indexing configuration | Recall's configuration file |
| Extracted metadata, keyword index, embeddings, job bookkeeping | Rebuildable SQLite database |

All meaningful document information must survive deleting and rebuilding the database. There must be no database-only task history, document identity, or folder membership.

The central root contains a user-content directory and separate areas for configuration, indexes, model files, and runtime assets. Application assets must not appear as user notes or folders. The content directory allows any user-created folder hierarchy; Recall does not prescribe Inbox, Projects, Reference, or other special folders.

**Recommended discovery rules:** configure one root at initialization, with an explicit root override for testing or advanced use. Never infer the root from the current working directory. The exact default path and override syntax are left to the builder and must be documented before release. If no root is initialized, give an actionable setup message rather than creating a new collection in the current project.

Folder membership is singular and follows the containing directory. Folder rename/move operations update indexed paths while document IDs remain unchanged. Empty folders are valid and do not require marker documents.

## 5. Document format

**Recommended format:** UTF-8 Markdown files with YAML frontmatter. Use the same extension and discovery rules for both document types.

### Filename typing and adoption

**Recommended exact convention:** `<name>.note.md` for notes and `<name>.task.md` for tasks. The filename must have a nonempty name before its type suffix. Recall's creation and rename operations use and preserve these suffixes. Case-sensitive suffix matching is the proposed convention; a case-insensitive host filesystem does not make other suffix spellings compliant.

The suffix declares the document type for discovery. Frontmatter `type` mirrors that declaration and must agree with it. A missing frontmatter type can be filled from a compliant filename; a contradictory declared type is an error, not an automatic conversion. Changing a note into a task or vice versa is not an implicit side effect of indexing; a user must make the filename and metadata consistent explicitly.

Files without a compliant suffix stay untouched and are not automatically adopted, assigned IDs, or included in managed-document search. They can be listed as unmanaged for visibility. To adopt existing Markdown, explicitly rename it to the appropriate compliant suffix, after which Recall may add missing schema fields. There is no need for a separate import wizard in v1.

### Shared metadata

| Field | Meaning |
| --- | --- |
| `type` | `note` or `task` |
| `id` | Stable, automatically generated, opaque document identifier |
| `title` | Optional display title; fall back to the filename without its type suffix and extension |

Stable IDs and the optional title fallback are design recommendations, not fields the user explicitly specified. They let agents and indexes identify documents across path changes without requiring a heading in the Markdown body. Explicit document-link features are not part of v1.

Example note, saved as `design-ideas.note.md`:

```markdown
---
type: note
id: "note-example"
---
Anything useful can go here: rough ideas, links, code, or prose.
```

The examples use readable placeholder IDs. The actual ID generation algorithm is a builder choice; IDs must be unique and stable, not derived from titles or paths.

### Task metadata

Tasks additionally have `state` and `state_history`. The Markdown body is the description, without a required Description heading or other template.

Example task, saved as `migration-guide.task.md`:

```markdown
---
type: task
id: "task-example"
title: Write the migration guide
state: in_progress
state_history:
  - state: planned
    at: 2026-10-17T10:00:00Z
  - state: in_progress
    at: 2026-10-18T09:00:00Z
---
Explain the migration process, including rollback instructions.
```

**Recommended serialized states:** `planned`, `in_progress`, `stalled`, and `completed`. Display friendly labels in the TUI. The exact frontmatter key names and event representation above are proposed conventions, not an already implemented API.

The first history event records the initial state and its timestamp. A task's creation time is derived from that event; its current-state-since time comes from its last event. A separate duplicated creation timestamp is not necessary initially. Description edits do not count as state transitions. Empty descriptions are allowed.

### Discovery, validation, and preservation

**Recommended onboarding-friendly defaults:**

- Discover files with compliant type-bearing suffixes recursively within the content root, excluding editor temporary files and application assets.
- Determine the type only from a compliant suffix, never from prose or unrelated metadata. Do not treat an untyped filename as a note by default.
- When registering a compliant file, add missing `type` and `id` metadata without changing its body. Do not alter noncompliant files automatically.
- For a newly discovered task with missing managed state fields, preserve valid existing information: infer a missing current state from the last valid history entry, or default to planned only if neither state nor history exists. If a valid current state exists without history, create an initial observed history entry for that state. Do not replace malformed existing history with an invented clean history; report the error.
- Surface filename/frontmatter type conflicts, invalid known fields, malformed frontmatter, and duplicate IDs as actionable diagnostics. Do not silently drop, delete, or replace the source file.
- An invalid file remains visible as a file with an error. Search must not silently serve an older indexed version as if it were current.
- Preserve unknown frontmatter fields and preserve the Markdown body when updating application metadata. Avoid unnecessary formatting changes.
- User-authored Markdown links or unfamiliar link metadata remain ordinary content. There is no automatic backlink index or link rewriting in v1.

The owner approved automatic insertion of missing schema fields only after filename compliance establishes a file's type. The `.note.md` / `.task.md` spelling and fallback-title convention were author recommendations included in the subsequently approved design.

## 6. Task lifecycle and dates

Any valid state can transition to any other valid state. There is no mandatory linear workflow. Reopening completed work preserves its earlier history. Setting a task to its existing state is not a new transition.

For operations through Recall, update the current state and append its history event in the same file write. All interfaces use this same behavior.

**Recommended date conventions:** store timezone-qualified timestamps normalized to UTC, display dates in the user's local timezone, and interpret date-only filters as local calendar-day boundaries. Support filtering by:

- Current state.
- Initial recorded/creation time.
- The timestamp of entry into the current state.
- Historical transitions into a specified state within a date range.

Historical-event filtering is explicitly different from current-state filtering. A task that completed last month and later reopened can match a historical completion query without currently being completed.

Sort by title, initial recorded time, or most recent state-change time, in either direction. Folder filtering can include descendants or only direct children; global queries aggregate across all folders.

## 7. Direct editing and synchronization

Neovim edits the real source file, not a temporary database export. Recall should provide an external-editor action for both document types, while independent Neovim sessions work too. **Recommended editor default:** `nvim`, with a configurable editor command.

Use filesystem watching with coalescing plus startup/manual reconciliation. Watching is an optimization, not the sole recovery mechanism: startup must find changes made while Recall was closed.

When a valid task's current state differs from the final state in its saved history, append one observed transition to the saved current state. If an existing history already ends at the current state, do not add a duplicate event.

For externally edited files, timestamps represent when Recall observed the transition, not a claim about the exact time an unobserved edit occurred. Several rapid transitions may collapse into one; transitions away from and back to the recorded state may leave no new event. This is acceptable.

Only intermediate history is allowed to be incomplete. The latest saved description, current state, and unrelated metadata must be preserved. Manually edited history is part of the source file; SQLite must not restore an older hidden copy over it.

The builder must design reconciliation around concurrent edits: serialize Recall-originated writes across processes, reread the current file before metadata updates, and detect/refuse or retry stale updates instead of blindly saving an old snapshot. Direct editors do not participate in Recall's locks, so external-edit coordination and Neovim's changed-on-disk behavior need explicit testing. Do not describe external editing as a fully transactional database interface.

## 8. Deletion

Deleting a task removes its source file, including its body and state history, then removes its current derived index entries and embeddings. Deleting a note behaves equivalently for that note.

Deleting a task does not delete notes or other tasks. Deleting a note does not delete tasks or other notes. User-authored links in remaining documents stay unchanged; there is no built-in relationship model or automatic link cleanup in v1.

There is no application trash, deleted-state marker, content-bearing deletion archive, or soft-deleted source record. This is ordinary deletion from the live collection, not secure disk erasure or deletion from pre-existing backups or agent transcripts.

**Recommended destructive-operation behavior:**

- The TUI asks for one clear confirmation identifying what will be removed.
- CLI and MCP deletion are noninteractive, explicit operations; agents have full permissions and do not need a separate Recall approval workflow.
- A nonempty folder requires explicit recursive-delete intent. It must not be erased as an accidental side effect of deleting an empty-looking folder entry.
- A recursive delete removes the contained task/note files and their derived data, but never follows paths or symlinks outside the content root.
- A stale background indexing job must not resurrect a deleted or replaced document.

## 9. Retrieval and indexing

Use hybrid keyword and semantic search over notes and tasks. Keyword search handles exact names, commands, identifiers, and error text; semantic search handles conceptually related wording. Neither should replace the other.

**Recommended storage:** SQLite with FTS5 for keywords and a validated vector-search extension such as sqlite-vec. The extension is a candidate, not a locked dependency.

Index titles, file paths, document type, task metadata, and appropriate passages of document content. Chunk longer bodies automatically without requiring headings. Support global search and filtering by folder subtree, document type, and applicable task state/date criteria. Date filters in v1 are task-history filters; note-date semantics are not an additional requirement.

Return bounded results containing the stable document ID, source path, title, matched passage, and enough metadata to locate/open the original. Deduplicate or group multiple matching passages from the same document sensibly. Search must not depend on generated answers.

Maintain embeddings incrementally using content hashes and a versioned model/preprocessing signature. Path-only moves should not require re-embedding unchanged content. Recheck source identity and content before publishing background results. Rebuilding the database must recover document metadata and task histories from files and recompute embeddings as needed.

Saving or editing a file must not block on model inference. During a pending update, keyword search and metadata filtering can remain available; stale semantic entries must not be represented as current without indicating their pending/stale status. A requested semantic search must not silently become keyword-only if the runtime is unavailable: return a clear status and offer the available keyword mode.

## 10. Embedding runtime and onboarding

The goal is one setup experience, not necessarily one executable. Recall supplies a compatible native inference runtime; the user does not separately install or manage Ollama, a Python environment, or an embedding server.

**Recommended implementation:** bundle a pinned llama.cpp-based worker for Apple Silicon and have Recall manage its lifecycle through a private local connection. This avoids Go/C++ bindings for inference and isolates worker failures. Validate runtime/model compatibility and packaging before committing to that specific runtime. This recommendation is not a completed feasibility benchmark.

`recall init` should:

1. Establish the central root and configuration without disturbing unrelated files.
2. Prepare the index and verify the bundled runtime can execute.
3. Download the chosen default model and required assets with visible progress.
4. Verify pinned checksums and install complete assets atomically.
5. Generate a test embedding and clearly report readiness or the remaining failure.

Initialization should be idempotent and safely retryable after interruptions. It must not require the user to select a model up front. Model/runtime assets remain separate from the Go executable and from the user-content tree.

Initial installation/downloads may require internet access. Normal indexing and searching run offline and do not send note/task content to an external inference service. A fully offline installation archive is not required for v1.

The runtime is started when needed, reused for batches/nearby searches, and can be stopped or unloaded when idle. MCP startup and ordinary note/task operations should not wait for a model to load. Do not expose the worker to the network or enable unrelated model tools.

The M1/16 GB machine is a realistic target for small embedding models, but measure actual memory, latency, and indexing behavior. EmbeddingGemma 1 is one previously discussed candidate, not a final model choice. Pin the selected model, tokenizer/configuration, query/document formatting, pooling, and normalization consistently. Changing them requires an explicit vector-index rebuild, not silent mixing of incompatible embeddings.

## 11. Human interfaces

The TUI should support navigating shared folders, opening/editing either document type in Neovim, managing their placement, finding information, and inspecting tasks by state/date.

A folder/document browser is recommended for the shared hierarchy. The owner selected a sortable/filterable task list, which can aggregate globally or be folder-scoped, rather than Kanban columns. Final panes, navigation keys, and visual styling are left to the builder for a later text-based proposal. The user declined browser mockups.

**Recommended TUI libraries:** Bubble Tea and Lip Gloss, subject to implementation validation. Only Go itself is a user-selected language/framework requirement.

The CLI must work from any project directory and support the same core operations without entering the TUI. Machine-oriented invocations must not launch Neovim, show interactive confirmations, print progress into JSON output, or depend on a terminal being attached. Human-oriented edit commands may invoke the configured editor explicitly.

## 12. Agent interfaces and permissions

**Recommended integration:** both a JSON-capable CLI and a local stdio MCP server, backed by the same Go application services as the TUI. MCP makes capabilities discoverable to compatible clients; the CLI covers agents and scripts with shell access. Neither requires a public HTTP API.

The MCP entry point can be `recall mcp`. Exact command/tool names are a builder choice; the functional capabilities must include:

- Search and read document content and metadata.
- Create/update/delete notes and tasks; change task states and inspect history.
- List/filter/sort tasks using the same folder/state/date semantics as the TUI.
- List/create/rename/move/delete folders and move documents between them.

Agents have full permissions within Recall. Do not require a separate read-only mode or an extra in-app approval round for every mutation. MCP hosts can still apply their own policies; Recall must not try to bypass them.

Agent operations must use stable IDs or explicit paths, typed inputs, bounded/paginated outputs, clear error codes, and noninteractive results. Updates should accept a content revision/hash so a stale agent read can be detected before it overwrites a newer version. Full permissions are not permission to silently discard concurrent work.

Tools operate within the configured content root, not arbitrary filesystem paths or shell commands. Agents edit document content through Recall operations; they do not need to know SQLite's schema. Logging goes to stderr or a log file, never into MCP protocol stdout or structured CLI output.

An agent can also edit source files directly where its host permits filesystem access. Those edits follow the same best-effort external reconciliation rules as Neovim; they are not guaranteed to produce the exact transition history of an explicit state-change operation.

## 13. Architecture and resilience

Keep a shared Go application layer for document/folder operations, validation, task transitions, source-file persistence, reconciliation, and retrieval. The TUI, CLI, and MCP server are adapters, not independent implementations of those rules.

Files win over derived database state. A failed index write does not roll back or discard a successfully saved source file; mark it for reconciliation and report indexing status accurately. Serialize cooperating writers to SQLite and source files. The exact locking and worker-sharing mechanism is left to the builder, but running a TUI and one or more agent clients against the same root must be supported without each making incompatible history updates or unnecessarily loading duplicate models.

Backups must include the source tree and configuration. The index is reproducible; backing it up is optional for faster recovery. Existing backups are outside the permanent-delete guarantee.

Do not store source-content payloads in an undeletable application audit log as a workaround for history or indexing. Technical logs should avoid unnecessary document content. There is no hidden database authority to restore manually removed source content.

## 14. Acceptance criteria

The implementation plan should provide verification for at least these outcomes:

1. A note/task created from an unrelated project directory lands in the configured central collection.
2. Nested folders contain both document types, and folder moves preserve document IDs while updating indexed paths.
3. Both document types can be edited as actual files in Neovim without required body templates.
4. All four task states work in any order; reopening preserves history; repeated same-state operations do not create transitions.
5. Recall-originated transitions persist state and history together, consistently across TUI, CLI, and MCP.
6. Direct editor saves and changes made while Recall was closed reconcile to the latest state, allowing missed intermediate events but preserving final content.
7. Date filters distinguish current-state age from historical transition queries, including reopened tasks and local day boundaries.
8. Only filename-compliant documents are automatically adopted. Missing schema fields are added safely; filename/type conflicts, malformed metadata, and duplicate IDs produce visible diagnostics without destroying source files or silently serving stale records. Noncompliant files stay untouched.
9. Permanent deletion removes the source and live derived data without deleting other documents or being undone by background jobs.
10. Removing the index and rebuilding recovers document IDs, task states, histories, and folder membership from files.
11. Exact-text and paraphrased queries retrieve appropriate source passages; folder/type/task filters constrain results correctly.
12. Initialization can recover from interrupted downloads, verifies assets, and produces embeddings without Ollama.
13. After setup, semantic indexing/search succeeds with network access disabled on the target machine.
14. Ordinary browsing, editing, task filtering, and keyword search remain usable if the embedding worker fails or is not loaded.
15. Agent clients can perform full noninteractive CRUD and permanent deletion; CLI/MCP output remains machine-readable.
16. Concurrent TUI/agent operations detect stale revisions, avoid blind content overwrites, and do not require independent model loads for every request.

These are design acceptance criteria, not claims that the behavior has been implemented or tested.

## 15. Builder decisions and remaining tradeoffs

The builder can settle the following within this design and should record the selected choices in the implementation plan:

- Default root location and configuration/override discovery.
- Final CLI verbs, MCP tool grouping, keyboard bindings, and text-based TUI layout.
- ID algorithm, parser/serializer, timestamp rendering, file naming, and index schema.
- Filesystem watcher, coalescing interval, reconciliation recovery, file locking, and stale-write handling.
- Worker packaging/lifecycle/sharing, platform-specific native assets, and distribution signing requirements.
- The exact compatible embedding model/runtime pairing, licensing, checksums, chunking, ranking, and vector-search extension.

The builder review produced one follow-up round. The owner resolved its product questions as follows:

- Automatic schema insertion is allowed only for files whose compliant filenames explicitly identify their type. Recall must not guess whether arbitrary Markdown is a note or task.
- Explicit document links and backlinks are deferred.
- The initial task interface is a filtered list.

Deferring additional date fields follows the requested state-history dates and the decision to use sorting/filtering rather than reminders. The owner approved this design, including its proposed filename convention, metadata defaults, title fallback, UTC/local date behavior, and SQLite-based derived index. Exact runtime/model selection and the other implementation choices above remain with the builder. No material product question from the builder review remains unanswered.

No implementation, dependency installation, project scaffolding, or repository initialization is authorized by this document. After the owner approves the complete design, the next step is an implementation plan.
