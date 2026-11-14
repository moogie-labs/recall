import { useState } from "react";
import type { AppInfo, UpdateReply } from "../api/types";
import { Modal } from "../tree/Modal";
export function SettingsDialog({
  info,
  onClose,
  onCheck,
  onToggle,
  onUpgrade,
}: {
  info: AppInfo;
  onClose: () => void;
  onCheck: () => Promise<UpdateReply>;
  onToggle: (enabled: boolean) => Promise<void>;
  onUpgrade: () => void;
}) {
  const [result, setResult] = useState<UpdateReply | null>(null),
    [checking, setChecking] = useState(false),
    [error, setError] = useState("");
  const check = () => {
    setChecking(true);
    setError("");
    void onCheck()
      .then(setResult, (e: Error) => setError(e.message))
      .finally(() => setChecking(false));
  };
  let message = "";
  if (result?.problem) message = result.problem.message;
  else if (result?.state === "current")
    message = `Recall ${result.current} is up to date.`;
  else if (result?.state === "available")
    message = `Recall ${result.latest} is available.`;
  return (
    <Modal title="Settings" onCancel={onClose}>
      <p>{`Version: Recall ${info.version}`}</p>
      <label className="setting-toggle">
        <input
          type="checkbox"
          checked={info.checkForUpdates}
          disabled={info.development}
          onChange={(e) => {
            setError("");
            void onToggle(e.target.checked).catch((err: Error) =>
              setError(err.message),
            );
          }}
        />
        Check for updates on launch
      </label>
      {info.development && (
        <p>Updates are only available in release installs.</p>
      )}
      {message && <p role="status">{message}</p>}
      {error && <p role="alert">{error}</p>}
      <div className="dialog-buttons">
        <button disabled={info.development || checking} onClick={check}>
          {checking ? "Checking…" : "Check now"}
        </button>
        {result?.state === "available" && (
          <button
            className="primary"
            onClick={() => {
              onClose();
              onUpgrade();
            }}
          >
            Upgrade
          </button>
        )}
        <button onClick={onClose}>Done</button>
      </div>
    </Modal>
  );
}
