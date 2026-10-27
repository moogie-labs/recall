import { expect, it, vi } from "vitest";
import { Workspace } from "./workspace";
import { SaveCoordinator } from "../saving/coordinator";
import { fakeBridge } from "../api/fakeBridge";
import type { DocumentReply, DocumentView } from "../api/types";
const a: DocumentView = {
  id: "a",
  path: "a.note.md",
  name: "a",
  title: "A",
  body: "old",
  revision: "r0",
  kind: "note",
  state: "",
};
const b: DocumentView = { ...a, id: "b", path: "b.note.md" };
it("folder selection and preview/search preserve the draft and its save timers", async () => {
  vi.useFakeTimers();
  try {
    const api = fakeBridge([a]);
    const c = new SaveCoordinator(api);
    const w = new Workspace(api, c);
    w.setCollection("test-session");
    await w.openDocument("a");
    c.edit("draft");
    await vi.advanceTimersByTimeAsync(500);
    w.setMode("preview");
    w.beginSearch();
    w.clearSearch();
    await w.selectTree({
      id: "folder:work",
      path: "work",
      name: "work",
      kind: "folder",
    });
    expect(w.snapshot().document?.id).toBe("a");
    expect(w.snapshot().treeSelection?.id).toBe("folder:work");
    await vi.advanceTimersByTimeAsync(500);
    expect(c.snapshot().status).toBe("autosaving");
    await vi.advanceTimersByTimeAsync(2000);
    expect(c.snapshot().status).toBe("saved");
    c.dispose();
  } finally {
    vi.useRealTimers();
  }
});
it("switch flushes immediately and failure blocks navigation", async () => {
  const api = fakeBridge([a, b]);
  const c = new SaveCoordinator(api);
  const w = new Workspace(api, c);
  w.setCollection("test-session");
  await w.openDocument("a");
  c.edit("draft");
  await w.openDocument("b");
  expect((await api.openDocument("test-session", "a")).document?.body).toBe(
    "draft",
  );
  expect(w.snapshot().document?.id).toBe("b");
  c.edit("conflicting draft");
  api.saveBody = async () => ({
    saved: false,
    document: null,
    generation: 1,
    problem: { code: "conflict", message: "Changed", path: "" },
    indexingWarning: null,
  });
  await expect(w.openDocument("a")).rejects.toThrow();
  expect(w.snapshot().document?.id).toBe("b");
  expect(c.snapshot().body).toBe("conflicting draft");
  c.dispose();
});
it("ignores a late document load from an older collection", async () => {
  let resolve!: (r: DocumentReply) => void;
  let began!: () => void;
  const started = new Promise<void>((r) => {
    began = r;
  });
  const api = fakeBridge([a, b]);
  api.openDocument = () =>
    new Promise((r) => {
      resolve = r;
      began();
    });
  const c = new SaveCoordinator(api);
  const w = new Workspace(api, c);
  w.setCollection("old");
  const pending = w.openDocument("a");
  await started;
  w.setCollection("new");
  resolve({ document: a, problem: null });
  await pending;
  expect(w.snapshot().document).toBeNull();
  expect(w.snapshot().session).toBe("new");
  c.dispose();
});
it("reloads clean external moves but preserves dirty drafts on changes or deletion", async () => {
  const api = fakeBridge([a]);
  const c = new SaveCoordinator(api);
  const w = new Workspace(api, c);
  w.setCollection("test-session");
  await w.openDocument("a");
  api.openDocument = async () => ({
    document: { ...a, path: "moved.note.md", revision: "r2", body: "external" },
    problem: null,
  });
  await w.refreshDocument();
  expect(w.snapshot().document?.path).toBe("moved.note.md");
  expect(c.snapshot().body).toBe("external");
  c.edit("my draft");
  api.openDocument = async () => ({
    document: { ...a, revision: "r3", body: "someone else" },
    problem: null,
  });
  await w.refreshDocument();
  expect(c.snapshot().body).toBe("my draft");
  expect(c.snapshot().status).toBe("conflict");
  await w.reloadCurrentAfterConfirmedDiscard();
  expect(c.snapshot().body).toBe("someone else");
  c.edit("preserve on deletion");
  api.openDocument = async () => ({
    document: null,
    problem: { code: "not_found", message: "Deleted externally", path: a.path },
  });
  await w.refreshDocument();
  expect(c.snapshot().body).toBe("preserve on deletion");
  expect(c.snapshot().status).toBe("conflict");
  c.dispose();
});
it("confirmed reload never replaces edits made after its discard decision", async () => {
  const api = fakeBridge([a]);
  const c = new SaveCoordinator(api),
    w = new Workspace(api, c);
  w.setCollection("test-session");
  await w.openDocument("a");
  c.edit("discard candidate");
  c.markConflict({
    code: "conflict",
    message: "Changed externally",
    path: a.path,
  });
  let resolve!: (reply: DocumentReply) => void, begin!: () => void;
  const started = new Promise<void>((r) => {
    begin = r;
  });
  api.openDocument = () =>
    new Promise((r) => {
      resolve = r;
      begin();
    });
  const reload = w.reloadCurrentAfterConfirmedDiscard();
  await started;
  c.edit("newer input");
  resolve({
    document: { ...a, body: "external", revision: "external" },
    problem: null,
  });
  await reload;
  expect(c.snapshot().body).toBe("newer input");
  expect(c.snapshot().status).toBe("conflict");
  expect(w.snapshot().busy).toBe(false);
  c.dispose();
});
it("confirmed reload ignores responses from a previous collection", async () => {
  const api = fakeBridge([a]);
  const c = new SaveCoordinator(api),
    w = new Workspace(api, c);
  w.setCollection("old");
  await w.openDocument("a");
  let resolve!: (reply: DocumentReply) => void, begin!: () => void;
  const started = new Promise<void>((r) => {
    begin = r;
  });
  api.openDocument = () =>
    new Promise((r) => {
      resolve = r;
      begin();
    });
  const reload = w.reloadCurrentAfterConfirmedDiscard();
  await started;
  w.setCollection("new");
  resolve({ document: a, problem: null });
  await reload;
  expect(w.snapshot().session).toBe("new");
  expect(w.snapshot().document).toBeNull();
  expect(c.snapshot().body).toBe("");
  c.dispose();
});
it("defers external refresh through composition startup and detects conflict afterward", async () => {
  const api = fakeBridge([a]);
  const c = new SaveCoordinator(api),
    w = new Workspace(api, c);
  w.setCollection("test-session");
  await w.openDocument("a");
  api.openDocument = async () => ({
    document: { ...a, revision: "external", body: "external body" },
    problem: null,
  });
  c.setComposing(true);
  await w.refreshDocument();
  expect(c.document()?.revision).toBe("r0");
  expect(c.snapshot().body).toBe("old");
  c.edit("old自");
  c.setComposing(false);
  await vi.waitFor(() => expect(c.snapshot().status).toBe("conflict"));
  expect(c.snapshot().body).toBe("old自");
  expect(c.document()?.revision).toBe("r0");
  c.dispose();
});
it("ignores a refresh response if composition began while its read was pending", async () => {
  const api = fakeBridge([a]);
  const c = new SaveCoordinator(api),
    w = new Workspace(api, c);
  w.setCollection("test-session");
  await w.openDocument("a");
  let resolve!: (reply: DocumentReply) => void;
  api.openDocument = () =>
    new Promise((r) => {
      resolve = r;
    });
  const refresh = w.refreshDocument();
  c.setComposing(true);
  resolve({
    document: { ...a, revision: "external", body: "external" },
    problem: null,
  });
  await refresh;
  expect(c.document()?.revision).toBe("r0");
  expect(c.snapshot().body).toBe("old");
  c.dispose();
});
it("external deletion of a clean source does not trap navigation or quitting", async () => {
  const api = fakeBridge([a, b]);
  const c = new SaveCoordinator(api),
    w = new Workspace(api, c);
  w.setCollection("test-session");
  await w.openDocument("a");
  const read = api.openDocument;
  api.openDocument = async (session, id) =>
    id === "a"
      ? {
          document: null,
          problem: {
            code: "not_found",
            message: "Deleted externally",
            path: a.path,
          },
        }
      : read(session, id);
  await w.refreshDocument();
  expect(w.snapshot().document).toBeNull();
  await expect(c.flush()).resolves.toBeUndefined();
  await w.openDocument("b");
  expect(w.snapshot().document?.id).toBe("b");
  c.dispose();
});
it("a deleted dirty source can be closed only through explicit discard without any write", async () => {
  const api = fakeBridge([a]);
  let writes = 0;
  const save = api.saveBody;
  api.saveBody = async (input) => {
    writes++;
    return save(input);
  };
  const c = new SaveCoordinator(api),
    w = new Workspace(api, c);
  w.setCollection("test-session");
  await w.openDocument("a");
  c.edit("keep until confirmed");
  api.openDocument = async () => ({
    document: null,
    problem: { code: "not_found", message: "Deleted externally", path: a.path },
  });
  await w.refreshDocument();
  expect(c.snapshot().body).toBe("keep until confirmed");
  await expect(w.flushBefore(async () => {})).rejects.toThrow();
  await w.closeCurrentAfterConfirmedDiscard();
  expect(w.snapshot().document).toBeNull();
  expect(c.snapshot().body).toBe("");
  expect(writes).toBe(0);
  await expect(c.flush()).resolves.toBeUndefined();
  c.dispose();
});
it("follows moved document selections without replacing an unrelated selected folder", async () => {
  const api = fakeBridge([a]);
  const c = new SaveCoordinator(api),
    w = new Workspace(api, c);
  w.setCollection("test-session");
  await w.openDocument("a");
  api.openDocument = async () => ({
    document: { ...a, path: "Other/a.note.md", revision: "moved" },
    problem: null,
  });
  await w.refreshDocument();
  expect(w.snapshot().treeSelection?.path).toBe("Other/a.note.md");
  await w.selectTree({
    id: "folder:Work",
    path: "Work",
    name: "Work",
    kind: "folder",
  });
  api.openDocument = async () => ({
    document: { ...a, path: "Another/a.note.md", revision: "moved-again" },
    problem: null,
  });
  await w.refreshDocument();
  expect(w.snapshot().document?.path).toBe("Another/a.note.md");
  expect(w.snapshot().treeSelection?.path).toBe("Work");
  c.dispose();
});
it("adopts moved IDs returned by a successful save before later creation", async () => {
  const api = fakeBridge([a]);
  const save = api.saveBody;
  api.saveBody = async (input) => {
    const r = await save(input);
    return {
      ...r,
      document: r.document ? { ...r.document, path: "Moved/a.note.md" } : null,
    };
  };
  const c = new SaveCoordinator(api),
    w = new Workspace(api, c);
  w.setCollection("test-session");
  await w.openDocument("a");
  c.edit("changed");
  await c.flush();
  expect(w.snapshot().document?.path).toBe("Moved/a.note.md");
  expect(w.snapshot().treeSelection?.path).toBe("Moved/a.note.md");
  c.dispose();
});
