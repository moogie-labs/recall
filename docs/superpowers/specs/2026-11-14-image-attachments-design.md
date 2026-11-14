# Recall: image attachments by drag and drop and paste

Date: 2026-11-14  
Status: Approved design; not yet implemented.

## 1. Purpose

Let people put images into a note in the desktop app by dropping image files
onto the editor or pasting an image with ⌘V, and see those images in Preview,
without weakening Recall's confined-storage guarantees or its promise that
Preview never fetches remote content.

## 2. Agreed requirements

- Dropping one or more image files onto the editor (Edit mode) copies each into
  the collection and inserts a Markdown image link at the drop position.
- Pasting an image with ⌘V does the same at the cursor.
- Images are stored beside their note: `Ideas/plan.md` keeps its images in
  `Ideas/plan.assets/`, linked relatively as `![name](plan.assets/name.png)`.
- Preview displays images stored in a note's `.assets` folder.
- Deleting a note also deletes its `.assets` folder; the confirmation says so.
- Moving or renaming a note (CLI/MCP) moves its `.assets` folder with it, and a
  rename rewrites the note's links to the old folder name.
- Accepted formats are PNG, JPEG, GIF and WebP, detected from content, up to
  20 MB per image. SVG is excluded because it can carry script.

## 3. Scope and non-goals

In scope: an application-level attachment save operation and desktop binding,
scanner treatment of `.assets` folders, delete and move lifecycle, a confined
image route in the desktop asset server, Preview rendering, editor drop/paste,
tests and documentation.

Not in scope:

- Dropping files onto the sidebar tree, or images as standalone tree items.
- Any image viewer, resizing, cropping or format conversion.
- Remote (`http`/`https`) images in Preview; they remain placeholders.
- Non-image attachments.
- Orphan cleanup. An image whose link was removed (for example, paste then
  undo) stays in the `.assets` folder. A cleanup command may follow later.
- macOS Trash. Deletion remains permanent, as for notes and folders today.
- Dropping into Preview mode. Drops there are cancelled and do nothing.

## 4. Storage and lifecycle

### 4.1 Attachment folders

For a note at `<dir>/<base>.md`, its attachment folder is `<dir>/<base>.assets/`.
Any folder whose name ends in `.assets` is an attachment folder.

### 4.2 Saving an image

A new `App.SaveAttachment(ctx, ref, name string, data []byte) (string, error)`
in `internal/app`:

1. Resolves the note by `ref` (ID). A missing note returns `not_found`.
2. Rejects data over 20 MB (`invalid_argument`).
3. Detects the format from the bytes (magic numbers for PNG, JPEG, GIF, WebP).
   Anything else returns `invalid_argument` naming the supported formats.
4. Derives the filename from `name`: strip directory components and the
   original extension, keep letters, digits, `-`, `_` and spaces collapsed to
   `-`, trim to a bounded length, fall back to `image` if empty, and append the
   extension for the detected format.
5. Creates `<base>.assets/` if absent using the confined `Mkdir`. An existing
   `.assets` entry that is a symlink or not a directory is refused
   (`outside_root` / `invalid_argument`).
6. Writes the file create-only through the confined write primitives, never
   following symlinks or overwriting. On a name clash it retries with `-2`,
   `-3`, … up to a bound. If `fsstore` lacks a create-only write, add one.
7. Returns the link target relative to the note's folder, e.g.
   `plan.assets/screenshot.png`.

The desktop service exposes `SaveImage(SaveImageInput) SaveImageReply` with the
session, document ID, original name and base64 data. It uses the
same session check and `Problem` mapping as other bindings.

Pasted images have no filename; the frontend supplies
`pasted-YYYY-MM-DD-HHMMSS` (local time).

### 4.3 Scanner and tree

`internal/scanner` skips attachment folders entirely: no `Folders` entry, no
diagnostics for their contents, no documents parsed from them. This applies
to every client (desktop, CLI, MCP). A `.md` file placed inside a `.assets`
folder by hand is therefore not a note; this is documented.

### 4.4 Delete

`App.Delete` keeps its revision check and removes the note, then recursively
removes `<base>.assets/` if it is a real directory (confined, no-follow; a
regular file of that name is left alone). If the note is removed but attachment
removal fails, including because the folder is a symlink, it returns a
`partial_failure` error stating that the note was deleted and its images were
not, with the folder path. The index is still resynchronised. The desktop treats
`partial_failure` as a completed delete and shows the message.

A new `App.AttachmentCount(ref)` counts the files directly inside the note's
attachment folder (0 if absent), exposed to the desktop as an `Attachments(session, id)`
binding. (A field on `DocumentView` would not work: a note can be deleted from the
tree without being opened.) The desktop delete confirmation adds "Also deletes N
attached image(s)." when the count is non-zero. CLI and MCP `delete` have the
same behaviour via the shared operation.

### 4.5 Move and rename

`App.Move` moves `<base>.assets/` alongside the note within the same mutation:

