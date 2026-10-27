import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { expect, it } from "vitest";
import { EditorView } from "@codemirror/view";
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
it("does not allow collection picking to race initial collection loading", async () => {
  const api = fakeBridge([doc], ["Work"]);
  let resolve!: (reply: any) => void;
  api.openCollection = () =>
    new Promise((r) => {
      resolve = r;
    });
  render(<App api={api} host={host} />);
  expect(
    screen.getByRole("button", { name: "Choose collection" }),
  ).toBeDisabled();
  await act(async () => {
    resolve({
      session: "test-session",
      root: "/test-collection",
      problem: null,
    });
  });
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Choose collection" }),
    ).not.toBeDisabled(),
  );
});
it("selects collection root after browsing without discarding the open draft", async () => {
  const api = fakeBridge([doc], ["Work"]);
  const { container } = render(<App api={api} host={host} />);
  fireEvent.click(await screen.findByRole("treeitem", { name: "Work" }));
  fireEvent.click(screen.getByRole("treeitem", { name: "a" }));
  await screen.findByRole("heading", { name: "A" });
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  const view = EditorView.findFromDOM(
    container.querySelector(".cm-editor") as HTMLElement,
  )!;
  act(() =>
    view.dispatch({
      changes: {
        from: 0,
        to: view.state.doc.length,
        insert: "# Pending draft",
      },
    }),
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Select collection root" }),
  );
  expect(view.state.doc.toString()).toBe("# Pending draft");
  expect(
    screen.getByRole("button", { name: "Delete selection" }),
  ).toBeDisabled();
  expect((await api.openDocument("test-session", doc.id)).document?.body).toBe(
    "# A",
  );
  fireEvent.click(screen.getByRole("button", { name: "Create" }));
  fireEvent.click(screen.getByRole("menuitem", { name: "New folder" }));
  fireEvent.change(screen.getByRole("textbox", { name: "Name" }), {
    target: { value: "Top level" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Create item" }));
  await screen.findByRole("treeitem", { name: "Top level" });
  expect(
    (await api.tree("test-session")).tree!.entries.find(
      (e) => e.name === "Top level",
    )?.path,
  ).toBe("Top level");
  expect(view.state.doc.toString()).toBe("# Pending draft");
});
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
