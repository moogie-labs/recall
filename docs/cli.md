# CLI reference

All examples assume the package's `bin/` is on your **session** PATH, or replace
`recall` with the full executable path. No global installation is necessary.
`recall --help` and `recall COMMAND --help` are authoritative for flags.

## Collection and sources

Root precedence: `--root` > absolute `RECALL_HOME` >
`$HOME/.local/share/recall`. Relative `--root` is resolved once against cwd.
Layout: `config.json`, `content/`, `index.sqlite`, `models/`, `runtime/`, `run/`.
Only `content/` holds user folders and documents.

Managed filenames are case-sensitive `<name>.note.md` or `<name>.task.md`.
Plain Markdown is untouched and reported as unmanaged, not silently adopted.
Compliant external files get metadata at reconciliation. Conflicting types,
malformed metadata, duplicate IDs and symlinked content produce diagnostics;
Recall does not silently overwrite them. IDs are stable, not paths.

```sh
recall init
recall status --json
recall folder create inbox
recall folder list
recall document create --type note --folder inbox --name idea --body-file idea.txt
recall document create --type task --name investigate </dev/null
recall document list --folder inbox --recursive=false --json
recall document get --path inbox/idea.note.md --json
recall document edit --path inbox/idea.note.md
recall document move --id ID --to inbox/renamed.note.md
recall folder move inbox archive
```

Create reads body from stdin by default (terminal input yields an empty body).
`--body-file -` explicitly selects stdin. Notes have no required body sections.
`document get` returns complete source; lists return bounded metadata summaries.
Use **exactly one** of `--id` or content-relative `--path` for references.

Raw updates require the revision returned by get and complete Markdown,
including frontmatter and the unchanged ID:

```sh
recall document update --id ID --if-revision REV --raw-file replacement.md --json
recall document delete --id ID --if-revision REV --json
recall folder delete archive --recursive
```

Move, delete and task state accept optional `--if-revision`; use it for agent
writes. A stale revision is `conflict`. Deletion is immediate and permanent,
including source-backed history. There is no deleted state, trash or undo.
Folder deletion is nonrecursive unless explicitly requested; content root
cannot be deleted. Absolute/traversal/symlinked content paths are rejected.

## Tasks and search

Tasks have `planned`, `in_progress`, `stalled`, `completed` states, plus
source-backed `created`, `state_changed` and `history` metadata. Same-state
updates do not add duplicate events. External edits preserve the latest
content; intermediate state transitions can be missed (best-effort history).

```sh
recall task state --id ID --state in_progress --if-revision REV
recall task history --id ID --json
recall task list --state completed --sort state_changed --desc --limit 50 --offset 0
recall task list --date-field transition --event-state completed --from 2026-10-01 --through 2026-10-31
recall search 'forgot my password' --mode hybrid --limit 20 --json
recall search credentials --mode keyword --folder archive --recursive=false
recall sync
recall sync --wait
recall sync --rebuild --wait
```

Search defaults to hybrid (keyword + exact cosine passage retrieval with rank
fusion); semantic and keyword modes are explicit. Keyword never starts
inference. Missing/unavailable embeddings are explicit errors, not silently
downgraded results. One-shot writes need not wait for indexing. `sync --wait`
finishes pending semantic work; `--rebuild` clears derived state only.

Task/search filters include folder, recursion, states, date field, event state,
from and through. Search also supports kind filtering (`--type`). Empty folder
means global; `.` explicitly selects content root. CLI folder filters recurse
by default; use `--recursive=false` for direct children. Human date inputs are
local calendar `YYYY-MM-DD`: from includes that day, through includes the entire
day in the collection timezone (DST-aware). Task sorts: title, created,
state_changed, with stable ID ties. Task/document lists default 50, maximum
200; search defaults 20, maximum 100 (semantic candidate pool is 50 passages).
Lists paginate via nonnegative offset and `next_offset`.

## JSON and exit status

`--json` emits one `{"data":...,"error":null}` or
`{"data":null,"error":{"code":"...","message":"..."}}` object.
Diagnostics/progress go to stderr; full content is returned only by explicit
document operations. TUI and MCP streaming commands reject `--json`.

Exit 0: success; 2: invalid input/document/type/path; 3: not initialized or not
found; 4: conflict/already exists/duplicate ID/nonempty folder; 1: other failure.
Do not parse human error prose; use stable JSON error codes.
