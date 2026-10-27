import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { expect, it } from "vitest";
import { App } from "./App";
import { fakeBridge } from "./api/fakeBridge";
import type { HostAPI } from "./api/types";
const host: HostAPI = {
  ready: async () => {},
  completeQuit: async () => ({ problem: null }),
  openExternal: async () => ({ problem: null }),
  on: () => () => {},
};
const doc = {
  id: "a",
  path: "Work/a.note.md",
  name: "a",
  title: "A",
  body: "# A",
  revision: "r0",
  kind: "note",
  state: "",
};
it("creates beside a selected file and inside a selected folder then opens new documents in Edit", async () => {
  const api = fakeBridge([doc], ["Work"]);
  render(<App api={api} host={host} />);
  const folder = await screen.findByRole("treeitem", { name: "Work" });
  fireEvent.click(folder);
  fireEvent.click(screen.getByRole("button", { name: "Create" }));
  fireEvent.click(screen.getByRole("menuitem", { name: "New task" }));
  fireEvent.change(screen.getByRole("textbox", { name: "Name" }), {
    target: { value: "release" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Create item" }));
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "Edit" })).toHaveAttribute(
      "aria-pressed",
      "true",
    ),
  );
  const tree = (await api.tree("test-session")).tree!;
  expect(tree.entries.find((e) => e.name === "release")?.path).toBe(
    "Work/release.task.md",
  );
  expect(screen.getByText(/task · planned/i)).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Create" }));
  fireEvent.click(screen.getByRole("menuitem", { name: "New note" }));
  fireEvent.change(screen.getByRole("textbox", { name: "Name" }), {
    target: { value: "beside" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Create item" }));
  await waitFor(() =>
    expect(
      screen.getByRole("treeitem", { name: "beside" }),
    ).toBeInTheDocument(),
  );
  expect(
    (await api.tree("test-session")).tree!.entries.find(
      (e) => e.name === "beside",
    )?.path,
  ).toBe("Work/beside.note.md");
});
it("confirms permanent recursive deletion and preserves the collection on cancel", async () => {
  const api = fakeBridge([doc], ["Work"]);
  render(<App api={api} host={host} />);
  fireEvent.click(await screen.findByRole("treeitem", { name: "Work" }));
  fireEvent.click(screen.getByRole("button", { name: "Delete selection" }));
  expect(await screen.findByRole("dialog")).toHaveTextContent(/permanent/i);
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect((await api.tree("test-session")).tree!.entries).toHaveLength(2);
  fireEvent.click(screen.getByRole("button", { name: "Delete selection" }));
  fireEvent.click(
    await screen.findByRole("button", { name: "Delete permanently" }),
  );
  await waitFor(() =>
    expect(screen.queryByRole("treeitem", { name: "Work" })).toBeNull(),
  );
});
it("collapses the sidebar without losing tree expansion and uses the exact heading", async () => {
  render(<App api={fakeBridge([doc], ["Work"])} host={host} />);
  const folder = await screen.findByRole("treeitem", { name: "Work" });
  fireEvent.click(folder);
  expect(folder).toHaveAttribute("aria-expanded", "true");
  fireEvent.click(screen.getByRole("button", { name: "Toggle sidebar" }));
  expect(
    screen.getByRole("button", { name: "Toggle sidebar" }),
  ).toHaveAttribute("aria-expanded", "false");
  fireEvent.click(screen.getByRole("button", { name: "Toggle sidebar" }));
  expect(screen.getByRole("treeitem", { name: "Work" })).toHaveAttribute(
    "aria-expanded",
    "true",
  );
  expect(
    screen.getByRole("heading", { name: "Recall", level: 2 }),
  ).toBeInTheDocument();
});
