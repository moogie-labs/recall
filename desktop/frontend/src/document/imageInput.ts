import type { Extension } from "@codemirror/state";
import { ViewPlugin, type EditorView, type ViewUpdate } from "@codemirror/view";

export const supportedImageTypes = [
  "image/png",
  "image/jpeg",
  "image/gif",
  "image/webp",
];
export const maxImageBytes = 20 * 1024 * 1024;
const unsupported = "only PNG, JPEG, GIF and WebP images are supported";

export interface SavedImage {
  name: string;
  link: string;
}
export interface ImageInputOptions {
  /** Stores one image (base64 data) and returns its stored name and Markdown link; throws with a user-facing reason. */
  save(name: string, data: string): Promise<SavedImage>;
  /** Shows a short user-facing failure message. */
  report(message: string): void;
}

export function pastedImageName(now = new Date()): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `pasted-${now.getFullYear()}-${p(now.getMonth() + 1)}-${p(now.getDate())}-${p(now.getHours())}${p(now.getMinutes())}${p(now.getSeconds())}`;
}

export function imageMarkdown(saved: SavedImage): string {
  return `![${saved.name.replace(/\.[^.]+$/, "")}](${saved.link})`;
}

function readBase64(file: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () =>
      resolve(String(reader.result).replace(/^data:[^,]*,/, ""));
    reader.onerror = () =>
      reject(reader.error ?? new Error("the image could not be read"));
    reader.readAsDataURL(file);
  });
}

function filesOf(transfer: DataTransfer | null): File[] {
  if (!transfer) return [];
  if (transfer.files?.length) return Array.from(transfer.files);
  return Array.from(transfer.items ?? [])
    .filter((item) => item.kind === "file")
    .map((item) => item.getAsFile())
    .filter((f): f is File => !!f);
}

function dropPosition(view: EditorView, event: DragEvent): number {
  try {
    const pos = view.posAtCoords({ x: event.clientX, y: event.clientY });
    if (pos !== null) return pos;
  } catch {
    // No layout (for example in tests): fall back to the cursor.
  }
  return view.state.selection.main.head;
}

/**
 * Drop image files onto, or paste images into, the editor: each is stored via
 * `save` and linked on its own line where it was dropped or at the cursor.
 */
export function imageInput(options: ImageInputOptions): Extension {
  return ViewPlugin.fromClass(
    class {
      trackers = new Set<{ pos: number }>();
      destroyed = false;
      constructor(readonly view: EditorView) {}
      update(update: ViewUpdate) {
        if (update.docChanged)
          for (const t of this.trackers)
            t.pos = update.changes.mapPos(t.pos, 1);
      }
      destroy() {
        this.destroyed = true;
      }
      async insert(files: File[], pos: number) {
        const tracker = { pos };
        this.trackers.add(tracker);
        try {
          for (const file of files) {
            const label = file.name || "image";
            if (!supportedImageTypes.includes(file.type)) {
              options.report(`Couldn't add ${label}: ${unsupported}`);
              continue;
            }
            if (file.size > maxImageBytes) {
              options.report(
                `Couldn't add ${label}: images must be 20 MB or smaller`,
              );
              continue;
            }
            let saved: SavedImage;
            try {
              saved = await options.save(
                file.name || pastedImageName(),
                await readBase64(file),
              );
            } catch (error) {
              options.report(
                `Couldn't add ${label}: ${error instanceof Error ? error.message : String(error)}`,
              );
              continue;
            }
            if (this.destroyed) return;
            const line = this.view.state.doc.lineAt(tracker.pos);
            const prefix = tracker.pos > line.from ? "\n" : "";
            this.view.dispatch({
              changes: {
                from: tracker.pos,
                insert: prefix + imageMarkdown(saved) + "\n",
              },
              userEvent: "input.paste",
            });
          }
        } finally {
          this.trackers.delete(tracker);
        }
      }
    },
    {
      eventHandlers: {
        drop(event, view) {
          const files = filesOf(event.dataTransfer);
          if (!files.length) return false;
          event.preventDefault();
          if (!view.state.readOnly)
            void this.insert(files, dropPosition(view, event));
          return true;
        },
        paste(event, view) {
          const files = filesOf(event.clipboardData).filter((f) =>
            f.type.startsWith("image/"),
          );
          if (!files.length) return false;
          event.preventDefault();
          if (!view.state.readOnly)
            void this.insert(files, view.state.selection.main.head);
          return true;
        },
      },
    },
  );
}
