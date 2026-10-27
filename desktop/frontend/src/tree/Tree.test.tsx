import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { useState } from "react";
import { Tree } from "./Tree";
const entries = [
  { id: "folder:Work", path: "Work", name: "Work", kind: "folder" },
  { id: "a", path: "Work/a.note.md", name: "a", kind: "note" },
];
function Fixture() {
  const [expanded, setExpanded] = useState(new Set<string>());
  return (
    <Tree
      entries={entries}
      selection={null}
      expanded={expanded}
      onSelect={() => {}}
      onToggle={(path) =>
        setExpanded((current) => {
          const n = new Set(current);
          n.has(path) ? n.delete(path) : n.add(path);
          return n;
        })
      }
    />
  );
}
it("starts closed with an accessible expand icon and hides children from focus", () => {
  render(<Fixture />);
  const folder = screen.getByRole("treeitem", { name: "Work" });
  expect(folder).toHaveAttribute("aria-expanded", "false");
  expect(screen.queryByRole("treeitem", { name: "a" })).toBeNull();
  fireEvent.click(folder);
  expect(folder).toHaveAttribute("aria-expanded", "true");
  expect(screen.getByRole("treeitem", { name: "a" })).toBeVisible();
  fireEvent.click(folder);
  expect(screen.queryByRole("treeitem", { name: "a" })).toBeNull();
});
it("supports arrow-key expansion and navigation", () => {
  render(<Fixture />);
  const folder = screen.getByRole("treeitem", { name: "Work" });
  folder.focus();
  fireEvent.keyDown(folder, { key: "ArrowRight" });
  fireEvent.keyDown(folder, { key: "ArrowDown" });
  expect(screen.getByRole("treeitem", { name: "a" })).toHaveFocus();
  fireEvent.keyDown(document.activeElement!, { key: "ArrowLeft" });
  expect(folder).toHaveFocus();
});
