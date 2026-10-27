import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { EditorView } from "@codemirror/view";
import { undo } from "@codemirror/commands";
import { DocumentPane } from "./DocumentPane";
import { Workspace } from "../navigation/workspace";
import { SaveCoordinator } from "../saving/coordinator";
import { fakeBridge } from "../api/fakeBridge";
const doc = {
  id: "a",
  path: "a.note.md",
  name: "a",
  title: "A",
  body: "# Original",
  revision: "r0",
  kind: "note",
  state: "",
};
async function setup() {
  const api = fakeBridge([doc]);
  const c = new SaveCoordinator(api);
  const w = new Workspace(api, c);
  w.setCollection("test-session");
  await w.openDocument("a");
  return { c, w, api };
}
afterEach(() => vi.useRealTimers());
it("requires explicit confirmation to close a draft whose source no longer exists", async () => {
  const { c, w } = await setup();
  c.edit("do not lose without confirmation");
  c.markConflict({
    code: "not_found",
    message: "Source deleted",
    path: doc.path,
  });
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  const { unmount } = render(<DocumentPane workspace={w} coordinator={c} />);
  fireEvent.click(screen.getByRole("button", { name: "Close draft…" }));
  expect(c.snapshot().body).toBe("do not lose without confirmation");
  confirm.mockReturnValue(true);
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Close draft…" }));
  });
  expect(w.snapshot().document).toBeNull();
  expect(c.snapshot().body).toBe("");
  confirm.mockRestore();
  unmount();
  c.dispose();
});
it("blocks undo after a flush while destination navigation is still pending", async () => {
  const { c, w, api } = await setup();
  const { container, unmount } = render(
    <DocumentPane workspace={w} coordinator={c} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  const view = EditorView.findFromDOM(
    container.querySelector(".cm-editor") as HTMLElement,
  )!;
  act(() =>
    view.dispatch({
      changes: { from: view.state.doc.length, insert: " draft" },
    }),
  );
  let resolve!: (reply: any) => void, begin!: () => void;
  const started = new Promise<void>((r) => {
    begin = r;
  });
  api.openDocument = () =>
    new Promise((r) => {
      resolve = r;
      begin();
    });
  let navigation!: Promise<void>;
  await act(async () => {
    navigation = w.openDocument("b");
    await started;
  });
  let changed = false;
  act(() => {
    changed = undo(view);
  });
  const retained = c.snapshot().body;
  await act(async () => {
    resolve({
      document: { ...doc, id: "b", path: "b.note.md", body: "# Next" },
      problem: null,
    });
    await navigation;
  });
  expect(changed).toBe(false);
  expect(retained).toBe("# Original draft");
  unmount();
  c.dispose();
});
it("preserves the live draft and undo across preview toggles", async () => {
  const { c, w } = await setup();
  const { container, unmount } = render(
    <DocumentPane workspace={w} coordinator={c} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  const view = EditorView.findFromDOM(
    container.querySelector(".cm-editor") as HTMLElement,
  )!;
  act(() =>
    view.dispatch({
      changes: { from: 0, to: view.state.doc.length, insert: "# Draft" },
    }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Preview" }));
  expect(screen.getByRole("heading", { name: "Draft" })).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  act(() => {
    undo(view);
  });
  expect(c.snapshot().body).toBe("# Original");
  unmount();
  c.dispose();
});
it("shows truthful live save status including errors", async () => {
  vi.useFakeTimers();
  const { c, w } = await setup();
  const { unmount } = render(<DocumentPane workspace={w} coordinator={c} />);
  expect(screen.getByRole("status")).toHaveTextContent("Saved");
  act(() => c.edit("draft"));
  expect(screen.getByRole("status")).toHaveTextContent("Unsaved changes");
  await act(() => vi.advanceTimersByTimeAsync(1000));
  expect(screen.getByRole("status")).toHaveTextContent("Autosaving");
  await act(() => vi.advanceTimersByTimeAsync(2000));
  expect(screen.getByRole("status")).toHaveTextContent("Saved");
  act(() =>
    c.markConflict({
      code: "conflict",
      message: "External change",
      path: doc.path,
    }),
  );
  expect(screen.getByRole("status")).toHaveTextContent("Save failed");
  expect(screen.getByText("External change")).toBeInTheDocument();
  unmount();
  c.dispose();
});
it("forwards composition state without truncating editor input", async () => {
  vi.useFakeTimers();
  const { c, w } = await setup();
  const { container, unmount } = render(
    <DocumentPane workspace={w} coordinator={c} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  const content = container.querySelector(".cm-content")!;
  fireEvent.compositionStart(content);
  act(() => c.edit("自由"));
  await act(() => vi.advanceTimersByTimeAsync(4000));
  expect(c.snapshot().status).toBe("dirty");
  fireEvent.compositionEnd(content);
  await act(() => vi.advanceTimersByTimeAsync(3000));
  expect(c.snapshot().body).toBe("自由");
  expect(c.snapshot().status).toBe("saved");
  unmount();
  c.dispose();
});
it("copies an unsaved conflict draft through the native boundary without browser clipboard support", async () => {
  const { c, w } = await setup();
  c.edit("recover this draft");
  c.markConflict({
    code: "conflict",
    message: "Changed elsewhere",
    path: doc.path,
  });
  let copied = "";
  const { unmount } = render(
    <DocumentPane
      workspace={w}
      coordinator={c}
      copyDraft={async (text) => {
        copied = text;
        return { problem: null };
      }}
    />,
  );
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Copy draft" }));
  });
  expect(copied).toBe("recover this draft");
  expect(screen.getByText("Draft copied")).toBeInTheDocument();
  unmount();
  c.dispose();
});
