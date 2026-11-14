import { useEffect, useMemo, useState, useSyncExternalStore } from "react";
import type {
  DesktopAPI,
  HostAPI,
  TreeEntry,
  TreeSnapshot,
  DeleteInput,
  SetupStatus as SetupState,
} from "./api/types";
import { SaveCoordinator } from "./saving/coordinator";
import { Workspace } from "./navigation/workspace";
import { DocumentPane } from "./document/DocumentPane";
import { Search } from "./search/Search";
import { registerShutdown } from "./shutdown";
import { Tree, parentPath } from "./tree/Tree";
import { CreateDialog } from "./tree/CreateDialog";
import { DeleteDialog } from "./tree/DeleteDialog";
import { movedPath, useTreeDrag } from "./tree/drag";
import { SetupStatus } from "./setup/SetupStatus";
import { useUpdates } from "./updates/useUpdates";
import { UpgradePrompt } from "./updates/UpgradePrompt";
import { VersionFooter } from "./updates/VersionFooter";
import { SettingsDialog } from "./updates/SettingsDialog";
export function App({ api, host }: { api: DesktopAPI; host: HostAPI }) {
  const coordinator = useMemo(() => new SaveCoordinator(api), [api]),
    workspace = useMemo(
      () => new Workspace(api, coordinator),
      [api, coordinator],
    );
  const state = useSyncExternalStore(workspace.subscribe, workspace.snapshot);
  const updates = useUpdates(api, host);
  const [settings, setSettings] = useState(false);
  const [tree, setTree] = useState<TreeSnapshot | null>(null),
    [initializing, setInitializing] = useState(true),
    [sidebar, setSidebar] = useState(true),
    [expanded, setExpanded] = useState(new Set<string>()),
    [menu, setMenu] = useState(false),
    [creating, setCreating] = useState<string | null>(null),
    [deleting, setDeleting] = useState<{
      entry: TreeEntry;
      input: DeleteInput;
    } | null>(null),
    [error, setError] = useState(""),
    [setup, setSetup] = useState<SetupState | null>(null);
  // Events can arrive before the frontend knows its session, so also read the
  // current status whenever a collection session starts.
  const loadSetup = (session: string) =>
    api
      .setupStatus(session)
      .then((status) => {
        if (session === workspace.snapshot().session) setSetup(status);
      })
      .catch(() => {});
  const refresh = async (session = workspace.snapshot().session) => {
    const r = await api.tree(session);
    if (session !== workspace.snapshot().session) return;
    if (r.problem) throw new Error(r.problem.message);
    setTree(r.tree);
  };
  useEffect(
    () => registerShutdown(host, workspace, coordinator),
    [host, workspace, coordinator],
  );
  useEffect(() => {
    const update = ({ session }: { session: string }) => {
      if (session !== workspace.snapshot().session) return;
      void refresh(session)
        .then(() => workspace.refreshDocument())
        .catch((e) => setError(e.message));
    };
    const off = host.on("desktop:collection-changed", update),
      warn = host.on("desktop:collection-warning", ({ session, problem }) => {
        if (session === workspace.snapshot().session && problem)
          setError(problem.message);
      }),
      progress = host.on("desktop:setup", (status: SetupState) => {
        if (status.session === workspace.snapshot().session) setSetup(status);
      });
    const focus = () => {
      const session = workspace.snapshot().session;
      if (session)
        void api
          .refresh(session)
          .then((r) => {
            if (session !== workspace.snapshot().session) return;
            if (r.problem) {
              setError(r.problem.message);
              return;
            }
            setTree(r.tree);
            return workspace.refreshDocument();
          })
          .catch((e) => setError(e.message));
    };
    window.addEventListener("focus", focus);
    return () => {
      off();
      warn();
      progress();
      window.removeEventListener("focus", focus);
    };
  }, [api, host, workspace]);
  async function chooseCollection() {
    const root = await host.chooseCollection?.();
    if (!root) return;
    await workspace.flushBefore(async () => {
      const r = await api.openCollection(root);
      if (r.problem) throw new Error(r.problem.message);
      workspace.setCollection(r.session);
      setExpanded(new Set());
      setError("");
      setSetup(null);
      void loadSetup(r.session);
      await refresh(r.session);
    });
  }
  useEffect(() => {
    let active = true;
    void api
      .openCollection("")
      .then(async (r) => {
        if (!active) return;
        if (r.problem) {
          setError(
            r.problem.message +
              (r.problem.code === "not_initialized" &&
              !r.problem.message.includes("Collection…")
                ? ' — Run recall --root "' +
                  r.root +
                  '" init, then reopen the app.'
                : ""),
          );
          return;
        }
        workspace.setCollection(r.session);
        void loadSetup(r.session);
        await refresh(r.session);
      })
      .catch((e) => {
        if (active) setError(e.message);
      })
      .finally(() => {
        if (active) setInitializing(false);
      });
    return () => {
      active = false;
      coordinator.dispose();
    };
  }, [api, workspace, coordinator]);
  const toggle = (path: string) =>
    setExpanded((current) => {
      const next = new Set(current);
      next.has(path) ? next.delete(path) : next.add(path);
      return next;
    });
  const reveal = (path: string) =>
    setExpanded((current) => {
      const next = new Set(current);
      let parent = parentPath(path);
      while (parent) {
        next.add(parent);
        parent = parentPath(parent);
      }
      return next;
    });
  const selection = state.treeSelection,
    parent = selection
      ? selection.kind === "folder"
        ? selection.path
        : parentPath(selection.path)
      : "";
  async function create(name: string) {
    await workspace.flushBefore(async () => {
      const current = workspace.snapshot(),
        selected = current.treeSelection;
      const targetParent = selected
        ? selected.kind === "folder"
          ? selected.path
          : parentPath(selected.path)
        : "";
      const r = await api.create({
        session: current.session,
        kind: creating!,
        parent: targetParent,
        name,
      });
      if (r.problem || !r.entry)
        throw new Error(r.problem?.message ?? "Creation failed");
      await refresh();
      reveal(r.entry.path);
      if (r.document) workspace.showDocument(r.document, "edit");
      else await workspace.selectTree(r.entry);
      setCreating(null);
      setError(r.indexingWarning?.message ?? "");
    });
  }
  // A move saves pending edits first when it relocates the open document,
  // then adopts the document's new path so editing continues seamlessly.
  async function move(entry: TreeEntry, folder: string) {
    const open = workspace.snapshot().document;
    const affected =
      open &&
      (open.id === entry.id ||
        (entry.kind === "folder" && open.path.startsWith(entry.path + "/")));
    const run = async () => {
      const { session, document: doc } = workspace.snapshot();
      const r = await api.move({
        session,
        id: entry.id,
        path: entry.path,
        folder,
        expectedRevision:
          doc && doc.id === entry.id
            ? (coordinator.document()?.revision ?? doc.revision)
            : "",
      });
      if (r.problem || !r.entry)
        throw new Error(r.problem?.message ?? "Move failed");
      const to = r.entry.path,
        follow = (path: string) => movedPath(path, entry.path, to);
      if (entry.kind === "folder") {
        setExpanded((current) => new Set([...current].map(follow)));
        workspace.followFolderMove(follow);
      }
      if (affected && doc) {
        let moved = r.document;
        if (!moved) {
          const reread = await api.openDocument(session, doc.id);
          if (reread.problem || !reread.document)
            throw new Error(reread.problem?.message ?? "Document unavailable");
          moved = reread.document;
        }
        workspace.updateCleanDocument(moved);
      }
      await refresh();
      reveal(to);
      setError(r.indexingWarning?.message ?? "");
    };
    if (affected) await workspace.flushBefore(run);
    else await run();
  }
  const drag = useTreeDrag({
    disabled: state.busy || !state.session,
    onMove: (entry, folder) => {
      void move(entry, folder).catch((e) => setError(e.message));
    },
  });
  async function askDelete() {
    if (!selection) return;
    const entry = selection;
    const current = state.document;
    const affected =
      current &&
      (current.id === entry.id ||
        (entry.kind === "folder" && current.path.startsWith(entry.path + "/")));
    const prepare = async () => {
      let revision = "";
      if (entry.kind !== "folder") {
        const r = await api.openDocument(state.session, entry.id);
        if (r.problem || !r.document)
          throw new Error(r.problem?.message ?? "Document unavailable");
        revision = r.document.revision;
      }
      setDeleting({
        entry,
        input: {
          session: state.session,
          id: entry.id,
          path: entry.path,
          expectedRevision: revision,
          recursive: entry.kind === "folder",
        },
      });
    };
    if (affected) await workspace.flushBefore(prepare);
    else await prepare();
  }
  async function remove() {
    if (!deleting) return;
    const target = deleting;
    const r = await api.delete(target.input);
    if (r.problem) throw new Error(r.problem.message);
    const doc = workspace.snapshot().document;
    if (
      doc &&
      (doc.id === target.entry.id ||
        (target.entry.kind === "folder" &&
          doc.path.startsWith(target.entry.path + "/")))
    )
      workspace.clearDocument();
    else workspace.clearSelection();
    setDeleting(null);
    await refresh();
  }
  return (
    <div className={"app " + (!sidebar ? "sidebar-hidden" : "")}>
      <header className="titlebar">
        <button
          className="icon"
          aria-label="Toggle sidebar"
          aria-expanded={sidebar}
          aria-controls="sidebar"
          onClick={() => setSidebar(!sidebar)}
        >
          ◧
        </button>
        <span className="brand">Recall</span>
        <Search
          api={api}
          workspace={workspace}
          session={state.session}
          reveal={reveal}
        />
        <button
          className="collection-button"
          aria-label="Choose collection"
          disabled={state.busy || initializing}
          onClick={() => {
            void chooseCollection().catch((e) => setError(e.message));
          }}
        >
          Collection…
        </button>
        <button
          className="icon"
          aria-label="Settings"
          disabled={!updates.info}
          onClick={() => setSettings(true)}
        >
          <svg
            width="17"
            height="17"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.5"
            aria-hidden="true"
          >
            <circle cx="12" cy="12" r="3" />
            <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1-2.83 0l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-2 2 2 2 0 0 1-2-2v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83 0 2 2 0 0 1 0-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1-2-2 2 2 0 0 1 2-2h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 2-2 2 2 0 0 1 2 2v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 2 2 2 2 0 0 1-2 2h-.09a1.65 1.65 0 0 0-1.51 1z" />
          </svg>
        </button>
      </header>
      <div className="app-body">
        <aside
          id="sidebar"
          className={"sidebar " + (!sidebar ? "collapsed" : "")}
          inert={!sidebar}
          aria-hidden={!sidebar}
        >
          <div className="sidebar-inner">
            <div className="sidebar-heading">
              <h2>
                <button
                  {...drag.rootProps("root-selection")}
                  aria-label="Select collection root"
                  disabled={state.busy}
                  onClick={() => workspace.clearSelection()}
                >
                  Recall
                </button>
              </h2>
              <div className="sidebar-actions">
                <button
                  className="icon"
                  aria-label="Create"
                  aria-haspopup="menu"
                  aria-expanded={menu}
                  disabled={!state.session || state.busy}
                  onClick={() => setMenu(!menu)}
                >
                  ＋
                </button>
                <button
                  className="icon"
                  aria-label="Delete selection"
                  disabled={!selection || state.busy}
                  onClick={() => {
                    void askDelete().catch((e) => setError(e.message));
                  }}
                >
                  <svg
                    width="15"
                    height="17"
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="1.5"
                    aria-hidden="true"
                  >
                    <path d="M3 6h18M9 6V3h6v3M6 6l1 15h10l1-15M10 10v7m4-7v7" />
                  </svg>
                </button>
                {menu && (
                  <div className="create-menu" role="menu">
                    {["folder", "note"].map((kind) => (
                      <button
                        key={kind}
                        role="menuitem"
                        onClick={() => {
                          setCreating(kind);
                          setMenu(false);
                        }}
                      >
                        New {kind}
                      </button>
                    ))}
                  </div>
                )}
              </div>
            </div>
            <Tree
              entries={tree?.entries ?? []}
              selection={selection}
              expanded={expanded}
              drag={drag}
              onToggle={toggle}
              onSelect={(entry) => {
                if (!state.busy)
                  void workspace
                    .selectTree(entry)
                    .catch((e) => setError(e.message));
              }}
            />
            {tree?.diagnostics.map((p) => (
              <div className="diagnostic" key={p.path + p.code}>
                {p.path}: {p.message}
              </div>
            ))}
            <SetupStatus
              status={setup}
              onRetry={() => {
                const session = workspace.snapshot().session;
                void api
                  .retrySetup(session)
                  .then((r) => {
                    if (r.problem) setError(r.problem.message);
                    else return loadSetup(session);
                  })
                  .catch((e) => setError(e.message));
              }}
            />
            <p className="tree-note">Plain Markdown. Yours to keep.</p>
            <VersionFooter
              info={updates.info}
              update={updates.update}
              phase={updates.phase}
              onUpgrade={updates.start}
            />
          </div>
        </aside>
        <main>
          <DocumentPane
            workspace={workspace}
            coordinator={coordinator}
            copyDraft={host.copyDraft}
            hidden={state.searchActive}
          />
          {(error || state.problem) && (
            <div className="app-error" role="alert">
              {error || state.problem?.message}
              <button aria-label="Dismiss error" onClick={() => setError("")}>
                ×
              </button>
            </div>
          )}
        </main>
      </div>
      {creating && (
        <CreateDialog
          kind={creating}
          parent={parent}
          onCreate={create}
          onCancel={() => setCreating(null)}
        />
      )}{" "}
      {deleting && (
        <DeleteDialog
          entry={deleting.entry}
          onDelete={remove}
          onCancel={() => setDeleting(null)}
        />
      )}
      {updates.prompting && updates.update && (
        <UpgradePrompt
          update={updates.update}
          onUpgrade={updates.start}
          onDismiss={updates.dismiss}
          onReleaseNotes={() => {
            void host.openExternal(updates.update!.releaseURL);
          }}
        />
      )}
      {settings && updates.info && (
        <SettingsDialog
          info={updates.info}
          onClose={() => setSettings(false)}
          onCheck={updates.checkNow}
          onToggle={updates.setCheckOnLaunch}
          onUpgrade={updates.start}
        />
      )}
    </div>
  );
}
