import type { SetupStatus as Status } from "../api/types";
const megabytes = (bytes: number) => Math.round(bytes / 1_000_000);
// Semantic-search setup runs in the background; notes, tasks and keyword
// search are usable throughout. Nothing is shown once setup is ready.
export function SetupStatus({
  status,
  onRetry,
}: {
  status: Status | null;
  onRetry: () => void;
}) {
  if (!status || status.state === "ready") return null;
  if (status.state === "failed")
    return (
      <div className="setup-status failed" role="alert">
        <p>
          Semantic search setup failed: {status.problem?.message ?? "unknown"}
        </p>
        <button onClick={onRetry}>Retry setup</button>
      </div>
    );
  const percent =
    status.state === "downloading" && status.total > 0
      ? Math.floor((status.done / status.total) * 100)
      : null;
  return (
    <div className="setup-status" role="status">
      <p>
        {percent === null
          ? "Setting up semantic search…"
          : `Downloading search model… ${percent}% (${megabytes(status.done)} of ${megabytes(status.total)} MB)`}
      </p>
      <progress
        aria-label="Semantic search setup"
        max={100}
        value={percent ?? undefined}
      />
      <p className="setup-note">Notes and keyword search work meanwhile.</p>
    </div>
  );
}