- Same basename, new folder: the folder moves; relative links keep working.
- New basename: the folder becomes `<newbase>.assets/`, and Markdown image and
  link destinations in the body that begin with `<base>.assets/` are rewritten
  to `<newbase>.assets/`. Nothing else in the source changes, including
  frontmatter.
- If the destination attachment folder already exists, the move fails with
  `conflict` before anything is changed.

`MoveFolder` and recursive `DeleteFolder` already carry attachment folders with
their contents.

## 5. Displaying images

### 5.1 Image route

`desktop/main.go` sets `assetserver.Options.Handler` alongside `Assets`. Wails
calls it only for requests the embedded frontend files don't match, from the app's own webview.
It serves only `GET /note-image/<url-encoded content-relative path>`:

1. If no collection is open, return 404. Access goes through the service lock
   so collection switches are safe.
2. Decode the path once; it must pass `fsstore` path validation (no `..`,
   absolute paths, empty or `.` segments, NUL), which also defeats encoded
   traversal such as `%2e%2e`.
3. The immediate parent folder must end in `.assets`; the extension must be one
   of the four formats.
4. Open through the confined no-follow read path; the target must be a
   regular file.
5. Detect the content type from the bytes; it must be one of the four formats
   and match the extension.

Success responds with that `Content-Type`, `X-Content-Type-Options: nosniff`
and `Cache-Control: no-cache`. Every failure is a plain 404 with no detail.
Other paths and methods fall through to 404.

### 5.2 Preview

`MarkdownPreview` receives a `notePath` prop from `DocumentPane`.

- Image sources that are relative (no scheme, no leading `/`) are resolved
  against the note's folder. If the result is inside `content/` and its parent
  folder ends in `.assets`, it maps to `/note-image/<encoded path>` and renders
  as `<img alt=… loading="lazy">`.
- On load error the existing "(image unavailable)" placeholder is shown.
- Remote and other image sources still render the placeholder; nothing remote
  is fetched. Link handling is unchanged.
- `rehype-sanitize` stays in place; verify its schema admits these URLs
  without broadening anything else.

### 5.3 Stray drops

A window-level `dragover`/`drop` handler cancels file drops everywhere except
over the editor's text (`.cm-content`), so WKWebView never navigates to a dropped
file. This includes Preview mode and the editor's line-number gutter.

## 6. Editor drop and paste

A focused CodeMirror extension (e.g. `src/document/imageInput.ts`) registered
in `DocumentPane` via `EditorView.domEventHandlers`:

- **drop:** if the transfer contains any files, prevent default and take the
  insertion position from `posAtCoords`. Images are added and other files are
  reported as unsupported, so CodeMirror never inserts a file's raw bytes as
  text. Drops without files (dragged text) keep default behaviour.
- **paste:** if the clipboard contains image files, prevent default and insert
  them at the cursor; accompanying text (Finder puts the filename alongside a
  copied image file) is ignored. Clipboards without image files paste normally,
  which includes copied web page content, since it carries HTML and text rather
  than files.
- Only acts while the editor is editable (existing busy/read-only state).
- For each image, in order: read `arrayBuffer()`, base64-encode, call
  `hostAPI.saveImage`, then insert `![<name>](<returned path>)` on its own line.
  The insertion position is mapped through intervening transactions so typing
  during a save does not misplace links. The alt text is the filename without its extension.
- Inserts are ordinary edits, so autosave and the save indicator apply.

## 7. Error handling

Unsupported format, oversize, symlinked or invalid `.assets`, note missing or
moved, and write failures each surface a short message through the existing
problem display, naming the file, for example: "Couldn't add screenshot.heic:
only PNG, JPEG, GIF and WebP are supported." A failed image inserts no link and
leaves the note unchanged; other images in the same drop still proceed.

## 8. Testing

- Go (`internal/app`, `internal/fsstore`, `internal/scanner`,
  `internal/desktop`, `desktop`):
  - save: format detection, non-image and oversize rejection, name
    sanitising, collision suffixes, create-only never overwrites, symlinked or
    non-directory `.assets` refused;
  - route: literal and encoded traversal, symlinks, non-`.assets` parents,
    extension/content mismatch, non-GET, no collection open;
  - scanner: `.assets` folders and files yield no folders or diagnostics;
  - delete: attachments removed, `attachmentCount`, partial-failure message;
  - move: plain move, rename with rewritten links, unrelated links untouched,
    destination conflict.
- Vitest: drop and paste handling (image, text-only, mixed, failure, multiple
  images in order, position mapping) and Preview source mapping (relative
  `.assets` path, remote still blocked, outside-`.assets` blocked, load-error
  fallback).
- Playwright: paste an image into a note, confirm the link is inserted, switch
  to Preview and confirm the image loads.

## 9. Documentation

- `docs/desktop.md`: replace "Attachments … are not supported" with drop/paste,
  storage location, formats and limits, Preview behaviour, delete behaviour.
- `docs/cli.md`: `delete` removes attachments; `move` carries and renames them.
- `docs/implementation-decisions.md`: a numbered ruling for the confined image
  route, with its cost if wrong.
