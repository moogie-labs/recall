import type { UpdateReply } from "../api/types";
import { Modal } from "../tree/Modal";
export function UpgradePrompt({
  update,
  onUpgrade,
  onDismiss,
  onReleaseNotes,
}: {
  update: UpdateReply;
  onUpgrade: () => void;
  onDismiss: () => void;
  onReleaseNotes: () => void;
}) {
  return (
    <Modal title="Upgrade available" onCancel={onDismiss}>
      <p>{`Recall ${update.latest} is available. You have ${update.current}.`}</p>
      <p>
        <button className="link-button" onClick={onReleaseNotes}>
          Release notes
        </button>
      </p>
      <div className="dialog-buttons">
        <button autoFocus data-autofocus onClick={onDismiss}>
          Not now
        </button>
        <button className="primary" onClick={onUpgrade}>
          Upgrade
        </button>
      </div>
    </Modal>
  );
}
