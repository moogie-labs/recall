import type { DesktopAPI, DocumentView, Problem } from "../api/types";
export interface Clock {
  setTimeout(callback: () => void, delayMs: number): unknown;
  clearTimeout(handle: unknown): void;
}
export interface SaveSnapshot {
  status: "saved" | "dirty" | "autosaving" | "error" | "conflict";
  body: string;
  generation: number;
  busy: boolean;
  problem: Problem | null;
  indexingWarning: Problem | null;
  composing: boolean;
  interaction: number;
}
const browserClock: Clock = {
  setTimeout: (cb, ms) => setTimeout(cb, ms),
  clearTimeout: (handle) =>
    clearTimeout(handle as ReturnType<typeof setTimeout>),
};
export class SaveCoordinator {
  private state: SaveSnapshot = {
    status: "saved",
    body: "",
    generation: 0,
    busy: false,
    problem: null,
    indexingWarning: null,
    composing: false,
    interaction: 0,
  };
  private listeners = new Set<(s: SaveSnapshot) => void>();
  private session = "";
  private doc: DocumentView | null = null;
  private epoch = 0;
  private acknowledged = 0;
  private pending: Promise<void> | null = null;
  private notice: unknown;
  private timer: unknown;
  private scheduleVersion = 0;
  constructor(
    private api: Pick<DesktopAPI, "saveBody">,
    private clock: Clock = browserClock,
  ) {}
  snapshot = () => this.state;
  document = () => this.doc;
  pause() {
    this.clearTimers();
  }
  async settle() {
    try {
      await this.pending;
    } catch {
      /* The retained draft still carries the save error. */
    }
  }
  subscribe = (listener: (s: SaveSnapshot) => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private update(patch: Partial<SaveSnapshot>) {
    this.state = { ...this.state, ...patch };
    for (const listener of this.listeners) listener(this.state);
  }
  private clearTimers() {
    this.scheduleVersion++;
    this.clock.clearTimeout(this.notice);
    this.clock.clearTimeout(this.timer);
  }
  open(session: string, doc: DocumentView) {
    this.clearTimers();
    this.epoch++;
    this.session = session;
    this.doc = { ...doc };
    this.acknowledged = 0;
    this.pending = null;
    this.update({
      status: "saved",
      body: doc.body,
      generation: 0,
      busy: false,
      problem: null,
      indexingWarning: null,
      composing: false,
      interaction: this.state.interaction + 1,
    });
  }
  edit(body: string) {
    if (!this.doc || body === this.state.body) return;
    this.clearTimers();
    const conflict = this.state.status === "conflict";
    this.update({
      body,
      generation: this.state.generation + 1,
      interaction: this.state.interaction + 1,
      status: conflict ? "conflict" : "dirty",
      problem: conflict ? this.state.problem : null,
    });
    if (!conflict && !this.state.composing) this.schedule();
  }
  private schedule() {
    const version = this.scheduleVersion;
    this.notice = this.clock.setTimeout(() => {
      this.update({ status: "autosaving" });
    }, 1000);
    this.timer = this.clock.setTimeout(() => {
      void this.save(version).catch(() => {});
    }, 3000);
  }
  setComposing(active: boolean) {
    this.clearTimers();
    this.update({ composing: active, interaction: this.state.interaction + 1 });
    if (
      !active &&
      this.state.generation !== this.acknowledged &&
      this.state.status !== "conflict" &&
      this.state.status !== "error"
    )
      this.schedule();
  }
  private save(automaticVersion?: number): Promise<void> {
    if (
      automaticVersion !== undefined &&
      (automaticVersion !== this.scheduleVersion || this.state.composing)
    )
      return Promise.resolve();
    if (this.state.composing)
      return Promise.reject(new Error("Finish text composition before saving"));
    if (this.state.status === "error" || this.state.status === "conflict")
      return Promise.reject(
        new Error(this.state.problem?.message ?? "Save failed"),
      );
    if (this.pending) {
      const epoch = this.epoch;
      return this.pending.then(() =>
        epoch === this.epoch ? this.save(automaticVersion) : undefined,
      );
    }
    if (!this.doc || this.acknowledged === this.state.generation)
      return Promise.resolve();
    const epoch = this.epoch,
      generation = this.state.generation;
    const input = {
      session: this.session,
      id: this.doc.id,
      expectedRevision: this.doc.revision,
      body: this.state.body,
      generation,
    };
    this.update({ busy: true, status: "autosaving" });
    const task = (async () => {
      try {
        const reply = await this.api.saveBody(input);
        if (epoch !== this.epoch) return;
        if (!reply.saved || !reply.document || reply.generation !== generation)
          throw (
            reply.problem ?? {
              code: "internal",
              message: "Save was not acknowledged",
              path: this.doc?.path ?? "",
            }
          );
        this.doc = reply.document;
        this.acknowledged = generation;
        this.update({
          status:
            this.state.generation === generation ? "saved" : this.state.status,
          problem: null,
          indexingWarning: reply.indexingWarning,
        });
      } catch (error) {
        if (epoch !== this.epoch) return;
        const p = error as Partial<Problem>;
        const problem = {
          code: p.code ?? "internal",
          message: p.message ?? "Save failed",
          path: p.path ?? this.doc?.path ?? "",
        };
        this.clearTimers();
        this.update({
          status: problem.code === "conflict" ? "conflict" : "error",
          problem,
        });
        throw new Error(problem.message);
      } finally {
        if (epoch === this.epoch) {
          this.pending = null;
          this.update({ busy: false });
        }
      }
    })();
    this.pending = task;
    return task;
  }
  async flush() {
    this.clearTimers();
    if (this.state.composing)
      throw new Error("Finish text composition before leaving the document");
    do {
      await this.save();
    } while (this.acknowledged !== this.state.generation);
  }
  async retry() {
    if (this.state.status === "conflict")
      throw new Error("Reload the source to resolve this conflict");
    this.update({ status: "dirty", problem: null });
    await this.flush();
  }
  markConflict(problem: Problem) {
    this.clearTimers();
    this.update({ status: "conflict", problem });
  }
  clear() {
    this.clearTimers();
    this.epoch++;
    this.doc = null;
    this.pending = null;
    this.acknowledged = 0;
    this.update({
      status: "saved",
      body: "",
      generation: 0,
      busy: false,
      problem: null,
      indexingWarning: null,
      composing: false,
      interaction: this.state.interaction + 1,
    });
  }
  dispose() {
    this.clear();
    this.listeners.clear();
  }
}
