# Implementation checkpoints and decisions

As of 2026-10-23, all fourteen implementation tasks have passed their independent task reviews. The complete branch still awaits its whole-branch review and final independent verification. This is an execution record, not a claim of production certification or human visual acceptance.

The owner explicitly authorized local repository initialization and an isolated worktree. Implementation is on `feat/recall-v1`; no merge, push, tag, publication, global installation or OpenCode registration has been performed.

## Completed task checkpoints

Each range includes the initial implementation and its reviewed corrections.

| Complete | Task | Commit range |
| --- | --- | --- |
| Yes | 1. Typed Markdown codec | `0897df2..0aad5d2` |
| Yes | 2. Central confined storage | `0aad5d2..508c97d` |
| Yes | 3. Adoption and reconciliation | `508c97d..9c9acc2` |
| Yes | 4. Task transitions and date ranges | `9c9acc2..d84a109` |
| Yes | 5. SQLite metadata/history/keyword index | `d84a109..d966d32` |
| Yes | 6. Shared application operations | `d966d32..018439e` |
| Yes | 7. External edits and editor handoff | `018439e..2942098` |
| Yes | 8. Verified assets and initialization | `2942098..0138fc4` |
| Yes | 9. Shared local embedding worker | `0138fc4..d18016b` |
| Yes | 10. Incremental hybrid retrieval | `d18016b..c8e7ec0` |
| Yes | 11. Human/JSON CLI | `c8e7ec0..8555bff` |
| Yes | 12. Full-permission stdio MCP | `8555bff..ea555c8` |
| Yes | 13. TUI and safe recovery | `ea555c8..92a1f74` |
| Yes | 14. Distribution and acceptance | `92a1f74..5e60d48` |

## Coordinator rulings, in order

1. **Use Git's worktree fallback.** The native harness could not create a worktree after repository initialization, despite a valid Git checkout. **Cost if wrong:** the native UI may not list this worktree; Git still tracks the isolated branch.
2. **Use harness-default subagent models.** Tool policy forbids selecting a model without a user request for that model. **Cost if wrong:** more quota/context cost than tiered model selection.
3. **Improve ignored-error test examples.** Check timezone-load and server-test errors rather than reproduce omissions in sample code. **Cost if wrong:** slightly more test harness code; no product behavior change.
4. **Include shared plan context in task briefs.** Task excerpts otherwise omit referenced types, defaults and file-map rows. **Cost if wrong:** approximately 150 extra lines of necessary context per worker.
5. **Preserve YAML reference semantics and scalar types.** Unknown aliases retain their intended references when managed anchors change; unrelated scalar values/tags remain intact. **Cost if wrong:** authors expecting aliases to behave as frozen copies see linked values change instead.
6. **Resolve `x/sys` to v0.48.0.** Pinned SQLite v1.60.1 requires it; forcing v0.47.0 would contradict its module graph. **Cost if wrong:** a transitive syscall-library change could affect filesystem behavior; the branch's tests cover that integration.
7. **Prefer no-follow monitoring over the default Darwin fsnotify backend.** The backend reopens symlink targets despite cleanup. The implemented fallback polls confined metadata. **Cost if wrong:** linear monitoring overhead; measured idle CPU is material at large source counts, as documented in verification.
8. **Add atomic create-if-absent configuration publication.** Bootstrap reloads the winner without replacing concurrent settings/unknown keys; intentional `Prepare` replacement semantics remain. **Cost if wrong:** a small internal helper and compatibility surface.
9. **Retain private `_embedding-worker --root` dispatch.** This is the tested manager/guardian entry, before normal CLI startup. **Cost if wrong:** one private argv convention differs from the draft; public commands are unaffected.
10. **Return summaries from list adapters.** Full bodies/raw/history belong to explicit get/history operations, not every list page. **Cost if wrong:** the unshipped JSON list item shape is leaner than the initial CLI draft; source/core service contracts remain unchanged.
11. **Permit malformed-file editor/delete recovery by explicit path.** Recognized typed regular files remain repairable/removable with confinement and revision checks; ambiguous ID operations still fail. **Cost if wrong:** explicit path intent can remove a file without parsing its invalid metadata.
12. **Carry captured source revisions in search hits.** Valid search actions use the revision attached to the displayed result, not a newer selection-time read. **Cost if wrong:** an additive shared/JSON result field and retriever tests; no source schema change.

## Verification limits and an execution incident

See [verification](verification.md) for actual commands, versions, measurements and the distinctions between cold/seeded setup, active/restricted workers, real/fake editors and sampled/peak memory. Notable limits include large-collection idle monitoring cost, unrun human visual/personal-config/Gatekeeper checks, and unrun targeted active-request log privacy capture.

An early CLI test harness lacked private-worker test dispatch and isolated HOME, creating empty lock files for temporary test roots in the real socket cache. Only the identified owned artifacts were removed, and controller checks confirmed an empty real socket cache and no real Recall collection. Subsequent fixtures isolate runtime HOME and reject or correctly route worker entry. Compiler subprocesses separately retain the original build-cache environment so default tests do not redownload dependencies when runtime HOME changes.
