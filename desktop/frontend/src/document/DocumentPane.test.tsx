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
  path: "a.md",
  name: "a",
  title: "A",
  body: "# Original",
  revision: "r0",
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
      document: { ...doc, id: "b", path: "b.md", body: "# Next" },
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
function renderWithCopy(
  c: SaveCoordinator,
  w: Workspace,
  copy: (text: string) => Promise<{ problem: null | any }>,
) {
  return render(
    <DocumentPane workspace={w} coordinator={c} copyDraft={copy} />,
  );
}
const copyButton = () =>
  screen.getByRole("button", { name: "Copy Markdown to clipboard" });
it("copies the same live Markdown body from preview and edit modes", async () => {
  const { c, w } = await setup();
  const copy = vi.fn(async (_: string) => ({ problem: null }));
  const { unmount } = renderWithCopy(c, w, copy);
  act(() => c.edit("# Unsaved"));
  await act(async () => {
    fireEvent.click(copyButton());
  });
  expect(screen.getByRole("button", { name: "Copied" })).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Copied" }));
  });
  expect(copy.mock.calls).toEqual([["# Unsaved"], ["# Unsaved"]]);
  unmount();
  c.dispose();
});
it("returns to the copy label shortly after a successful copy", async () => {
  vi.useFakeTimers();
  const { c, w } = await setup();
  const { unmount } = renderWithCopy(c, w, async () => ({ problem: null }));
  await act(async () => {
    fireEvent.click(copyButton());
  });
  expect(screen.getByText("Copied to clipboard")).toBeInTheDocument();
  await act(() => vi.advanceTimersByTimeAsync(1499));
  expect(screen.getByRole("button", { name: "Copied" })).toBeInTheDocument();
  await act(() => vi.advanceTimersByTimeAsync(1));
  expect(copyButton()).toHaveTextContent("Copy");
  unmount();
  c.dispose();
});
it.each([
  [
    "a host problem",
    async () => ({ problem: { code: "internal", message: "nope" } }),
  ],
  ["a rejected bridge call", () => Promise.reject(new Error("bridge"))],
])("reports a failed copy from %s", async (_, copy) => {
  const { c, w } = await setup();
  const { unmount } = renderWithCopy(c, w, copy as any);
  await act(async () => {
    fireEvent.click(copyButton());
  });
  expect(
    screen.getByRole("button", { name: "Copy failed" }),
  ).toBeInTheDocument();
  expect(screen.getByText("Could not copy the document")).toBeInTheDocument();
  unmount();
  c.dispose();
});
it("offers no copy button when no document is open", () => {
  const api = fakeBridge([doc]);
  const c = new SaveCoordinator(api);
  const w = new Workspace(api, c);
  const { unmount } = render(<DocumentPane workspace={w} coordinator={c} />);
  expect(screen.queryByRole("button", { name: /copy/i })).toBeNull();
  unmount();
  c.dispose();
});

