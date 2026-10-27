// Browser-only fixture. Vite's release entry never imports this file.
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { fakeBridge } from "./api/fakeBridge";
import type { HostAPI } from "./api/types";
import "./app.css";
if (import.meta.env.MODE !== "test-ui")
  throw new Error("Test fixture is not a production entry point");
const fixture = window as any;
const api = fakeBridge(
  [
    {
      id: "reading",
      path: "Work/Reading.note.md",
      name: "Reading",
      title: "Reading notes",
      body:
        fixture.__fixtureBody ??
        "# Reading notes\n\nA quieter place to keep useful ideas.\n\n## Next steps\n\n- Read something good\n- Keep a note",
      kind: "note",
      state: "",
      revision: "r0",
    },
  ],
  ["Work"],
);
const listeners = new Map<string, Set<(payload: any) => void>>();
const controls = {
  api,
  quits: 0,
  copied: "",
  emit: (name: string, payload: any) =>
    listeners.get(name)?.forEach((fn) => fn(payload)),
  externalEdit: async () => {
    const current = (await api.openDocument("test-session", "reading"))
      .document!;
    await api.saveBody({
      session: "test-session",
      id: current.id,
      expectedRevision: current.revision,
      body: "External version",
      generation: 99,
    });
  },
};
fixture.__test = controls;
const host: HostAPI = {
  copyDraft: async (body) => {
    controls.copied = body;
    return { problem: null };
  },
  ready: async () => {},
  completeQuit: async () => {
    controls.quits++;
    return { problem: null };
  },
  cancelQuit: async () => ({ problem: null }),
  chooseCollection: async () => "/test-collection",
  openExternal: async () => ({ problem: null }),
  on: (name, callback) => {
    let set = listeners.get(name);
    if (!set) {
      set = new Set();
      listeners.set(name, set);
    }
    set.add(callback);
    return () => {
      set!.delete(callback);
    };
  },
};
createRoot(document.getElementById("root")!).render(
  <App api={api} host={host} />,
);
