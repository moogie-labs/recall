// Maps a Markdown image source to the desktop's confined image route, or null
// when the image must not load. Only relative sources that resolve, inside the
// collection, to an accepted image file directly within a ".assets" folder are
// allowed; remote, absolute and scheme URLs are never fetched. The Go handler
// re-validates everything; this keeps Preview from even asking.
export const imageRoute = "/note-image/";
const extensions = /\.(png|jpe?g|gif|webp)$/i;
export function imageSource(src: string, notePath?: string): string | null {
  if (!notePath || !src) return null;
  if (
    /^[a-z][a-z0-9+.-]*:/i.test(src) ||
    src.startsWith("/") ||
    src.startsWith("\\")
  )
    return null;
  const parts = notePath.split("/").slice(0, -1);
  for (const raw of src.split(/[?#]/)[0].split("/")) {
    let part: string;
    try {
      part = decodeURIComponent(raw);
    } catch {
      return null;
    }
    if (/[/\\\0]/.test(part)) return null;
    if (part === "" || part === ".") continue;
    if (part === "..") {
      if (!parts.length) return null;
      parts.pop();
      continue;
    }
    parts.push(part);
  }
  const file = parts.at(-1) ?? "",
    folder = parts.at(-2) ?? "";
  if (!folder.endsWith(".assets") || !extensions.test(file)) return null;
  return imageRoute + parts.map(encodeURIComponent).join("/");
}
