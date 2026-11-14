import { useEffect, useRef, useState } from "react";
import type { DragEvent } from "react";
import type { TreeEntry } from "../api/types";
import { parentPath } from "./Tree";

export const DRAG_TYPE = "application/x-recall-entry";
const EXPAND_DELAY = 600;

/** Whether entry may be moved into folder ("" is the collection root). */
export function canDrop(entry: TreeEntry, folder: string) {
  if (folder === parentPath(entry.path)) return false;
  if (entry.kind !== "folder") return true;
  return folder !== entry.path && !folder.startsWith(entry.path + "/");
}

/** Rewrite path when it is, or lies under, a folder moved from `from` to `to`. */
export function movedPath(path: string, from: string, to: string) {
  if (path === from) return to;
  return path.startsWith(from + "/") ? to + path.slice(from.length) : path;
}

export interface TreeDrag {
  /** Folder path under the pointer that accepts the drop; "" is the root. */
  target: string | null;
  rowProps(
    entry: TreeEntry,
    expand?: () => void,
  ): React.ButtonHTMLAttributes<HTMLButtonElement>;
  rootProps(className?: string): React.ButtonHTMLAttributes<HTMLButtonElement>;
}

export function useTreeDrag({
  onMove,
  disabled,
}: {
  onMove: (entry: TreeEntry, folder: string) => void;
  disabled: boolean;
}): TreeDrag {
  // The dragged entry lives in a ref as well as state: dragover handlers
  // need the current value before React has rendered the drag start.
  const dragging = useRef<TreeEntry | null>(null),
    timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined),
    [target, setTarget] = useState<string | null>(null);
  const cancelExpand = () => {
    clearTimeout(timer.current);
    timer.current = undefined;
  };
  const reset = () => {
    cancelExpand();
    dragging.current = null;
    setTarget(null);
  };
  useEffect(() => cancelExpand, []);
  useEffect(() => {
    if (disabled) reset();
  }, [disabled]);
  const dropProps = (folder: string, expand?: () => void) => {
    const over = (event: DragEvent<HTMLElement>) => {
      const entry = dragging.current;
      if (!entry || !canDrop(entry, folder)) return;
      event.preventDefault();
      event.dataTransfer.dropEffect = "move";
      if (target !== folder) {
        cancelExpand();
        if (expand) timer.current = setTimeout(expand, EXPAND_DELAY);
        setTarget(folder);
      }
    };
    return {
      onDragEnter: over,
      onDragOver: over,
      onDragLeave: (event: DragEvent<HTMLElement>) => {
        if (event.currentTarget.contains(event.relatedTarget as Node | null))
          return;
        if (target === folder) {
          cancelExpand();
          setTarget(null);
        }
      },
      onDrop: (event: DragEvent<HTMLElement>) => {
        const entry = dragging.current;
        if (!entry || !canDrop(entry, folder)) return;
        event.preventDefault();
        reset();
        onMove(entry, folder);
      },
    };
  };
  return {
    target,
    rowProps: (entry, expand) => ({
      draggable: !disabled,
      onDragStart: (event) => {
        if (disabled) {
          event.preventDefault();
          return;
        }
        event.dataTransfer.setData(DRAG_TYPE, entry.id);
        event.dataTransfer.effectAllowed = "move";
        dragging.current = entry;
      },
      onDragEnd: reset,
      ...(entry.kind === "folder" ? dropProps(entry.path, expand) : {}),
    }),
    rootProps: (className = "") => ({
      className: (className + (target === "" ? " drop-target" : "")).trim(),
      ...dropProps(""),
    }),
  };
}
