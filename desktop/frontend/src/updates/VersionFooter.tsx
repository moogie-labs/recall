import type { AppInfo, UpdateReply } from "../api/types";
import type { UpgradePhase } from "./useUpdates";
const percent = (done: number, total: number) =>
  total > 0 ? ` ${Math.floor((done / total) * 100)}%` : "";
export function VersionFooter({
  info,
  update,
  phase,
  onUpgrade,
}: {
  info: AppInfo | null;
  update: UpdateReply | null;
  phase: UpgradePhase;
  onUpgrade: () => void;
}) {
  if (!info) return null;
  let status = null;
  if (phase.kind === "downloading")
    status = (
      <span role="status">{`Downloading Recall ${update?.latest ?? ""}…${percent(phase.done, phase.total)}`}</span>
    );
  else if (phase.kind === "verifying")
    status = <span role="status">Verifying…</span>;
  else if (phase.kind === "quitting")
    status = <span role="status">Opening Installer…</span>;
  else if (phase.kind === "failed")
    status = (
      <span role="alert">
        {phase.message}{" "}
        <button className="link-button" onClick={onUpgrade}>
          Retry upgrade
        </button>
      </span>
    );
  return (
    <div className="version-footer">
      <span>{`Recall ${info.version}`}</span>
      {update?.state === "available" && phase.kind === "idle" && (
        <button className="upgrade-available" onClick={onUpgrade}>
          Upgrade available
        </button>
      )}
      {status}
    </div>
  );
}
