// WKWebView opens a file dropped anywhere outside editable content, replacing
// the app with that file. Cancel file drags everywhere except over the Markdown
// editor's text, where the image plugin handles them.
export function guardFileDrops(target: Window = window): () => void {
  const handler = (event: Event) => {
    const transfer = (event as DragEvent).dataTransfer;
    if (!transfer || !Array.from(transfer.types ?? []).includes("Files"))
      return;
    if (event.target instanceof Element && event.target.closest(".cm-content"))
      return;
    event.preventDefault();
    transfer.dropEffect = "none";
  };
  target.addEventListener("dragover", handler);
  target.addEventListener("drop", handler);
  return () => {
    target.removeEventListener("dragover", handler);
    target.removeEventListener("drop", handler);
  };
}
