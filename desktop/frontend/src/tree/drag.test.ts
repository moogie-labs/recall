import { expect, it } from "vitest";
import { canDrop, movedPath } from "./drag";
const note = { id: "a", path: "Work/a.md", name: "a", kind: "note" };
const folder = {
  id: "folder:Work",
  path: "Work",
  name: "Work",
  kind: "folder",
};
const nested = {
  id: "folder:Work/Sub",
  path: "Work/Sub",
  name: "Sub",
  kind: "folder",
};
it("allows moving into another folder or to the root", () => {
  expect(canDrop(note, "Archive")).toBe(true);
  expect(canDrop(note, "")).toBe(true);
  expect(canDrop(nested, "")).toBe(true);
  expect(canDrop(folder, "Archive")).toBe(true);
});
it("refuses the current parent, the item itself and a folder's descendants", () => {
  expect(canDrop(note, "Work")).toBe(false);
  expect(canDrop(folder, "")).toBe(false);
  expect(canDrop(folder, "Work")).toBe(false);
  expect(canDrop(folder, "Work/Sub")).toBe(false);
  expect(canDrop(folder, "Work/Sub/Deep")).toBe(false);
  expect(canDrop(folder, "Workshop")).toBe(true);
});
it("rewrites paths at or under a moved folder only", () => {
  expect(movedPath("Work", "Work", "Archive/Work")).toBe("Archive/Work");
  expect(movedPath("Work/Sub/a.md", "Work", "Archive/Work")).toBe(
    "Archive/Work/Sub/a.md",
  );
  expect(movedPath("Workshop/a.md", "Work", "Archive/Work")).toBe(
    "Workshop/a.md",
  );
});
