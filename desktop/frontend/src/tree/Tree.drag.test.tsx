import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { useState } from "react";
import type { TreeEntry } from "../api/types";
import { Tree } from "./Tree";
import { useTreeDrag } from "./drag";
const entries = [
  { id: "folder:Archive", path: "Archive", name: "Archive", kind: "folder" },
  { id: "folder:Work", path: "Work", name: "Work", kind: "folder" },
  { id: "folder:Work/Sub", path: "Work/Sub", name: "Sub", kind: "folder" },
  { id: "a", path: "Work/a.md", name: "a", kind: "note" },
];
const transfer = () => ({
  setData: vi.fn(),
  getData: () => "",
  effectAllowed: "",
  dropEffect: "",
  types: ["application/x-recall-entry"],
});
function Fixture({
  onMove,
  initiallyExpanded = ["Work"],
}: {
  onMove: (entry: TreeEntry, folder: string) => void;
  initiallyExpanded?: string[];
}) {
  const [expanded, setExpanded] = useState(new Set(initiallyExpanded));
  const drag = useTreeDrag({ onMove, disabled: false });
  return (
    <>
      <button {...drag.rootProps()}>Root</button>
      <Tree
        entries={entries}
        selection={null}
        expanded={expanded}
        drag={drag}
        onSelect={() => {}}
        onToggle={(path) =>
          setExpanded((current) => {
            const n = new Set(current);
            n.has(path) ? n.delete(path) : n.add(path);
            return n;
          })
        }
      />
    </>
  );
}
afterEach(() => vi.useRealTimers());
function dragOnto(source: HTMLElement, target: HTMLElement) {
  const dataTransfer = transfer();
  fireEvent.dragStart(source, { dataTransfer });
  fireEvent.dragEnter(target, { dataTransfer });
  fireEvent.dragOver(target, { dataTransfer });
  return dataTransfer;
}
it("moves a dragged note into the folder it is dropped on", () => {
  const onMove = vi.fn();
  render(<Fixture onMove={onMove} />);
  const note = screen.getByRole("treeitem", { name: "a" }),
    archive = screen.getByRole("treeitem", { name: "Archive" });
  expect(note).toHaveAttribute("draggable", "true");
  const dataTransfer = dragOnto(note, archive);
  expect(archive).toHaveClass("drop-target");
  fireEvent.drop(archive, { dataTransfer });
  expect(onMove).toHaveBeenCalledWith(entries[3], "Archive");
  expect(archive).not.toHaveClass("drop-target");
});
it("moves a dragged folder to the root target", () => {
  const onMove = vi.fn();
  render(<Fixture onMove={onMove} />);
  const sub = screen.getByRole("treeitem", { name: "Sub" }),
    root = screen.getByRole("button", { name: "Root" });
  const dataTransfer = dragOnto(sub, root);
  expect(root).toHaveClass("drop-target");
  fireEvent.drop(root, { dataTransfer });
  expect(onMove).toHaveBeenCalledWith(entries[2], "");
});
it("refuses drops into the current parent or a folder's own descendant", () => {
  const onMove = vi.fn();
  render(<Fixture onMove={onMove} />);
  const work = screen.getByRole("treeitem", { name: "Work" }),
    sub = screen.getByRole("treeitem", { name: "Sub" }),
    note = screen.getByRole("treeitem", { name: "a" });
  let dataTransfer = dragOnto(work, sub);
  expect(sub).not.toHaveClass("drop-target");
  fireEvent.drop(sub, { dataTransfer });
  fireEvent.dragEnd(work, { dataTransfer });
  dataTransfer = dragOnto(note, work);
  expect(work).not.toHaveClass("drop-target");
  fireEvent.drop(work, { dataTransfer });
  expect(onMove).not.toHaveBeenCalled();
});
it("ignores drags that did not start in the tree", () => {
  const onMove = vi.fn();
  render(<Fixture onMove={onMove} />);
  const archive = screen.getByRole("treeitem", { name: "Archive" }),
    dataTransfer = transfer();
  fireEvent.dragOver(archive, { dataTransfer });
  expect(archive).not.toHaveClass("drop-target");
  fireEvent.drop(archive, { dataTransfer });
  expect(onMove).not.toHaveBeenCalled();
});
it("opens a closed folder after hovering over it during a drag", () => {
  vi.useFakeTimers();
  render(<Fixture onMove={() => {}} initiallyExpanded={[]} />);
  const archive = screen.getByRole("treeitem", { name: "Archive" }),
    work = screen.getByRole("treeitem", { name: "Work" });
  dragOnto(archive, work);
  expect(work).toHaveAttribute("aria-expanded", "false");
  act(() => vi.advanceTimersByTime(700));
  expect(work).toHaveAttribute("aria-expanded", "true");
});
it("keeps the new target when the browser enters it before leaving the old one", () => {
  vi.useFakeTimers();
  render(<Fixture onMove={() => {}} />);
  const archive = screen.getByRole("treeitem", { name: "Archive" }),
    work = screen.getByRole("treeitem", { name: "Work" }),
    sub = screen.getByRole("treeitem", { name: "Sub" }),
    dataTransfer = transfer();
  fireEvent.dragStart(archive, { dataTransfer });
  fireEvent.dragEnter(work, { dataTransfer });
  expect(work).toHaveClass("drop-target");
  fireEvent.dragEnter(sub, { dataTransfer });
  fireEvent.dragLeave(work, { dataTransfer });
  expect(sub).toHaveClass("drop-target");
  expect(work).not.toHaveClass("drop-target");
  act(() => vi.advanceTimersByTime(700));
  expect(sub).toHaveAttribute("aria-expanded", "true");
});
