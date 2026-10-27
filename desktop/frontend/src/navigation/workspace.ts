import type {
  DesktopAPI,
  DocumentView,
  Problem,
  TreeEntry,
} from "../api/types";
import { SaveCoordinator } from "../saving/coordinator";
export interface WorkspaceState {
  session: string;
  treeSelection: TreeEntry | null;
  document: DocumentView | null;
  mode: "preview" | "edit";
  searchActive: boolean;
  busy: boolean;
  problem: Problem | null;
}
export class Workspace {
  private state: WorkspaceState = {
    session: "",
    treeSelection: null,
    document: null,
    mode: "preview",
    searchActive: false,
    busy: false,
    problem: null,
  };
  private listeners = new Set<() => void>();
  private request = 0;
  private transitions: Promise<void> = Promise.resolve();
  private activeTransitions = 0;
  private refreshDeferred = false;
  constructor(
    private api: DesktopAPI,
    readonly coordinator: SaveCoordinator,
  ) {
    coordinator.subscribe(() => {
      const save = coordinator.snapshot(),
        doc = coordinator.document();
      if (
        save.status === "saved" &&
        !save.busy &&
        doc &&
        this.state.document?.id === doc.id &&
        (this.state.document.revision !== doc.revision ||
          this.state.document.path !== doc.path)
      )
        this.synchronizeDocument(doc);
      this.resumeDeferredRefresh();
    });
  }
  snapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private update(patch: Partial<WorkspaceState>) {
    this.state = { ...this.state, ...patch };
    this.listeners.forEach((l) => l());
    this.resumeDeferredRefresh();
  }
  private resumeDeferredRefresh() {
    const save = this.coordinator.snapshot();
    if (
      !this.refreshDeferred ||
      save.composing ||
      save.busy ||
      this.state.busy ||
      !this.state.document ||
      this.coordinator.document()?.id !== this.state.document.id
    )
      return;
    this.refreshDeferred = false;
    const session = this.state.session,
      id = this.state.document.id;
    queueMicrotask(() => {
      if (session === this.state.session && id === this.state.document?.id)
        void this.refreshDocument().catch((error) =>
          this.update({
            problem: {
              code: "internal",
              message: (error as Error).message,
              path: "",
            },
          }),
        );
    });
  }
  private deferRefresh() {
    this.refreshDeferred = true;
    this.resumeDeferredRefresh();
  }
  setCollection(session: string) {
    this.request++;
    this.coordinator.clear();
    this.update({
      session,
      treeSelection: null,
      document: null,
      mode: "preview",
      searchActive: false,
      problem: null,
    });
  }
  async selectTree(entry: TreeEntry) {
    if (entry.kind === "folder") {
      this.request++;
      this.update({ treeSelection: entry });
      return;
    }
    await this.openDocument(entry.id);
  }
  async openDocument(id: string) {
    const request = ++this.request,
      session = this.state.session;
    await this.flushBefore(async () => {
      const r = await this.api.openDocument(session, id);
      if (request !== this.request || session !== this.state.session) return;
      if (r.problem || !r.document)
        throw new Error(r.problem?.message ?? "Document not found");
      this.showDocument(r.document, "preview");
    });
  }
  showDocument(doc: DocumentView, mode: "preview" | "edit") {
    this.coordinator.open(this.state.session, doc);
    this.update({
      document: doc,
      treeSelection: {
        id: doc.id,
        path: doc.path,
        name: doc.name,
        kind: doc.kind,
      },
      mode,
      searchActive: false,
      problem: null,
    });
  }
  setMode(mode: "preview" | "edit") {
    this.update({ mode });
  }
  beginSearch() {
    this.update({ searchActive: true });
  }
  clearSearch() {
    this.update({ searchActive: false });
  }
  async reloadCurrentAfterConfirmedDiscard() {
    const id = this.state.document?.id,
      session = this.state.session,
      request = ++this.request,
      generation = this.coordinator.snapshot().generation,
      interaction = this.coordinator.snapshot().interaction;
    if (!id) return;
    await this.transition(async () => {
      this.coordinator.pause();
      await this.coordinator.settle();
      if (
        session !== this.state.session ||
        id !== this.state.document?.id ||
        request !== this.request ||
        generation !== this.coordinator.snapshot().generation ||
        interaction !== this.coordinator.snapshot().interaction
      )
        return;
      const r = await this.api.openDocument(session, id);
      if (
        session !== this.state.session ||
        id !== this.state.document?.id ||
        request !== this.request ||
        generation !== this.coordinator.snapshot().generation ||
        interaction !== this.coordinator.snapshot().interaction
      )
        return;
      if (r.problem || !r.document)
        throw new Error(r.problem?.message ?? "Document unavailable");
      this.showDocument(r.document, this.state.mode);
    }, false);
  }
  async closeCurrentAfterConfirmedDiscard() {
    const id = this.state.document?.id,
      session = this.state.session,
      request = ++this.request,
      interaction = this.coordinator.snapshot().interaction;
    if (!id) return;
    await this.transition(async () => {
      this.coordinator.pause();
      await this.coordinator.settle();
      if (
        session === this.state.session &&
        id === this.state.document?.id &&
        request === this.request &&
        interaction === this.coordinator.snapshot().interaction
      )
        this.clearDocument();
    }, false);
  }
  flushBefore(action: () => Promise<void>): Promise<void> {
    return this.transition(action, true);
  }
  private transition(
    action: () => Promise<void>,
    save: boolean,
  ): Promise<void> {
    this.activeTransitions++;
    this.update({ busy: true });
    const next = this.transitions
      .catch(() => {})
      .then(async () => {
        try {
          if (save) await this.coordinator.flush();
          await action();
        } catch (error) {
          this.update({
            problem: {
              code: "internal",
              message: (error as Error).message,
              path: "",
            },
          });
          throw error;
        } finally {
          this.activeTransitions--;
          this.update({ busy: this.activeTransitions > 0 });
        }
      });
    this.transitions = next;
    return next;
  }
  clearDocument() {
    this.request++;
    this.coordinator.clear();
    this.update({
      document: null,
      treeSelection: null,
      searchActive: false,
      problem: null,
    });
  }
  clearSelection() {
    this.update({ treeSelection: null });
  }
  updateCleanDocument(doc: DocumentView) {
    if (
      this.coordinator.snapshot().status === "saved" &&
      !this.coordinator.snapshot().busy
    ) {
      this.coordinator.open(this.state.session, doc);
      this.synchronizeDocument(doc);
    }
  }
  private synchronizeDocument(doc: DocumentView) {
    this.update({
      document: doc,
      treeSelection:
        this.state.treeSelection?.id === doc.id
          ? { id: doc.id, path: doc.path, name: doc.name, kind: doc.kind }
          : this.state.treeSelection,
    });
  }
  async refreshDocument() {
    const doc = this.state.document,
      session = this.state.session;
    if (!doc) return;
    if (
      this.coordinator.snapshot().busy ||
      this.coordinator.snapshot().composing ||
      this.state.busy
    ) {
      this.deferRefresh();
      return;
    }
    const generation = this.coordinator.snapshot().generation,
      interaction = this.coordinator.snapshot().interaction,
      baseline = this.coordinator.document()?.revision;
    const r = await this.api.openDocument(session, doc.id);
    if (this.state.session !== session || this.state.document?.id !== doc.id)
      return;
    if (
      generation !== this.coordinator.snapshot().generation ||
      interaction !== this.coordinator.snapshot().interaction ||
      this.coordinator.snapshot().busy ||
      this.coordinator.snapshot().composing ||
      this.state.busy ||
      baseline !== this.coordinator.document()?.revision
    ) {
      this.deferRefresh();
      return;
    }
    if (r.problem || !r.document) {
      if (this.coordinator.snapshot().status === "saved") {
        if (r.problem?.code === "not_found") this.clearDocument();
        this.update({
          problem: r.problem ?? {
            code: "not_found",
            message: "Source unavailable",
            path: doc.path,
          },
        });
        return;
      }
      this.coordinator.markConflict(
        r.problem ?? {
          code: "not_found",
          message: "Source unavailable; copy your draft before leaving",
          path: doc.path,
        },
      );
      return;
    }
    if (
      r.document.revision === baseline &&
      r.document.path === this.coordinator.document()?.path
    )
      return;
    if (this.coordinator.snapshot().status === "saved")
      this.updateCleanDocument(r.document);
    else
      this.coordinator.markConflict({
        code: "conflict",
        message:
          "The source changed outside this editor. Copy the draft or reload.",
        path: r.document.path,
      });
  }
}
