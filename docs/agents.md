# Agents and MCP

Recall provides stdio MCP via `recall mcp`. It shares the same central collection,
locking, source revisions, filters and permanent deletion semantics as the CLI
and desktop app. Bare `recall` shows CLI help; it does not launch a terminal UI.
No cloud account, API key or network service is required.

## OpenCode V2 (documentation checked 2026-10-21)

The [current V2 MCP guide](https://opencode.ai/v2/docs/mcp-servers) documents
`--global` and local commands after `--`. With the executable on your session
PATH, the global registration example is:

```sh
opencode mcp add recall --global -- recall mcp
opencode mcp list
```

Prefer the absolute path to the extracted executable, especially when the
OpenCode service does not inherit your shell's PATH:

```sh
opencode mcp add recall --global -- '/path with spaces/recall-darwin-arm64/bin/recall' mcp --root '/absolute/collection'
```

These commands are for the owner to run; acceptance did **not** modify the
owner's OpenCode configuration or register a server. V2 manual configuration
uses `mcp.servers`, not server names directly under `mcp`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "servers": {
      "recall": {
        "type": "local",
        "command": ["/absolute/package/bin/recall", "mcp", "--root", "/absolute/collection"]
      }
    }
  }
}
```

## Tools and safe agent workflows

Tools: `documents_list`, `document_get`, `document_create`, `document_update`,
`document_move`, `document_delete`, `folders_list`, `folder_create`,
`folder_move`, `folder_delete`, `tasks_list`, `task_set_state`, `task_history`,
`search`. Inspect their published schemas for argument details.

- List/search returns bounded summaries/passages; explicitly get a document
  for full body/raw/history. References require exactly one ID or path.
- Summary/hit `title` is a UTF-8-safe display preview (256 bytes including any
  ellipsis); `title_truncated` is explicit. Never use it as an identity. IDs,
  paths and captured revisions remain exact. Full get retains the full title.
- Compact JSON **data values** are capped at 32 KiB, including auxiliary arrays
  and cursors; each summary/hit/diagnostic/folder reference is capped at 4 KiB.
  The outer `data` envelope, MCP JSON-RPC, and SDK text plus structured copies
  add protocol overhead beyond that budget. A page that cannot fit returns a
  bounded `output_too_large`, not a silently shortened page. Reduce `limit` or
  narrow scope; use explicit full get by known path for an oversized identity.
  Oversized error messages/paths return that fixed error with an empty path,
  never a clipped usable reference. Source metadata is not restricted/rewritten.
- Read `revision`, then use `if_revision` when updating/moving/deleting/changing
  state. Raw updates require it and complete source Markdown with unchanged ID.
- MCP create uses `kind` (`note`/`task`); CLI uses `--type`.
- MCP filters take RFC3339 timezone-bearing `[from, until)` timestamps, unlike
  human CLI inclusive calendar dates. Folder `recursive` defaults false for
  MCP; an empty folder remains global.
- Current-state filters, like date filters, require tasks. Valid note `state`
  metadata remains in source/index but cannot make a note match those filters.
- Lists use bounded limit/offset pages; follow `next_offset`. MCP document
  diagnostics share the document page budget (diagnostics first); CLI lists
  return diagnostics alongside a document-only page.
- Search defaults hybrid. Request keyword explicitly for inference-free work;
  handle `indexing`/`runtime_unavailable` instead of assuming a silent fallback.
- Tools return structured `data` or stable `error` and set MCP `isError` for
  application failures. Stdout contains protocol only.
- JSON preserves authored controls (encoded normally); human CLI previews escape
  them. Explicit non-JSON raw document output remains exact and can contain
  terminal controls: redirect untrusted source rather than displaying it.
- Agents have full permissions **inside Recall**. CLI/MCP deletion does not
  prompt, stage approval, or create trash. Folder recursion must be explicit.
  Backups and agent transcripts can retain copied content outside Recall.

MCP is a local process, not an exposed TCP daemon. The embedding manager and
native server use owner-private Unix sockets. Shared use deliberately reuses
one worker per canonical collection; exiting a CLI/MCP client does not kill a
worker another client may be using.
