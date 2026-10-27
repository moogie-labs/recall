import type { TreeEntry } from "../api/types";
export function parentPath(path: string) {
  const index = path.lastIndexOf("/");
  return index < 0 ? "" : path.slice(0, index);
}
export function Tree({
  entries,
  selection,
  expanded,
  onSelect,
  onToggle,
}: {
  entries: TreeEntry[];
  selection: TreeEntry | null;
  expanded: Set<string>;
  onSelect: (entry: TreeEntry) => void;
  onToggle: (path: string) => void;
}) {
  const sorted = [...entries].sort(
    (a, b) =>
      (a.kind === "folder" ? 0 : 1) - (b.kind === "folder" ? 0 : 1) ||
      a.name.toLowerCase().localeCompare(b.name.toLowerCase()) ||
      a.path.localeCompare(b.path),
  );
  function branch(parent: string, depth: number): React.ReactNode {
    return sorted
      .filter((e) => parentPath(e.path) === parent)
      .map((entry) => {
        const folder = entry.kind === "folder",
          open = expanded.has(entry.path);
        return (
          <div key={entry.id}>
            <button
              className={
                "tree-row " + (selection?.id === entry.id ? "selected" : "")
              }
              role="treeitem"
              aria-label={entry.name}
              aria-selected={selection?.id === entry.id}
              aria-expanded={folder ? open : undefined}
              data-path={entry.path}
              style={{ paddingLeft: 12 + depth * 20 }}
              onClick={() => {
                onSelect(entry);
                if (folder) onToggle(entry.path);
              }}
              onKeyDown={(event) => {
                const row = event.currentTarget,
                  tree = row.closest("[role=tree]")!,
                  visible = [
                    ...tree.querySelectorAll<HTMLButtonElement>(
                      "[role=treeitem]",
                    ),
                  ].filter((el) => !el.closest("[inert]")),
                  index = visible.indexOf(row);
                if (event.key === "ArrowDown" || event.key === "ArrowUp") {
                  event.preventDefault();
                  visible[
                    index + (event.key === "ArrowDown" ? 1 : -1)
                  ]?.focus();
                }
                if (event.key === "ArrowRight" && folder) {
                  event.preventDefault();
                  if (!open) onToggle(entry.path);
                  else visible[index + 1]?.focus();
                }
                if (event.key === "ArrowLeft") {
                  event.preventDefault();
                  if (folder && open) onToggle(entry.path);
                  else
                    visible
                      .find((el) => el.dataset.path === parentPath(entry.path))
                      ?.focus();
                }
              }}
            >
              <span
                className={"chevron " + (open ? "open" : "")}
                aria-hidden="true"
              >
                {folder ? "›" : ""}
              </span>
              <svg
                aria-hidden="true"
                width="16"
                height="16"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.5"
              >
                {folder ? (
                  <path d="M3 7V5h7l2 3h9v12H3V7Z" />
                ) : entry.kind === "task" ? (
                  <>
                    <rect x="4" y="4" width="16" height="16" rx="2" />
                    <path d="m8 12 3 3 5-6" />
                  </>
                ) : (
                  <>
                    <path d="M5 3h10l4 4v14H5Z" />
                    <path d="M8 11h8M8 15h8" />
                  </>
                )}
              </svg>
              <span className="tree-name">{entry.name}</span>
            </button>
            {folder && (
              <div
                className={"folder-children " + (!open ? "closed" : "")}
                role="group"
                aria-hidden={!open}
                inert={!open}
              >
                <div>{branch(entry.path, depth + 1)}</div>
              </div>
            )}
          </div>
        );
      });
  }
  return (
    <nav role="tree" aria-label="Recall files">
      {branch("", 0)}
    </nav>
  );
}
