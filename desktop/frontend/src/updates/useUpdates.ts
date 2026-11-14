import { useCallback, useEffect, useRef, useState } from "react";
import type {
  AppInfo,
  DesktopAPI,
  HostAPI,
  UpdateReply,
  UpgradeProgress,
} from "../api/types";

export type UpgradePhase =
  | { kind: "idle" }
  | { kind: "downloading"; done: number; total: number }
  | { kind: "verifying" }
  | { kind: "quitting" }
  | { kind: "failed"; message: string };

export interface Updates {
  info: AppInfo | null;
  update: UpdateReply | null;
  prompting: boolean;
  phase: UpgradePhase;
  dismiss(): void;
  start(): void;
  checkNow(): Promise<UpdateReply>;
  setCheckOnLaunch(enabled: boolean): Promise<void>;
}

// Checks GitHub once at launch (unless turned off) and tracks an upgrade from
// download to the quit that opens Installer.
export function useUpdates(api: DesktopAPI, host: HostAPI): Updates {
  const [info, setInfo] = useState<AppInfo | null>(null),
    [update, setUpdate] = useState<UpdateReply | null>(null),
    [prompting, setPrompting] = useState(false),
    [phase, setPhaseState] = useState<UpgradePhase>({ kind: "idle" });
  // The ref mirrors phase synchronously so double clicks and the racing
  // reply/event pair see the latest value before React re-renders.
  const phaseRef = useRef<UpgradePhase>({ kind: "idle" });
  const setPhase = useCallback((next: UpgradePhase) => {
    phaseRef.current = next;
    setPhaseState(next);
  }, []);
  useEffect(() => {
    let active = true;
    void api
      .appInfo()
      .then(async (current) => {
        if (!active) return;
        setInfo(current);
        if (current.development || !current.checkForUpdates) return;
        const reply = await api.checkForUpdate(false);
        // Launch-check failures stay silent; Settings › Check now shows them.
        if (!active || reply.state !== "available") return;
        setUpdate(reply);
        setPrompting(true);
      })
      .catch(() => {});
    const offProgress = host.on(
      "desktop:upgrade-progress",
      (p: UpgradeProgress) => {
        // A late event after a failure or cancel must not erase the error.
        if (phaseRef.current.kind === "failed") return;
        setPhase(
          p.stage === "verifying"
            ? { kind: "verifying" }
            : { kind: "downloading", done: p.done, total: p.total },
        );
      },
    );
    const offCancelled = host.on(
      "desktop:upgrade-cancelled",
      (p: { problem?: { message: string } }) =>
        setPhase({
          kind: "failed",
          message: p?.problem?.message ?? "Upgrade cancelled.",
        }),
    );
    return () => {
      active = false;
      offProgress();
      offCancelled();
    };
  }, [api, host, setPhase]);
  const start = useCallback(() => {
    const kind = phaseRef.current.kind;
    if (kind !== "idle" && kind !== "failed") return;
    setPrompting(false);
    setPhase({ kind: "downloading", done: 0, total: 0 });
    // Only an in-progress upgrade may be moved on by the reply; a cancel
    // event that arrived first has already set the final failed state.
    const inProgress = () =>
      phaseRef.current.kind === "downloading" ||
      phaseRef.current.kind === "verifying";
    void api.startUpgrade().then(
      (r) => {
        if (!inProgress()) return;
        setPhase(
          r.problem
            ? { kind: "failed", message: r.problem.message }
            : { kind: "quitting" },
        );
      },
      (e: Error) => {
        if (inProgress()) setPhase({ kind: "failed", message: e.message });
      },
    );
  }, [api, setPhase]);
  const checkNow = useCallback(async () => {
    const reply = await api.checkForUpdate(true);
    if (reply.state === "available" || reply.state === "current")
      setUpdate(reply);
    return reply;
  }, [api]);
  const setCheckOnLaunch = useCallback(
    async (enabled: boolean) => {
      const r = await api.setCheckForUpdates(enabled);
      if (r.problem) throw new Error(r.problem.message);
      setInfo((current) => current && { ...current, checkForUpdates: enabled });
    },
    [api],
  );
  const dismiss = useCallback(() => setPrompting(false), []);
  return {
    info,
    update,
    prompting,
    phase,
    dismiss,
    start,
    checkNow,
    setCheckOnLaunch,
  };
}
