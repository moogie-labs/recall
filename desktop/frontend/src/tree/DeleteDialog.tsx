import { useEffect, useState } from "react";
import type { TreeEntry } from "../api/types";
import { Modal } from "./Modal";
export function DeleteDialog({
  entry,
  attachments,
  onDelete,
  onCancel,
}: {
  entry: TreeEntry;
  attachments?: () => Promise<number>;
  onDelete: () => Promise<void>;
  onCancel: () => void;
}) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [images, setImages] = useState(0);
  useEffect(() => {
    let live = true;
    attachments?.()
      .then((count) => {
        if (live) setImages(count);
      })
      .catch(() => {});
    return () => {
      live = false;
    };
    // The count belongs to the entry being confirmed; a new closure each
    // render must not refetch it.
  }, [entry.id]);
  return (
    <Modal
      title={"Delete “" + entry.name + "”?"}
      onCancel={() => {
        if (!busy) onCancel();
      }}
    >
      <p>
        This is permanent deletion, not macOS Trash.
        {entry.kind === "folder"
          ? " The folder and everything inside it will be deleted."
          : images > 0
            ? ` Also deletes ${images} attached image${images === 1 ? "" : "s"}.`
            : ""}
      </p>
      {error && <p role="alert">{error}</p>}
      <div className="dialog-buttons">
        <button disabled={busy} onClick={onCancel}>
          Cancel
        </button>
        <button
          className="danger"
          disabled={busy}
          onClick={() => {
            setBusy(true);
            void onDelete().catch((e) => {
              setError(e.message);
              setBusy(false);
            });
          }}
        >
          Delete permanently
        </button>
      </div>
    </Modal>
  );
}
