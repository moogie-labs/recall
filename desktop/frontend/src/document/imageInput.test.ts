import { afterEach, expect, it, vi } from "vitest";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { imageInput, maxImageBytes, pastedImageName } from "./imageInput";
const views: EditorView[] = [];
afterEach(() => views.splice(0).forEach((v) => v.destroy()));
function file(name: string, type = "image/png") {
  return new File([new Uint8Array([0x89, 0x50])], name, { type });
}
function editor(
  save: (name: string, data: string) => Promise<{ name: string; link: string }>,
  report = vi.fn(),
  doc = "Hello",
  readOnly = false,
) {
  const view = new EditorView({
    parent: document.body,
    state: EditorState.create({
      doc,
      selection: { anchor: doc.length },
      extensions: [
        imageInput({ save, report }),
        EditorState.readOnly.of(readOnly),
      ],
    }),
  });
  views.push(view);
  return { view, report };
}
// A minimal DataTransfer stand-in; jsdom has none. getData feeds CodeMirror's
// own text paste/drop handling when the image plugin declines an event.
function send(
  view: EditorView,
  type: "paste" | "drop",
  files: File[],
  text = "",
) {
  const event = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(
    event,
    type === "paste" ? "clipboardData" : "dataTransfer",
    {
      value: {
        files,
        types: files.length ? ["Files"] : ["text/plain"],
        items: [],
        getData: (format: string) =>
          format.toLowerCase().startsWith("text") ? text : "",
      },
    },
  );
  view.contentDOM.dispatchEvent(event);
  return event;
}
const ok = vi.fn(async (name: string) => ({
  name: name || "image.png",
  link: "plan.assets/" + (name || "image.png"),
}));
afterEach(() => ok.mockClear());

it("pastes an image on its own line at the cursor", async () => {
  const { view } = editor(ok);
  const event = send(view, "paste", [file("shot.png")]);
  expect(event.defaultPrevented).toBe(true);
  await vi.waitFor(() =>
    expect(view.state.doc.toString()).toBe(
      "Hello\n![shot](plan.assets/shot.png)\n",
    ),
  );
  expect(ok).toHaveBeenCalledWith("shot.png", "iVA=");
});
it("names pasted images that have no filename", async () => {
  const { view } = editor(ok);
  send(view, "paste", [file("")]);
  await vi.waitFor(() => expect(ok).toHaveBeenCalled());
  expect(ok.mock.calls[0][0]).toMatch(/^pasted-\d{4}-\d{2}-\d{2}-\d{6}$/);
  expect(pastedImageName(new Date(2026, 10, 4, 9, 5, 7))).toBe(
    "pasted-2026-11-04-090507",
  );
});
it("names placeholder-named pasted images by time but keeps dropped names", async () => {
  const { view } = editor(ok);
  send(view, "paste", [file("image.png")]);
  await vi.waitFor(() => expect(ok).toHaveBeenCalledTimes(1));
  expect(ok.mock.calls[0][0]).toMatch(/^pasted-\d{4}-\d{2}-\d{2}-\d{6}$/);
  send(view, "drop", [file("image.png")]);
  await vi.waitFor(() => expect(ok).toHaveBeenCalledTimes(2));
  expect(ok.mock.calls[1][0]).toBe("image.png");
});
it("does not report a late failure after the editor is destroyed", async () => {
  let fail!: (e: Error) => void;
  const save = vi.fn(
    () => new Promise<{ name: string; link: string }>((_, rej) => (fail = rej)),
  );
  const { view, report } = editor(save);
  send(view, "paste", [file("shot.png")]);
  await vi.waitFor(() => expect(save).toHaveBeenCalled());
  view.destroy();
  fail(new Error("disk full"));
  await new Promise((r) => setTimeout(r, 20));
  expect(report).not.toHaveBeenCalled();
});
it("leaves text-only pastes to CodeMirror", () => {
  const { view } = editor(ok);
  send(view, "paste", [], " world");
  expect(view.state.doc.toString()).toBe("Hello world");
  expect(ok).not.toHaveBeenCalled();
});
it("leaves drops without files alone", () => {
  const { view } = editor(ok);
  expect(send(view, "drop", []).defaultPrevented).toBe(false);
  expect(ok).not.toHaveBeenCalled();
});
it("ignores non-image clipboard files so their text pastes normally", () => {
  const { view } = editor(ok);
  send(view, "paste", [file("report.pdf", "application/pdf")], " report.pdf");
  expect(view.state.doc.toString()).toBe("Hello report.pdf");
  expect(ok).not.toHaveBeenCalled();
});
it("keeps order and tracks the insertion point while the user types", async () => {
  let release!: () => void;
  const gate = new Promise<void>((resolve) => (release = resolve));
  const save = vi.fn(async (name: string) => {
    await gate;
    return { name, link: "plan.assets/" + name };
  });
  const { view } = editor(save);
  send(view, "paste", [file("a.png"), file("b.png")]);
  await vi.waitFor(() => expect(save).toHaveBeenCalledTimes(1));
  view.dispatch({ changes: { from: 0, insert: "Typed " } });
  release();
  await vi.waitFor(() =>
    expect(view.state.doc.toString()).toBe(
      "Typed Hello\n![a](plan.assets/a.png)\n![b](plan.assets/b.png)\n",
    ),
  );
});
it("reports unsupported, oversized and failed images and still adds the rest", async () => {
  const save = vi.fn(async (name: string) => {
    if (name === "broken.png") throw new Error("disk full");
    return { name, link: "plan.assets/" + name };
  });
  const big = file("big.png");
  Object.defineProperty(big, "size", { value: maxImageBytes + 1 });
  const { view, report } = editor(save);
  send(view, "drop", [
    file("photo.heic", "image/heic"),
    big,
    file("broken.png"),
    file("good.png"),
    file("notes.txt", "text/plain"),
  ]);
  await vi.waitFor(() =>
    expect(view.state.doc.toString()).toBe(
      "Hello\n![good](plan.assets/good.png)\n",
    ),
  );
  expect(report.mock.calls.map((c) => c[0])).toEqual([
    "Couldn't add photo.heic: only PNG, JPEG, GIF and WebP images are supported",
    "Couldn't add big.png: images must be 20 MB or smaller",
    "Couldn't add broken.png: disk full",
    "Couldn't add notes.txt: only PNG, JPEG, GIF and WebP images are supported",
  ]);
});
it("does nothing but cancel the default while read-only", () => {
  const { view } = editor(ok, vi.fn(), "Hello", true);
  expect(send(view, "paste", [file("shot.png")]).defaultPrevented).toBe(true);
  expect(ok).not.toHaveBeenCalled();
});