function pressCommandA(
  target: Element | Document = document.body,
  modifiers: Record<string, boolean> = {},
) {
  // fireEvent returns false once a handler called preventDefault.
  return !fireEvent.keyDown(target, { key: "a", metaKey: true, ...modifiers });
}
function selectedText() {
  return window.getSelection()?.toString() ?? "";
}
it("selects only the preview on Command-A", async () => {
  const { c, w } = await setup();
  const { unmount } = render(
    <div>
      <p>Outside text</p>
      <DocumentPane workspace={w} coordinator={c} />
    </div>,
  );
  expect(pressCommandA()).toBe(true);
  expect(selectedText()).toContain("Original");
  expect(selectedText()).not.toContain("Outside text");
  expect(selectedText()).not.toContain("Preview");
  unmount();
  c.dispose();
});
it.each([
  ["shift", { shiftKey: true }],
  ["control", { ctrlKey: true }],
  ["alt", { altKey: true }],
  ["no command", { metaKey: false }],
])("ignores Command-A with %s", async (_name, modifiers) => {
  const { c, w } = await setup();
  const { unmount } = render(<DocumentPane workspace={w} coordinator={c} />);
  window.getSelection()?.removeAllRanges();
  expect(pressCommandA(document.body, modifiers)).toBe(false);
  expect(selectedText()).toBe("");
  unmount();
  c.dispose();
});
it.each([
  ["input", '<input aria-label="field" />'],
  ["textarea", "<textarea></textarea>"],
  ["contenteditable", '<div contenteditable="true"><span>inner</span></div>'],
])("leaves Command-A alone in a focused %s", async (_name, html) => {
  const { c, w } = await setup();
  const { unmount } = render(<DocumentPane workspace={w} coordinator={c} />);
  const host = document.createElement("div");
  host.innerHTML = html;
  document.body.append(host);
  window.getSelection()?.removeAllRanges();
  const target = host.querySelector("span") ?? host.firstElementChild!;
  expect(pressCommandA(target)).toBe(false);
  expect(selectedText()).toBe("");
  host.remove();
  unmount();
  c.dispose();
});
it("leaves Command-A alone while a dialog is open", async () => {
  const { c, w } = await setup();
  const { unmount } = render(<DocumentPane workspace={w} coordinator={c} />);
  const dialog = document.createElement("dialog");
  dialog.setAttribute("open", "");
  document.body.append(dialog);
  window.getSelection()?.removeAllRanges();
  expect(pressCommandA()).toBe(false);
  expect(selectedText()).toBe("");
  dialog.remove();
  expect(pressCommandA()).toBe(true);
  unmount();
  c.dispose();
});
it("leaves Command-A alone in edit mode, when hidden and with no document", async () => {
  const { c, w } = await setup();
  const { rerender, unmount } = render(
    <DocumentPane workspace={w} coordinator={c} />,
  );
  window.getSelection()?.removeAllRanges();
  act(() => w.setMode("edit"));
  expect(pressCommandA()).toBe(false);
  act(() => w.setMode("preview"));
  rerender(<DocumentPane workspace={w} coordinator={c} hidden />);
  expect(pressCommandA()).toBe(false);
  rerender(<DocumentPane workspace={w} coordinator={c} />);
  expect(pressCommandA()).toBe(true);
  unmount();
  expect(pressCommandA()).toBe(false);
  c.dispose();
  const api = fakeBridge([doc]);
  const empty = new SaveCoordinator(api);
  const idle = new Workspace(api, empty);
  idle.setCollection("test-session");
  const closed = render(<DocumentPane workspace={idle} coordinator={empty} />);
  expect(pressCommandA()).toBe(false);
  closed.unmount();
  empty.dispose();
});

function pasteImage(container: HTMLElement, ...names: string[]) {
  const event = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "clipboardData", {
    value: {
      files: names.map(
        (name) => new File([new Uint8Array([1])], name, { type: "image/png" }),
      ),
      types: ["Files"],
      items: [],
    },
  });
  container.querySelector(".cm-content")!.dispatchEvent(event);
}
it("pastes an image into the note and reports failures", async () => {
  const { c, w, api } = await setup();
  const { container, unmount } = render(
    <DocumentPane workspace={w} coordinator={c} saveImage={api.saveImage} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  pasteImage(container, "shot.png");
  // A new editor's cursor starts at 0, so the link goes before the heading.
  await vi.waitFor(() =>
    expect(c.snapshot().body).toBe("![shot](a.assets/shot.png)\n# Original"),
  );
  const failing = vi.fn(async () => ({
    name: "",
    link: "",
    problem: { code: "not_found", message: "Document not found", path: "" },
  }));
  unmount();
  const second = render(
    <DocumentPane workspace={w} coordinator={c} saveImage={failing} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  pasteImage(second.container, "again.png");
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Couldn't add again.png: Document not found",
  );
  second.unmount();
  c.dispose();
});
it("keeps every image failure, clears on dismiss and when the document changes", async () => {
  const api = fakeBridge([doc, { ...doc, id: "b", path: "b.md", name: "b" }]);
  const c = new SaveCoordinator(api);
  const w = new Workspace(api, c);
  w.setCollection("test-session");
  await w.openDocument("a");
  const failing = vi.fn(async () => ({
    name: "",
    link: "",
    problem: { code: "not_found", message: "Disk full", path: "" },
  }));
  const { container, unmount } = render(
    <DocumentPane workspace={w} coordinator={c} saveImage={failing} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  pasteImage(container, "one.png", "two.png");
  const alert = await screen.findByRole("alert");
  await vi.waitFor(() => {
    expect(alert).toHaveTextContent("Couldn't add one.png: Disk full");
    expect(alert).toHaveTextContent("Couldn't add two.png: Disk full");
  });
  fireEvent.click(screen.getByRole("button", { name: "Dismiss image errors" }));
  expect(screen.queryByRole("alert")).toBeNull();
  pasteImage(container, "three.png");
  await screen.findByRole("alert");
  await act(async () => {
    await w.openDocument("b");
  });
  expect(screen.queryByRole("alert")).toBeNull();
  unmount();
  c.dispose();
});
