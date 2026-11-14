# Image Attachments Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let people drop image files onto the desktop editor, or paste an image with ⌘V, to store it beside the note and insert a Markdown link, and show those images in Preview.

**Architecture:** A pure `internal/attachment` package owns the rules: folder naming, format detection, filenames, links and link rewriting. `internal/fsstore` gains two read helpers and `internal/scanner` skips `.assets` folders. `internal/app` adds save, count and read operations and makes delete and move carry attachments. The desktop service exposes `SaveImage`/`Attachments` bindings and a confined `/note-image/` HTTP handler for the Wails asset server. The React frontend adds a CodeMirror drop/paste plugin, Preview image mapping, a window-level drop guard and an image count in the delete dialog.

**Tech Stack:** Go 1.27 (`golang.org/x/sys/unix` no-follow descriptors), Wails v2.17 asset server, React 19, CodeMirror 6, react-markdown 10 with rehype-sanitize, Vitest + Testing Library, Playwright.

**Spec:** `docs/superpowers/specs/2026-11-14-image-attachments-design.md`

## Global Constraints

- Attachment folder for `<dir>/<base>.md` is `<dir>/<base>.assets/`; any folder whose name ends in `.assets` is an attachment folder.
- Accepted formats: PNG, JPEG, GIF, WebP, detected from content (magic bytes), never from the name. SVG is never accepted.
- Size limit: 20 MB (`20 << 20` bytes) per image.
- Every filesystem access goes through `internal/fsstore` descriptor-based, no-follow helpers; never `os.*` path APIs on collection content in production code.
- Store mutations happen inside `App.mutate`/`mutateDocument` (which hold `WithLock`); never acquire `WithLock` recursively.
- Preview never fetches remote (`http`/`https`) or scheme URLs; only relative sources resolving to a file directly inside a `.assets` folder load, via `/note-image/`.
- Image route failures are plain 404s with no detail.
- User-facing failure format: `Couldn't add <file>: <reason>`; unsupported-format reason text: `only PNG, JPEG, GIF and WebP images are supported`.
- Partial delete error code: `partial_failure`.
- Commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Commands

- Go (root module, covers `internal/...`): `go test ./...` from the repository root.
- Desktop module (needs built frontend for `//go:embed all:frontend/dist`): `(cd desktop/frontend && npm run build) && (cd desktop && go vet ./... && go test ./...)`.
- Frontend: `cd desktop/frontend && npm run typecheck && npm test`; end-to-end `npm run test:e2e`.
- Format: run `gofmt -w` on changed Go files and `cd desktop/frontend && npm run format` before committing; then `gofmt -l .` must print nothing and `npx prettier --check src tests` must pass. Code blocks in this plan are not guaranteed to be pre-formatted.

## File Map

| File | Responsibility |
| --- | --- |
| `internal/attachment/attachment.go` (new) | Pure rules: suffix, size limit, formats, folder path, `Within`, filenames, Markdown destinations, link rewriting |
| `internal/fsstore/store.go` | Add `Stat` and `List` (no-follow, confined) |
| `internal/scanner/reconcile.go` | Skip attachment folders and everything inside them |
| `internal/app/attachments.go` (new) | `Attachment`, `SaveAttachment`, `AttachmentCount`, `ReadAttachment`, delete/move helpers |
| `internal/app/documents.go` | `Delete` removes attachments; `Move` carries them and rewrites links on rename |
| `internal/mcpserver/tools.go` | Tool descriptions mention attachments for move/delete |
| `internal/desktop/types.go`, `service.go`, `bindings.go` | Binding types, `Application` interface, API allowlist |
| `internal/desktop/images.go` (new) | `SaveImage`, `Attachments`, `ImageHandler` |
| `desktop/main.go` | Register `ImageHandler` as asset-server fallback |
| `desktop/frontend/src/api/{types,bridge,fakeBridge}.ts` | `saveImage`, `attachments` |
| `desktop/frontend/src/tree/DeleteDialog.tsx`, `src/App.tsx` | Image count in confirmation; `partial_failure` handling; drop guard install; pass `saveImage` |
| `desktop/frontend/src/document/imageSource.ts` (new) | Map Markdown image sources to `/note-image/` or reject |
| `desktop/frontend/src/document/MarkdownPreview.tsx` | Render allowed images with load-error fallback |
| `desktop/frontend/src/document/imageInput.ts` (new) | CodeMirror drop/paste plugin |
| `desktop/frontend/src/document/dropGuard.ts` (new) | Cancel stray file drops |
| `desktop/frontend/src/document/DocumentPane.tsx` | Wire plugin, `notePath`, image error banner |
| `desktop/frontend/src/app.css` | Image sizing and error banner |
| `desktop/frontend/tests/images.spec.ts` (new) | End-to-end paste → Preview |
| `docs/desktop.md`, `docs/cli.md`, `docs/implementation-decisions.md` | User and decision docs |

---

### Task 1: Attachment rules package

**Files:**
- Create: `internal/attachment/attachment.go`
- Test: `internal/attachment/attachment_test.go`

**Interfaces:**
- Consumes: `document.Suffix` (`".md"`) from `recall/internal/document`.
- Produces:
  - `const FolderSuffix = ".assets"`, `const MaxBytes = 20 << 20`
  - `type Format struct{ Extension, MIME string }`; vars `PNG`, `JPEG`, `GIF`, `WebP` (extensions `.png`, `.jpg`, `.gif`, `.webp`)
  - `func Detect(data []byte) (Format, bool)`
  - `func ForExtension(ext string) (Format, bool)` (case-insensitive; `.jpeg` → `JPEG`)
  - `func Folder(notePath string) string`
  - `func IsFolderName(name string) bool`
  - `func Within(path string) bool`
  - `func FileName(original string, f Format, n int) string`
  - `func Destination(rel string) string`
  - `func RewriteLinks(body []byte, oldFolder, newFolder string) []byte`

- [ ] **Step 1: Write the failing tests**

```go
package attachment

import (
	"strings"
	"testing"
)

var pngHeader = []byte("\x89PNG\r\n\x1a\nrest")

func TestDetect(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want Format
		ok   bool
	}{
		{"png", pngHeader, PNG, true},
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0}, JPEG, true},
		{"gif87", []byte("GIF87a...."), GIF, true},
		{"gif89", []byte("GIF89a...."), GIF, true},
		{"webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), WebP, true},
		{"riff-not-webp", []byte("RIFF\x00\x00\x00\x00WAVEfmt "), Format{}, false},
		{"svg", []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"), Format{}, false},
		{"text", []byte("hello"), Format{}, false},
		{"empty", nil, Format{}, false},
	}
	for _, c := range cases {
		got, ok := Detect(c.data)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: Detect = %v, %v; want %v, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestForExtension(t *testing.T) {
	for ext, want := range map[string]Format{".png": PNG, ".PNG": PNG, ".jpg": JPEG, ".jpeg": JPEG, ".JPEG": JPEG, ".gif": GIF, ".webp": WebP} {
		if got, ok := ForExtension(ext); !ok || got != want {
			t.Errorf("ForExtension(%q) = %v, %v", ext, got, ok)
		}
	}
	for _, ext := range []string{"", ".svg", ".heic", ".md", "png"} {
		if _, ok := ForExtension(ext); ok {
			t.Errorf("ForExtension(%q) accepted", ext)
		}
	}
}

func TestFolderAndWithin(t *testing.T) {
	for note, want := range map[string]string{"plan.md": "plan.assets", "Ideas/plan.md": "Ideas/plan.assets", "a b/my plan.md": "a b/my plan.assets"} {
		if got := Folder(note); got != want {
			t.Errorf("Folder(%q) = %q, want %q", note, got, want)
		}
	}
	for path, want := range map[string]bool{
		"plan.assets":                 true,
		"Ideas/plan.assets":           true,
		"Ideas/plan.assets/shot.png":  true,
		"Ideas/plan.assets/deep/x.md": true,
		"plan.md":                     false,
		"plan.assets.md":              false,
		"assets/x.png":                false,
		"Ideas/plan.md":               false,
	} {
		if got := Within(path); got != want {
			t.Errorf("Within(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestFileName(t *testing.T) {
	cases := []struct {
		original string
		format   Format
		n        int
		want     string
	}{
		{"Screen Shot 2026-11-14 at 15.30.12.png", PNG, 1, "Screen-Shot-2026-11-14-at-15-30-12.png"},
		{"photo.jpg", PNG, 1, "photo.png"},
		{"photo.JPEG", JPEG, 2, "photo-2.jpg"},
		{"../../etc/passwd", GIF, 1, "passwd.gif"},
		{".hidden.png", PNG, 1, "hidden.png"},
		{"", WebP, 1, "image.webp"},
		{"!!!.png", PNG, 1, "image.png"},
		{"café_menu.png", PNG, 1, "café_menu.png"},
		{"pasted-2026-11-14-153012", PNG, 3, "pasted-2026-11-14-153012-3.png"},
	}
	for _, c := range cases {
		if got := FileName(c.original, c.format, c.n); got != c.want {
			t.Errorf("FileName(%q, %v, %d) = %q, want %q", c.original, c.format, c.n, got, c.want)
		}
	}
	long := FileName(strings.Repeat("a", 200)+".png", PNG, 1)
	if len([]rune(long)) != 80+len(".png") {
		t.Errorf("long name not truncated: %d runes", len([]rune(long)))
	}
}

func TestDestination(t *testing.T) {
	for rel, want := range map[string]string{
		"plan.assets/shot.png":       "plan.assets/shot.png",
		"my plan.assets/shot.png":    "my%20plan.assets/shot.png",
		"a (b).assets/café.png":      "a%20%28b%29.assets/caf%C3%A9.png",
		"x<y>.assets/z.png":          "x%3Cy%3E.assets/z.png",
	} {
		if got := Destination(rel); got != want {
			t.Errorf("Destination(%q) = %q, want %q", rel, got, want)
		}
	}
}

func TestRewriteLinks(t *testing.T) {
	body := "![a](plan.assets/a.png)\n" +
		"![b](<plan.assets/b.png>)\n" +
		"[ref]: plan.assets/c.png\n" +
		"[file](plan.assets.md) [web](https://x.example/plan.assets/a.png)\n" +
		"text plan.assets/a.png stays\n"
	want := "![a](roadmap.assets/a.png)\n" +
		"![b](<roadmap.assets/b.png>)\n" +
		"[ref]: roadmap.assets/c.png\n" +
		"[file](plan.assets.md) [web](https://x.example/plan.assets/a.png)\n" +
		"text plan.assets/a.png stays\n"
	if got := string(RewriteLinks([]byte(body), "plan.assets", "roadmap.assets")); got != want {
		t.Errorf("RewriteLinks:\n%s\nwant:\n%s", got, want)
	}
	spaced := "![a](my%20plan.assets/a.png) ![b](<my plan.assets/b.png>)"
	wantSpaced := "![a](new%20name.assets/a.png) ![b](<new%20name.assets/b.png>)"
	if got := string(RewriteLinks([]byte(spaced), "my plan.assets", "new name.assets")); got != wantSpaced {
		t.Errorf("RewriteLinks spaced = %q, want %q", got, wantSpaced)
	}
	if got := string(RewriteLinks([]byte(body), "plan.assets", "plan.assets")); got != body {
		t.Error("same folder changed the body")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/attachment/`
Expected: FAIL to compile with `undefined: Detect` (and the other names).

- [ ] **Step 3: Write the implementation**

```go
// Package attachment holds the pure rules for note image attachments: where a
// note's images live, which formats are accepted, how stored files are named,
// and how Markdown links to them are written and rewritten. It performs no I/O;
// callers do all filesystem access through the confined store.
package attachment

import (
	"bytes"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"recall/internal/document"
)

const (
	// FolderSuffix marks an attachment folder: <dir>/<base>.assets holds the
	// images of <dir>/<base>.md.
	FolderSuffix = ".assets"
	// MaxBytes is the largest image accepted.
	MaxBytes = 20 << 20
	maxStem  = 80
)

type Format struct {
	Extension string
	MIME      string
}

var (
	PNG  = Format{".png", "image/png"}
	JPEG = Format{".jpg", "image/jpeg"}
	GIF  = Format{".gif", "image/gif"}
	WebP = Format{".webp", "image/webp"}
)

// Detect identifies an accepted image format from its leading bytes.
func Detect(data []byte) (Format, bool) {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return PNG, true
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return JPEG, true
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return GIF, true
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return WebP, true
	}
	return Format{}, false
}

// ForExtension maps a filename extension, including its dot, to a format.
func ForExtension(ext string) (Format, bool) {
	switch strings.ToLower(ext) {
	case ".png":
		return PNG, true
	case ".jpg", ".jpeg":
		return JPEG, true
	case ".gif":
		return GIF, true
	case ".webp":
		return WebP, true
	}
	return Format{}, false
}

// Folder returns the content-relative attachment folder of a note path.
func Folder(notePath string) string {
	base := strings.TrimSuffix(filepath.Base(notePath), document.Suffix)
	return filepath.Join(filepath.Dir(notePath), base+FolderSuffix)
}

func IsFolderName(name string) bool { return strings.HasSuffix(name, FolderSuffix) }

// Within reports whether a content-relative path is an attachment folder or
// lies anywhere inside one.
func Within(path string) bool {
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if IsFolderName(part) {
			return true
		}
	}
	return false
}

// FileName derives a safe stored filename from an original name: directory
// parts and the original extension are dropped, runs of anything but letters,
// digits and underscores become one "-", and the detected format's extension
// is appended. n > 1 adds a "-n" collision suffix.
func FileName(original string, f Format, n int) string {
	base := filepath.Base(strings.ReplaceAll(original, "\\", "/"))
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	var b strings.Builder
	dash := false
	for _, r := range stem {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	runes := []rune(strings.TrimRight(b.String(), "-"))
	if len(runes) > maxStem {
		runes = runes[:maxStem]
	}
	name := strings.TrimRight(string(runes), "-")
	if name == "" {
		name = "image"
	}
	if n > 1 {
		name += "-" + strconv.Itoa(n)
	}
	return name + f.Extension
}

// Destination percent-encodes each segment of a relative path so it is a valid
// Markdown link destination even when names contain spaces or parentheses.
func Destination(rel string) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

// RewriteLinks repoints Markdown link and image destinations (inline, angle
// bracketed, and reference definitions) that start with oldFolder/ to
// newFolder/. Both the literal and percent-encoded forms of oldFolder match;
// the replacement is always encoded. Other text is untouched.
func RewriteLinks(body []byte, oldFolder, newFolder string) []byte {
	if oldFolder == newFolder {
		return body
	}
	replacement := Destination(newFolder) + "/"
	olds := []string{oldFolder}
	if encoded := Destination(oldFolder); encoded != oldFolder {
		olds = append(olds, encoded)
	}
	for _, old := range olds {
		for _, opener := range []string{"](", "](<", "]: ", "]: <"} {
			body = bytes.ReplaceAll(body, []byte(opener+old+"/"), []byte(opener+replacement))
		}
	}
	return body
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/attachment/ && gofmt -l internal/attachment`
Expected: `ok  recall/internal/attachment`, no gofmt output.

- [ ] **Step 5: Commit**

```bash
git add internal/attachment
git commit -m "Add attachment rules for note images

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Confined `Stat` and `List` in the store

**Files:**
- Modify: `internal/fsstore/store.go` (add after `Read`, around line 130)
- Test: `internal/fsstore/store_test.go` (append)

**Interfaces:**
- Consumes: existing `parentAt`, `statAt`, `openDirectoryAt`, `directoryNames`, `fileType`, `Entry`.
- Produces:
  - `func (s *Store) Stat(ctx context.Context, path string) (Entry, error)`: describes one path without following a final symlink; `not_found` when absent (including a missing parent).
  - `func (s *Store) List(ctx context.Context, path string) ([]Entry, error)`: immediate children in lexical order, each `Path` content-relative; `outside_root` if `path` or an ancestor is a symlink; `invalid_argument` if not a directory.

- [ ] **Step 1: Write the failing test**

Append to `internal/fsstore/store_test.go` (it already imports `os`, `path/filepath`, `reflect`, `model`; `openStore(t)` returns `(*Store, config.Config)`):

```go
func TestStatAndListDoNotFollowSymlinks(t *testing.T) {
	s, cfg := openStore(t)
	ctx := t.Context()
	dir := filepath.Join(cfg.Content, "plan.assets")
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.png"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cfg.Content, "linked.assets")); err != nil {
		t.Fatal(err)
	}

	got, err := s.List(ctx, "plan.assets")
	want := []Entry{
		{Path: "plan.assets/a.png"},
		{Path: "plan.assets/link", IsSymlink: true},
		{Path: "plan.assets/nested", IsDir: true},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("List = %+v, %v; want %+v", got, err, want)
	}
	if _, err := s.List(ctx, "linked.assets"); model.ErrorCode(err) != "outside_root" {
		t.Fatalf("List through symlink: %v", err)
	}
	if _, err := s.List(ctx, "plan.assets/a.png"); model.ErrorCode(err) != "invalid_argument" {
		t.Fatalf("List of a file: %v", err)
	}
	if _, err := s.List(ctx, "missing.assets"); model.ErrorCode(err) != "not_found" {
		t.Fatalf("List of missing: %v", err)
	}

	for path, want := range map[string]Entry{
		"plan.assets":       {Path: "plan.assets", IsDir: true},
		"plan.assets/a.png": {Path: "plan.assets/a.png"},
		"linked.assets":     {Path: "linked.assets", IsSymlink: true},
	} {
		if got, err := s.Stat(ctx, path); err != nil || got != want {
			t.Errorf("Stat(%q) = %+v, %v; want %+v", path, got, err, want)
		}
	}
	for _, path := range []string{"missing.assets", "missing/child.png"} {
		if _, err := s.Stat(ctx, path); model.ErrorCode(err) != "not_found" {
			t.Errorf("Stat(%q): %v", path, err)
		}
	}
	if _, err := s.Stat(ctx, "../escape"); model.ErrorCode(err) != "outside_root" {
		t.Errorf("Stat traversal: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/fsstore/ -run TestStatAndListDoNotFollowSymlinks`
Expected: FAIL to compile, `s.List undefined`.

- [ ] **Step 3: Implement**

Insert after `Read` in `internal/fsstore/store.go`:

```go
// Stat describes one content path without following a final symlink. A
// missing path or missing parent is not_found.
func (s *Store) Stat(ctx context.Context, path string) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	parent, name, err := parentAt(s.content, path)
	if err != nil {
		return Entry{}, err
	}
	defer parent.Close()
	stat, err := statAt(parent, name, path)
	if err != nil {
		return Entry{}, err
	}
	return Entry{Path: path, IsDir: fileType(stat) == unix.S_IFDIR, IsSymlink: fileType(stat) == unix.S_IFLNK}, nil
}

// List returns the immediate children of a content folder in lexical order,
// reporting symlinks without following them.
func (s *Store) List(ctx context.Context, path string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parent, name, err := parentAt(s.content, path)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	dir, err := openDirectoryAt(parent, name, path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	names, err := directoryNames(dir)
	if err != nil {
		return nil, pathError(path, err)
	}
	entries := []Entry{}
	for _, child := range names {
		childPath := filepath.Join(path, child)
		stat, err := statAt(dir, child, childPath)
		if err != nil {
			return nil, err
		}
		entries = append(entries, Entry{Path: childPath, IsDir: fileType(stat) == unix.S_IFDIR, IsSymlink: fileType(stat) == unix.S_IFLNK})
	}
	return entries, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/fsstore/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/fsstore/store.go internal/fsstore/store_test.go
git commit -m "Add confined Stat and List to the source store

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Scanner skips attachment folders

**Files:**
- Modify: `internal/scanner/reconcile.go` (inside the discovery loop in `reconcile`, before the `entry.IsSymlink` check, around line 74)
- Test: `internal/scanner/reconcile_test.go` (append)

**Interfaces:**
- Consumes: `attachment.Within(path string) bool` (Task 1).
- Produces: snapshots with no folders, documents or diagnostics from inside `*.assets` folders, for every client.

- [ ] **Step 1: Write the failing test**

```go
func TestAttachmentFoldersAreSilentlySkipped(t *testing.T) {
	s, cfg := newCollection(t)
	for _, dir := range []string{"plan.assets/nested", "Ideas/plan.assets"} {
		if err := os.MkdirAll(filepath.Join(cfg.Content, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	saveExternal(t, cfg, "plan.md", "plan\n")
	saveExternal(t, cfg, "plan.assets/shot.png", "\x89PNG\r\n\x1a\n")
	saveExternal(t, cfg, "plan.assets/inside.md", "not a note\n")
	saveExternal(t, cfg, "plan.assets/nested/deeper.txt", "x")
	saveExternal(t, cfg, "Ideas/plan.assets/b.png", "\x89PNG\r\n\x1a\n")
	if err := os.Symlink(t.TempDir(), filepath.Join(cfg.Content, "plan.assets", "link")); err != nil {
		t.Fatal(err)
	}
	got := reconcileLocked(t, s, New(s, func() string { return "fixed-id" }))
	if len(got.Documents) != 1 || got.Documents[0].Path != "plan.md" {
		t.Fatalf("documents: %+v", got.Documents)
	}
	if len(got.Diagnostics) != 0 {
		t.Fatalf("attachment contents produced diagnostics: %+v", got.Diagnostics)
	}
	if !reflect.DeepEqual(got.Folders, []string{".", "Ideas"}) {
		t.Fatalf("folders = %v", got.Folders)
	}
	if readSource(t, cfg, "plan.assets/inside.md") != "not a note\n" {
		t.Fatal("a Markdown file inside an attachment folder was adopted")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/scanner/ -run TestAttachmentFoldersAreSilentlySkipped`
Expected: FAIL (diagnostics for `plan.assets/shot.png` etc., and `plan.assets` in folders).

- [ ] **Step 3: Implement**

In `internal/scanner/reconcile.go`, add the import `"recall/internal/attachment"` and, as the first statement after the `ctx.Err()` check inside `for _, entry := range entries {`:

```go
		// Attachment folders hold a note's images; they are not part of the
		// note tree and their contents are neither indexed nor diagnosed.
		if attachment.Within(entry.Path) {
			continue
		}
```

Update the `Reconcile` doc comment's "with actual folders (including ".")" sentence to add: "Attachment folders (`*.assets`) and their contents are skipped."

- [ ] **Step 4: Run tests**

Run: `go test ./internal/scanner/ ./internal/app/ ./internal/desktop/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/scanner
git commit -m "Skip note attachment folders during reconciliation

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Save, count and read attachments in the app

**Files:**
- Create: `internal/app/attachments.go`
- Test: `internal/app/attachments_test.go`

**Interfaces:**
- Consumes: Task 1 (`attachment.*`), Task 2 (`Store.List`), existing `App.mutateDocument`, `App.Read`, `validatePath`, `appError`, `Store.Mkdir`, `Store.Write` (empty revision = create-only, returns `already_exists`), `Store.Read`.
- Produces:
  - `type Attachment struct { Path, Name, Link string }` (JSON `path`, `name`, `link`)
  - `func (a *App) SaveAttachment(ctx context.Context, ref model.Ref, name string, data []byte) (Attachment, error)`
  - `func (a *App) AttachmentCount(ctx context.Context, ref model.Ref) (int, error)`
  - `func (a *App) ReadAttachment(ctx context.Context, path string) ([]byte, attachment.Format, error)`
  - unexported `func (a *App) countAttachments(ctx context.Context, folder string) (int, error)` for Task 5.

- [ ] **Step 1: Write the failing tests**

```go
package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"recall/internal/attachment"
	"recall/internal/model"
)

var testPNG = []byte("\x89PNG\r\n\x1a\nimage-bytes")

func newNote(t *testing.T, a *App, folder, name, body string) model.Document {
	t.Helper()
	if folder != "" {
		if err := a.CreateFolder(t.Context(), folder); err != nil && model.ErrorCode(err) != "already_exists" {
			t.Fatal(err)
		}
	}
	return mustCreate(t, a, CreateInput{Folder: folder, Name: name, Body: body})
}

func TestSaveAttachmentStoresBesideTheNote(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	ctx := t.Context()
	note := newNote(t, a, "Ideas", "plan", "body\n")
	if n, err := a.AttachmentCount(ctx, model.Ref{ID: note.ID}); err != nil || n != 0 {
		t.Fatalf("count before = %d, %v", n, err)
	}
	first, err := a.SaveAttachment(ctx, model.Ref{ID: note.ID}, "Screen Shot.png", testPNG)
	want := Attachment{Path: "Ideas/plan.assets/Screen-Shot.png", Name: "Screen-Shot.png", Link: "plan.assets/Screen-Shot.png"}
	if err != nil || first != want {
		t.Fatalf("first = %+v, %v; want %+v", first, err, want)
	}
	stored, err := os.ReadFile(filepath.Join(cfg.Content, first.Path))
	if err != nil || !bytes.Equal(stored, testPNG) {
		t.Fatalf("stored bytes: %v", err)
	}
	second, err := a.SaveAttachment(ctx, model.Ref{ID: note.ID}, "Screen Shot.jpg", testPNG)
	if err != nil || second.Name != "Screen-Shot-2.png" {
		t.Fatalf("collision = %+v, %v", second, err)
	}
	if n, err := a.AttachmentCount(ctx, model.Ref{ID: note.ID}); err != nil || n != 2 {
		t.Fatalf("count after = %d, %v", n, err)
	}
	after, err := a.Read(ctx, model.Ref{ID: note.ID})
	if err != nil || after.Raw != note.Raw {
		t.Fatalf("note changed: %v", err)
	}
	folders, err := a.Folders(ctx)
	if err != nil || len(folders) != 2 {
		t.Fatalf("attachment folder listed: %v %v", folders, err)
	}
}

func TestSaveAttachmentEncodesSpacedNoteNames(t *testing.T) {
	a, _ := newTestApp(t)
	defer a.Close()
	note := newNote(t, a, "", "my plan", "")
	got, err := a.SaveAttachment(t.Context(), model.Ref{ID: note.ID}, "", testPNG)
	if err != nil || got.Link != "my%20plan.assets/image.png" || got.Path != "my plan.assets/image.png" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestSaveAttachmentRejectsUnsafeInput(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	ctx := t.Context()
	note := newNote(t, a, "", "plan", "")
	ref := model.Ref{ID: note.ID}
	_, err := a.SaveAttachment(ctx, ref, "x.png", []byte("<svg/>"))
	wantCode(t, err, "invalid_argument")
	big := append(append([]byte{}, testPNG...), make([]byte, attachment.MaxBytes)...)
	_, err = a.SaveAttachment(ctx, ref, "x.png", big)
	wantCode(t, err, "invalid_argument")
	_, err = a.SaveAttachment(ctx, model.Ref{ID: "missing"}, "x.png", testPNG)
	wantCode(t, err, "not_found")

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(cfg.Content, "plan.assets")); err != nil {
		t.Fatal(err)
	}
	_, err = a.SaveAttachment(ctx, ref, "x.png", testPNG)
	wantCode(t, err, "outside_root")
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("wrote through symlink: %v", entries)
	}

	other := newNote(t, a, "", "other", "")
	if err := os.WriteFile(filepath.Join(cfg.Content, "other.assets"), []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = a.SaveAttachment(ctx, model.Ref{ID: other.ID}, "x.png", testPNG)
	wantCode(t, err, "invalid_argument")
}

func TestReadAttachmentServesOnlyGenuineImagesInAssets(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	ctx := t.Context()
	note := newNote(t, a, "Ideas", "plan", "")
	saved, err := a.SaveAttachment(ctx, model.Ref{ID: note.ID}, "shot.png", testPNG)
	if err != nil {
		t.Fatal(err)
	}
	data, format, err := a.ReadAttachment(ctx, saved.Path)
	if err != nil || format != attachment.PNG || !bytes.Equal(data, testPNG) {
		t.Fatalf("read = %v, %v", format, err)
	}
	assets := filepath.Join(cfg.Content, "Ideas", "plan.assets")
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(assets, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("fake.png", []byte("not an image"))
	write("wrong.jpg", testPNG)
	write("vector.svg", []byte("<svg/>"))
	if err := os.WriteFile(filepath.Join(cfg.Content, "loose.png"), testPNG, 0600); err != nil {
		t.Fatal(err)
	}
	outsidePNG := filepath.Join(t.TempDir(), "x.png")
	if err := os.WriteFile(outsidePNG, testPNG, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePNG, filepath.Join(assets, "link.png")); err != nil {
		t.Fatal(err)
	}
	for path, code := range map[string]string{
		"Ideas/plan.md":                 "not_found",
		"Ideas/plan.assets/fake.png":    "not_found",
		"Ideas/plan.assets/wrong.jpg":   "not_found",
		"Ideas/plan.assets/vector.svg":  "not_found",
		"loose.png":                     "not_found",
		"Ideas/plan.assets/missing.png": "not_found",
		"Ideas/plan.assets/link.png":    "outside_root",
		"../config.json":                "outside_root",
		"Ideas/plan.assets/../../x.png": "outside_root",
	} {
		_, _, err := a.ReadAttachment(ctx, path)
		if model.ErrorCode(err) != code {
			t.Errorf("ReadAttachment(%q) = %v, want %s", path, err, code)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/app/ -run 'Attachment'`
Expected: FAIL to compile, `a.SaveAttachment undefined`.

- [ ] **Step 3: Implement `internal/app/attachments.go`**

```go
package app

import (
	"context"
	"path/filepath"

	"recall/internal/attachment"
	"recall/internal/model"
)

const maxNameAttempts = 999

// Attachment is a stored image and the Markdown destination that links to it
// from its note's folder.
type Attachment struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Link string `json:"link"`
}

// SaveAttachment stores an image in the note's attachment folder, creating
// the folder if needed. The format comes from the bytes, the name is
// sanitised, and an existing file is never overwritten: clashes get -2, -3, ...
func (a *App) SaveAttachment(ctx context.Context, ref model.Ref, name string, data []byte) (Attachment, error) {
	if len(data) > attachment.MaxBytes {
		return Attachment{}, appError("invalid_argument", "", "images must be 20 MB or smaller")
	}
	format, ok := attachment.Detect(data)
	if !ok {
		return Attachment{}, appError("invalid_argument", "", "only PNG, JPEG, GIF and WebP images are supported")
	}
	var saved Attachment
	_, err := a.mutateDocument(ctx, ref, "", func(note model.Document) (model.Document, error) {
		folder := attachment.Folder(note.Path)
		// Mkdir refuses a symlink (outside_root). An existing regular file
		// reports already_exists here and is refused by Write's no-follow
		// directory open below (invalid_argument).
		if err := a.store.Mkdir(ctx, folder); err != nil && model.ErrorCode(err) != "already_exists" {
			return model.Document{}, err
		}
		for n := 1; n <= maxNameAttempts; n++ {
			file := attachment.FileName(name, format, n)
			path := filepath.Join(folder, file)
			err := a.store.Write(ctx, path, "", data)
			if model.ErrorCode(err) == "already_exists" {
				continue
			}
			if err != nil {
				return model.Document{}, err
			}
			saved = Attachment{Path: path, Name: file, Link: attachment.Destination(filepath.Join(filepath.Base(folder), file))}
			return note, nil
		}
		return model.Document{}, appError("already_exists", folder, "too many images share this name")
	})
	return saved, err
}

// AttachmentCount counts the files directly inside a note's attachment folder.
func (a *App) AttachmentCount(ctx context.Context, ref model.Ref) (int, error) {
	note, err := a.Read(ctx, ref)
	if err != nil {
		return 0, err
	}
	return a.countAttachments(ctx, attachment.Folder(note.Path))
}

func (a *App) countAttachments(ctx context.Context, folder string) (int, error) {
	entries, err := a.store.List(ctx, folder)
	if model.ErrorCode(err) == "not_found" {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir && !entry.IsSymlink {
			count++
		}
	}
	return count, nil
}

// ReadAttachment returns an image stored directly inside an attachment folder.
// It reads through the confined, no-follow store and requires the bytes to
// match an accepted format and the file's extension, so nothing but genuine
// images in .assets folders is ever returned.
func (a *App) ReadAttachment(ctx context.Context, path string) ([]byte, attachment.Format, error) {
	if err := validatePath(path); err != nil {
		return nil, attachment.Format{}, err
	}
	notAttachment := appError("not_found", path, "not a note image")
	if !attachment.IsFolderName(filepath.Base(filepath.Dir(path))) {
		return nil, attachment.Format{}, notAttachment
	}
	want, ok := attachment.ForExtension(filepath.Ext(path))
	if !ok {
		return nil, attachment.Format{}, notAttachment
	}
	data, err := a.store.Read(ctx, path)
	if err != nil {
		return nil, attachment.Format{}, err
	}
	if got, ok := attachment.Detect(data); !ok || got != want {
		return nil, attachment.Format{}, notAttachment
	}
	return data, want, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/app/`
Expected: PASS. If `TestSaveAttachmentRejectsUnsafeInput`'s regular-file case returns a different code, read the actual code path (`Write` → `parentAt` → `openDirectoryAt`) and fix the implementation, not the test, unless the code is still a refusal that writes nothing (then update the expected code and say so in the commit message).

- [ ] **Step 5: Commit**

```bash
git add internal/app/attachments.go internal/app/attachments_test.go
git commit -m "Save, count and read note image attachments

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Delete and move carry attachments

**Files:**
- Modify: `internal/app/documents.go` (`Move` at line 102, `Delete` at line 118)
- Modify: `internal/app/attachments.go` (helpers)
- Modify: `internal/mcpserver/tools.go:144,147` (descriptions)
- Test: `internal/app/attachments_test.go` (append)

**Interfaces:**
- Consumes: Task 1 (`Folder`, `RewriteLinks`), Task 2 (`Store.Stat`), existing `Store.Remove(ctx, path, recursive)`, `Store.Move`, `Store.Write`, `document.Parse`, `document.Encode`.
- Produces: `App.Delete` returns `partial_failure` (path = attachment folder) when the note was deleted but its folder was not; `App.Move` returns `conflict` when the destination attachment folder exists, before changing anything.

- [ ] **Step 1: Write the failing tests**

```go
func TestDeleteRemovesAttachments(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	ctx := t.Context()
	note := newNote(t, a, "Ideas", "plan", "")
	if _, err := a.SaveAttachment(ctx, model.Ref{ID: note.ID}, "a.png", testPNG); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(ctx, model.Ref{ID: note.ID}, note.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(cfg.Content, "Ideas", "plan.assets")); !os.IsNotExist(err) {
		t.Fatalf("attachment folder survived: %v", err)
	}
	plain := newNote(t, a, "", "plain", "")
	if err := a.Delete(ctx, model.Ref{ID: plain.ID}, plain.Revision); err != nil {
		t.Fatalf("delete without attachments: %v", err)
	}
}

func TestDeleteReportsUnremovableAttachments(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	ctx := t.Context()
	note := newNote(t, a, "", "plan", "")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep.png"), testPNG, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cfg.Content, "plan.assets")); err != nil {
		t.Fatal(err)
	}
	err := a.Delete(ctx, model.Ref{ID: note.ID}, note.Revision)
	wantCode(t, err, "partial_failure")
	if _, statErr := os.Stat(filepath.Join(cfg.Content, "plan.md")); !os.IsNotExist(statErr) {
		t.Fatal("note was not deleted")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "keep.png")); statErr != nil {
		t.Fatal("followed the symlink")
	}
	if s, _ := a.List(ctx, "", true); len(s.Documents) != 0 {
		t.Fatalf("index not resynchronised: %+v", s.Documents)
	}
}

func TestMoveCarriesAttachments(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	ctx := t.Context()
	body := "![a](plan.assets/a.png)\n[file](plan.assets.md) [web](https://x.example/plan.assets/a.png)\n"
	note := newNote(t, a, "Ideas", "plan", body)
	if _, err := a.SaveAttachment(ctx, model.Ref{ID: note.ID}, "a.png", testPNG); err != nil {
		t.Fatal(err)
	}
	if err := a.CreateFolder(ctx, "Archive"); err != nil {
		t.Fatal(err)
	}
	moved, err := a.Move(ctx, model.Ref{ID: note.ID}, "Archive/plan.md", note.Revision)
	if err != nil || moved.Raw != note.Raw {
		t.Fatalf("plain move: %+v %v", moved, err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Content, "Archive", "plan.assets", "a.png")); err != nil {
		t.Fatalf("attachments did not move: %v", err)
	}
	renamed, err := a.Move(ctx, model.Ref{ID: note.ID}, "Archive/roadmap.md", moved.Revision)
	if err != nil {
		t.Fatal(err)
	}
	wantBody := "![a](roadmap.assets/a.png)\n[file](plan.assets.md) [web](https://x.example/plan.assets/a.png)\n"
	if renamed.Body != wantBody || renamed.ID != note.ID || renamed.Title != note.Title {
		t.Fatalf("rename = %+v", renamed)
	}
	if _, err := os.Stat(filepath.Join(cfg.Content, "Archive", "roadmap.assets", "a.png")); err != nil {
		t.Fatalf("attachments not renamed: %v", err)
	}
}

func TestMoveRenamesSpacedAttachmentLinks(t *testing.T) {
	a, _ := newTestApp(t)
	defer a.Close()
	ctx := t.Context()
	note := newNote(t, a, "", "my plan", "![a](my%20plan.assets/a.png) ![b](<my plan.assets/b.png>)")
	if _, err := a.SaveAttachment(ctx, model.Ref{ID: note.ID}, "a.png", testPNG); err != nil {
		t.Fatal(err)
	}
	got, err := a.Move(ctx, model.Ref{ID: note.ID}, "new name.md", note.Revision)
	if err != nil || got.Body != "![a](new%20name.assets/a.png) ![b](<new%20name.assets/b.png>)" {
		t.Fatalf("got %q, %v", got.Body, err)
	}
}

func TestMoveRefusesExistingDestinationAttachments(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	ctx := t.Context()
	note := newNote(t, a, "", "plan", "")
	if _, err := a.SaveAttachment(ctx, model.Ref{ID: note.ID}, "a.png", testPNG); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cfg.Content, "roadmap.assets"), 0755); err != nil {
		t.Fatal(err)
	}
	_, err := a.Move(ctx, model.Ref{ID: note.ID}, "roadmap.md", note.Revision)
	wantCode(t, err, "conflict")
	for _, path := range []string{"plan.md", "plan.assets/a.png"} {
		if _, err := os.Stat(filepath.Join(cfg.Content, path)); err != nil {
			t.Fatalf("%s changed: %v", path, err)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/app/ -run 'Delete|Move'`
Expected: the new tests FAIL (attachment folder survives; links not rewritten; no conflict).

- [ ] **Step 3: Implement helpers in `internal/app/attachments.go`**

Add imports `"bytes"` and `"recall/internal/document"`, then:

```go
// removeAttachments deletes a deleted note's attachment folder. A missing
// folder or a regular file of that name is left alone; anything that cannot be
// removed (including a symlink, which is never followed) is a partial failure.
func (a *App) removeAttachments(ctx context.Context, notePath string) error {
	folder := attachment.Folder(notePath)
	entry, err := a.store.Stat(ctx, folder)
	if model.ErrorCode(err) == "not_found" {
		return nil
	}
	if err == nil && !entry.IsDir && !entry.IsSymlink {
		return nil
	}
	if err == nil && entry.IsSymlink {
		err = appError("outside_root", folder, "symlinks are not allowed")
	}
	if err == nil {
		err = a.store.Remove(ctx, folder, true)
	}
	if err == nil {
		return nil
	}
	return appError("partial_failure", folder, "note deleted, but its attached images could not be removed: "+model.PublicError(err).Message)
}

// attachmentsToCarry reports whether a note's attachment folder must move with
// it, refusing the move before anything changes when it cannot.
func (a *App) attachmentsToCarry(ctx context.Context, from, to string) (bool, error) {
	entry, err := a.store.Stat(ctx, from)
	if model.ErrorCode(err) == "not_found" {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if entry.IsSymlink {
		return false, appError("outside_root", from, "symlinks are not allowed")
	}
	if !entry.IsDir {
		return false, nil
	}
	if _, err := a.store.Stat(ctx, to); err == nil {
		return false, appError("conflict", to, "the destination already has an attachments folder")
	} else if model.ErrorCode(err) != "not_found" {
		return false, err
	}
	return true, nil
}

// rewriteAttachmentLinks repoints body links from one attachment folder name
// to another, leaving frontmatter and all other text byte-for-byte intact.
func rewriteAttachmentLinks(path string, raw []byte, from, to string) ([]byte, bool, error) {
	if from == to {
		return raw, false, nil
	}
	p, err := document.Parse(path, raw)
	if err != nil {
		return nil, false, err
	}
	body := attachment.RewriteLinks(p.Body, from, to)
	if bytes.Equal(body, p.Body) {
		return raw, false, nil
	}
	p.Body = body
	out, err := document.Encode(p)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}
```

- [ ] **Step 4: Change `Move` and `Delete` in `internal/app/documents.go`**

Add imports `"errors"` and `"recall/internal/attachment"`. Replace `Move`:

```go
// Move takes a complete content-relative .md destination filename. The note's
// attachment folder moves with it; on a rename its body links are repointed.
func (a *App) Move(ctx context.Context, ref model.Ref, destination, expectedRevision string) (model.Document, error) {
	if err := validatePath(destination); err != nil {
		return model.Document{}, err
	}
	return a.mutateDocument(ctx, ref, expectedRevision, func(current model.Document) (model.Document, error) {
		if err := document.ValidatePath(destination); err != nil {
			return model.Document{}, err
		}
		from, to := attachment.Folder(current.Path), attachment.Folder(destination)
		carry, err := a.attachmentsToCarry(ctx, from, to)
		if err != nil {
			return model.Document{}, err
		}
		if err := a.store.Move(ctx, current.Path, destination); err != nil {
			return model.Document{}, err
		}
		raw := []byte(current.Raw)
		if carry {
			if err := a.store.Move(ctx, from, to); err != nil {
				// Put the note back rather than separate it from its images.
				return model.Document{}, errors.Join(err, a.store.Move(ctx, destination, current.Path))
			}
			rewritten, changed, err := rewriteAttachmentLinks(destination, raw, filepath.Base(from), filepath.Base(to))
			if err != nil {
				return model.Document{}, err
			}
			if changed {
				if err := a.store.Write(ctx, destination, current.Revision, rewritten); err != nil {
					return model.Document{}, err
				}
				raw = rewritten
			}
		}
		p, err := document.Parse(destination, raw)
		return p.Document, err
	})
}
```

In `Delete`, replace `return model.Document{}, a.store.Remove(ctx, current.Path, false)` and the trailing `return err` so the partial failure is returned only after `mutate` resynchronises the index:

```go
	var partial error
	_, err := a.mutate(ctx, func(snapshot model.Snapshot) (model.Document, error) {
		// ... existing resolution and revision check unchanged ...
		if err := a.store.Remove(ctx, current.Path, false); err != nil {
			return model.Document{}, err
		}
		partial = a.removeAttachments(ctx, current.Path)
		return model.Document{}, nil
	})
	if err != nil {
		return err
	}
	return partial
```

(`var partial error` goes before the `a.mutate` call; keep the existing body between the comment markers.)

- [ ] **Step 5: Update MCP tool descriptions**

In `internal/mcpserver/tools.go`:
- line 144: `"Move a document to a full content-relative .md filename; preserves ID. Its <name>.assets image folder moves with it, and a rename repoints the note's links to it."`
- line 147: `"Permanently delete a document and its <name>.assets image folder immediately, without confirmation or trash."`

- [ ] **Step 6: Run tests**

Run: `go test ./...`
Expected: PASS, including the existing `TestDocumentCRUDAndDirectRefresh` (moves without attachments keep `Raw` identical) and the MCP tests.

- [ ] **Step 7: Commit**

```bash
git add internal/app internal/mcpserver/tools.go
git commit -m "Delete and move note attachments with their notes

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Desktop bindings and the confined image route

**Files:**
- Create: `internal/desktop/images.go`
- Modify: `internal/desktop/types.go`, `internal/desktop/service.go` (the `Application` interface), `internal/desktop/bindings.go`
- Modify: `desktop/main.go:60`
- Test: `internal/desktop/images_test.go`

**Interfaces:**
- Consumes: Task 4/5 app methods; existing `Service.mu`, `Service.check`, `problem`, `invalid`, `fixture(t)` test helper in `documents_test.go` (returns `(*Service, *app.App, config.Config, session string)`).
- Produces (Go):
  - `type SaveImageInput struct { Session, ID, Name, Data string }` (JSON `session`, `id`, `name`, `data`; `data` is standard base64)
  - `type SaveImageReply struct { Name, Link string; Problem *Problem }` (JSON `name`, `link`, `problem`)
  - `type AttachmentsReply struct { Count int; Problem *Problem }` (JSON `count`, `problem`)
  - `func (s *Service) SaveImage(SaveImageInput) SaveImageReply`, `func (s *Service) Attachments(session, id string) AttachmentsReply`
  - `func ImageHandler(s *Service) http.Handler` serving `GET /note-image/<escaped content-relative path>`
  - Bound: `API.SaveImage`, `API.Attachments` (frontend calls `window.go.desktop.API.SaveImage(input)` and `.Attachments(session, id)`).

- [ ] **Step 1: Write the failing tests**

```go
package desktop

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"recall/internal/app"
	"recall/internal/attachment"
)

var desktopPNG = []byte("\x89PNG\r\n\x1a\ndesktop-image")

func TestSaveImageAndCount(t *testing.T) {
	s, a, _, session := fixture(t)
	if err := a.CreateFolder(t.Context(), "Ideas"); err != nil {
		t.Fatal(err)
	}
	note, err := a.Create(t.Context(), app.CreateInput{Folder: "Ideas", Name: "plan"})
	if err != nil {
		t.Fatal(err)
	}
	data := base64.StdEncoding.EncodeToString(desktopPNG)
	r := s.SaveImage(SaveImageInput{Session: session, ID: note.ID, Name: "shot.png", Data: data})
	if r.Problem != nil || r.Name != "shot.png" || r.Link != "plan.assets/shot.png" {
		t.Fatalf("save = %+v", r)
	}
	if c := s.Attachments(session, note.ID); c.Problem != nil || c.Count != 1 {
		t.Fatalf("count = %+v", c)
	}
	for name, in := range map[string]SaveImageInput{
		"stale session": {Session: "old", ID: note.ID, Name: "x.png", Data: data},
		"no document":   {Session: session, Name: "x.png", Data: data},
		"bad base64":    {Session: session, ID: note.ID, Name: "x.png", Data: "***"},
		"too large":     {Session: session, ID: note.ID, Name: "x.png", Data: strings.Repeat("A", base64.StdEncoding.EncodedLen(attachment.MaxBytes)+4)},
		"not an image":  {Session: session, ID: note.ID, Name: "x.png", Data: base64.StdEncoding.EncodeToString([]byte("<svg/>"))},
	} {
		if got := s.SaveImage(in); got.Problem == nil || got.Link != "" {
			t.Errorf("%s accepted: %+v", name, got)
		}
	}
	if c := s.Attachments("old", note.ID); c.Problem == nil {
		t.Error("stale session counted")
	}
}

func TestImageHandlerServesOnlyConfinedImages(t *testing.T) {
	s, a, cfg, session := fixture(t)
	if err := a.CreateFolder(t.Context(), "Ideas"); err != nil {
		t.Fatal(err)
	}
	note, err := a.Create(t.Context(), app.CreateInput{Folder: "Ideas", Name: "my plan"})
	if err != nil {
		t.Fatal(err)
	}
	if r := s.SaveImage(SaveImageInput{Session: session, ID: note.ID, Name: "shot.png", Data: base64.StdEncoding.EncodeToString(desktopPNG)}); r.Problem != nil {
		t.Fatal(r.Problem)
	}
	assets := filepath.Join(cfg.Content, "Ideas", "my plan.assets")
	if err := os.WriteFile(filepath.Join(assets, "fake.png"), []byte("text"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "x.png")
	if err := os.WriteFile(outside, desktopPNG, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(assets, "link.png")); err != nil {
		t.Fatal(err)
	}
	handler := ImageHandler(s)
	get := func(method, target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(method, target, nil))
		return w
	}

	ok := get(http.MethodGet, "/note-image/Ideas/my%20plan.assets/shot.png")
	if ok.Code != http.StatusOK || !bytes.Equal(ok.Body.Bytes(), desktopPNG) {
		t.Fatalf("GET = %d %q", ok.Code, ok.Body.String())
	}
	for header, want := range map[string]string{"Content-Type": "image/png", "X-Content-Type-Options": "nosniff", "Cache-Control": "no-cache"} {
		if got := ok.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	for _, target := range []string{
		"/note-image/Ideas/my%20plan.md",
		"/note-image/Ideas/my%20plan.assets/fake.png",
		"/note-image/Ideas/my%20plan.assets/link.png",
		"/note-image/Ideas/my%20plan.assets/missing.png",
		"/note-image/%2e%2e/config.json",
		"/note-image/..%2F..%2Fconfig.json",
		"/note-image/Ideas/my%20plan.assets/..%2F..%2F..%2Fconfig.json",
		"/note-image/",
		"/other/Ideas/my%20plan.assets/shot.png",
	} {
		if w := get(http.MethodGet, target); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d", target, w.Code)
		}
	}
	// httptest.NewRequest panics on an invalid escape, so build this one by hand.
	malformed := httptest.NewRecorder()
	handler.ServeHTTP(malformed, &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/note-image/x", RawPath: "/note-image/Ideas/my%zzplan.assets/shot.png"}})
	if malformed.Code != http.StatusNotFound {
		t.Errorf("malformed escape = %d", malformed.Code)
	}
	if w := get(http.MethodPost, "/note-image/Ideas/my%20plan.assets/shot.png"); w.Code != http.StatusNotFound {
		t.Errorf("POST = %d", w.Code)
	}
	closed := NewService(nil, nil)
	w := httptest.NewRecorder()
	ImageHandler(closed).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/note-image/Ideas/my%20plan.assets/shot.png", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("no collection = %d", w.Code)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/desktop/ -run 'SaveImage|ImageHandler'`
Expected: FAIL to compile, `s.SaveImage undefined`.

- [ ] **Step 3: Add types, interface methods and bindings**

`internal/desktop/types.go`, append:

```go
type SaveImageInput struct {
	Session string `json:"session"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Data    string `json:"data"`
}
type SaveImageReply struct {
	Name    string   `json:"name"`
	Link    string   `json:"link"`
	Problem *Problem `json:"problem"`
}
type AttachmentsReply struct {
	Count   int      `json:"count"`
	Problem *Problem `json:"problem"`
}
```

`internal/desktop/service.go`, in `type Application interface`, add (and import `"recall/internal/attachment"`):

```go
	SaveAttachment(context.Context, model.Ref, string, []byte) (app.Attachment, error)
	AttachmentCount(context.Context, model.Ref) (int, error)
	ReadAttachment(context.Context, string) ([]byte, attachment.Format, error)
```

`internal/desktop/bindings.go`, append:

```go
func (a *API) SaveImage(input SaveImageInput) SaveImageReply { return a.service.SaveImage(input) }
func (a *API) Attachments(session, id string) AttachmentsReply {
	return a.service.Attachments(session, id)
}
```

- [ ] **Step 4: Implement `internal/desktop/images.go`**

```go
package desktop

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"recall/internal/attachment"
	"recall/internal/model"
)

const imageRoute = "/note-image/"

// SaveImage stores a dropped or pasted image beside a note and returns the
// Markdown destination to insert. Data is standard base64.
func (s *Service) SaveImage(in SaveImageInput) SaveImageReply {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p := s.check(in.Session); p != nil {
		return SaveImageReply{Problem: p}
	}
	if in.ID == "" {
		return SaveImageReply{Problem: invalid("A document is required")}
	}
	if len(in.Data) > base64.StdEncoding.EncodedLen(attachment.MaxBytes) {
		return SaveImageReply{Problem: invalid("images must be 20 MB or smaller")}
	}
	data, err := base64.StdEncoding.DecodeString(in.Data)
	if err != nil {
		return SaveImageReply{Problem: invalid("the image data could not be read")}
	}
	saved, err := s.application.SaveAttachment(s.ctx, model.Ref{ID: in.ID}, in.Name, data)
	if err != nil {
		return SaveImageReply{Problem: problem(err)}
	}
	return SaveImageReply{Name: saved.Name, Link: saved.Link}
}

// Attachments counts a note's stored images for the delete confirmation.
func (s *Service) Attachments(session, id string) AttachmentsReply {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p := s.check(session); p != nil {
		return AttachmentsReply{Problem: p}
	}
	n, err := s.application.AttachmentCount(s.ctx, model.Ref{ID: id})
	if err != nil {
		return AttachmentsReply{Problem: problem(err)}
	}
	return AttachmentsReply{Count: n}
}

// ImageHandler serves note images to the webview at /note-image/<path>. Wails
// uses it as the asset server's fallback, so it only sees requests the embedded
// frontend cannot satisfy, and only from the app's own webview. Anything other
// than a GET for a genuine image directly inside a .assets folder of the open
// collection is a plain 404.
func ImageHandler(s *Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, format, ok := s.image(r)
		if !ok {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", format.MIME)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-cache")
		_, _ = w.Write(data)
	})
}

func (s *Service) image(r *http.Request) ([]byte, attachment.Format, bool) {
	if r.Method != http.MethodGet {
		return nil, attachment.Format{}, false
	}
	escaped, found := strings.CutPrefix(r.URL.EscapedPath(), imageRoute)
	if !found {
		return nil, attachment.Format{}, false
	}
	path, err := url.PathUnescape(escaped)
	if err != nil {
		return nil, attachment.Format{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.application == nil {
		return nil, attachment.Format{}, false
	}
	data, format, err := s.application.ReadAttachment(s.ctx, filepath.FromSlash(path))
	return data, format, err == nil
}
```

- [ ] **Step 5: Register the handler**

`desktop/main.go:60`: change `AssetServer: &assetserver.Options{Assets: assets},` to:

```go
		// The fallback handler serves note images only; see dto.ImageHandler.
		AssetServer: &assetserver.Options{Assets: assets, Handler: dto.ImageHandler(service)},
```

- [ ] **Step 6: Run tests**

Run: `go test ./... && (cd desktop/frontend && npm ci && npm run build) && (cd desktop && go vet ./... && go test ./...)`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/desktop desktop/main.go
git commit -m "Expose image saving and a confined image route to the desktop app

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Frontend API surface and delete confirmation

**Files:**
- Modify: `desktop/frontend/src/api/types.ts`, `src/api/bridge.ts`, `src/api/fakeBridge.ts`
- Modify: `desktop/frontend/src/tree/DeleteDialog.tsx`, `src/App.tsx` (DeleteDialog usage ~line 425, `remove()` at line 226)
- Test: `desktop/frontend/src/tree/DeleteDialog.test.tsx` (new), `src/App.test.tsx` (append)

**Interfaces:**
- Consumes: Task 6 bindings `SaveImage(input)`, `Attachments(session, id)`.
- Produces (TS):
  - `interface SaveImageInput { session: string; id: string; name: string; data: string }`
  - `interface SaveImageReply { name: string; link: string; problem: Problem | null }`
  - `interface AttachmentsReply { count: number; problem: Problem | null }`
  - `DesktopAPI.saveImage(input: SaveImageInput): Promise<SaveImageReply>`, `DesktopAPI.attachments(session: string, id: string): Promise<AttachmentsReply>`
  - `fakeBridge` implements both: stores names per note, returns `link = encodeURIComponent(doc.name) + ".assets/" + name`, forces `.png`, adds `-2`, `-3` on clashes; `data === ""` → problem `invalid_argument` "the image data could not be read"; unknown note → `not_found` "Document not found".
  - `DeleteDialog` prop `attachments?: () => Promise<number>`.

- [ ] **Step 1: Write failing tests**

`desktop/frontend/src/tree/DeleteDialog.test.tsx`:

```tsx
import { act, render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { DeleteDialog } from "./DeleteDialog";
const note = { id: "a", path: "a.md", name: "a", kind: "note" };
const noop = async () => {};
it("says how many attached images a note deletion removes", async () => {
  render(
    <DeleteDialog
      entry={note}
      attachments={async () => 3}
      onDelete={noop}
      onCancel={() => {}}
    />,
  );
  expect(
    await screen.findByText(/Also deletes 3 attached images\./),
  ).toBeInTheDocument();
});
it("uses the singular for one image", async () => {
  render(
    <DeleteDialog
      entry={note}
      attachments={async () => 1}
      onDelete={noop}
      onCancel={() => {}}
    />,
  );
  expect(
    await screen.findByText(/Also deletes 1 attached image\./),
  ).toBeInTheDocument();
});
it("adds nothing when a note has no images or the count fails", async () => {
  for (const attachments of [
    async () => 0,
    async () => {
      throw new Error("gone");
    },
  ]) {
    const { unmount } = render(
      <DeleteDialog
        entry={note}
        attachments={attachments}
        onDelete={noop}
        onCancel={() => {}}
      />,
    );
    await act(async () => {});
    expect(screen.queryByText(/attached image/)).toBeNull();
    unmount();
  }
});
```

Append to `desktop/frontend/src/App.test.tsx` (reuse its existing `doc`, `host` and imports; add `waitFor` to the import if missing):

```tsx
it("treats a partial attachment failure as a completed delete and shows why", async () => {
  const api = fakeBridge([doc], ["Work"]);
  api.delete = async () => ({
    problem: {
      code: "partial_failure",
      message: "note deleted, but its attached images could not be removed",
      path: "Work/x.assets",
    },
  });
  render(<App api={api} host={host} />);
  fireEvent.click(await screen.findByRole("treeitem", { name: "Work" }));
  fireEvent.click(screen.getByRole("button", { name: "Delete selection" }));
  fireEvent.click(
    await screen.findByRole("button", { name: "Delete permanently" }),
  );
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "attached images could not be removed",
  );
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd desktop/frontend && npm test -- DeleteDialog App`
Expected: FAIL (no "Also deletes" text; dialog stays open with the error).

- [ ] **Step 3: Implement API types, bridge and fake**

`src/api/types.ts`: add the three interfaces and, in `DesktopAPI`:

```ts
  saveImage(input: SaveImageInput): Promise<SaveImageReply>;
  attachments(session: string, id: string): Promise<AttachmentsReply>;
```

`src/api/bridge.ts`, inside `api`:

```ts
  saveImage: (input) => backend().SaveImage(input),
  attachments: (session, id) => backend().Attachments(session, id),
```

`src/api/fakeBridge.ts`: declare `const images = new Map<string, string[]>();` beside `docs`, and add to the returned object:

```ts
    saveImage: async (input) => {
      const doc = docs.get(input.id);
      if (!doc)
        return {
          name: "",
          link: "",
          problem: issue("not_found", "Document not found"),
        };
      if (!input.data)
        return {
          name: "",
          link: "",
          problem: issue("invalid_argument", "the image data could not be read"),
        };
      const stored = images.get(input.id) ?? [],
        stem = input.name.replace(/\.[^.]*$/, "") || "image";
      let name = stem + ".png";
      for (let n = 2; stored.includes(name); n++) name = `${stem}-${n}.png`;
      images.set(input.id, [...stored, name]);
      return {
        name,
        link: encodeURIComponent(doc.name) + ".assets/" + name,
        problem: null,
      };
    },
    attachments: async (_session, id) => ({
      count: images.get(id)?.length ?? 0,
      problem: null,
    }),
```

Run `npm run typecheck`; if any test file builds a `DesktopAPI` literal by hand, add the two methods there too (as `async () => ({ name: "", link: "", problem: null })` / `async () => ({ count: 0, problem: null })`).

- [ ] **Step 4: Implement the dialog count**

`src/tree/DeleteDialog.tsx`: import `useEffect`; add the prop `attachments?: () => Promise<number>` to the props and its type; inside the component:

```tsx
  const [images, setImages] = useState(0);
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
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [entry.id]);
```

and change the paragraph to:

```tsx
      <p>
        This is permanent deletion, not macOS Trash.
        {entry.kind === "folder"
          ? " The folder and everything inside it will be deleted."
          : images > 0
            ? ` Also deletes ${images} attached image${images === 1 ? "" : "s"}.`
            : ""}
      </p>
```

(Drop the eslint comment if the project has no eslint config; check `ls desktop/frontend/.eslintrc* desktop/frontend/eslint.config.*`.)

- [ ] **Step 5: Wire App**

`src/App.tsx` `remove()`:

```ts
  async function remove() {
    if (!deleting) return;
    const target = deleting;
    const r = await api.delete(target.input);
    // partial_failure: the note is gone but its images folder is not.
    if (r.problem && r.problem.code !== "partial_failure")
      throw new Error(r.problem.message);
    const doc = workspace.snapshot().document;
    if (
      doc &&
      (doc.id === target.entry.id ||
        (target.entry.kind === "folder" &&
          doc.path.startsWith(target.entry.path + "/")))
    )
      workspace.clearDocument();
    else workspace.clearSelection();
    setDeleting(null);
    await refresh();
    if (r.problem) setError(r.problem.message);
  }
```

DeleteDialog usage:

```tsx
        <DeleteDialog
          entry={deleting.entry}
          attachments={
            deleting.entry.kind === "folder"
              ? undefined
              : () =>
                  api
                    .attachments(workspace.snapshot().session, deleting.entry.id)
                    .then((r) => r.count)
          }
          onDelete={remove}
          onCancel={() => setDeleting(null)}
        />
```

- [ ] **Step 6: Run tests**

Run: `cd desktop/frontend && npm run typecheck && npm test && npx prettier --check src`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add desktop/frontend/src
git commit -m "Count attached images in the delete confirmation

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Show stored images in Preview

**Files:**
- Create: `desktop/frontend/src/document/imageSource.ts`, `src/document/imageSource.test.ts`
- Modify: `desktop/frontend/src/document/MarkdownPreview.tsx`, `src/document/MarkdownPreview.test.tsx`, `src/document/DocumentPane.tsx` (the `<MarkdownPreview body={save.body} />` line), `src/app.css`

**Interfaces:**
- Consumes: Task 6 route `/note-image/<encoded path>`.
- Produces: `export const imageRoute = "/note-image/"`, `export function imageSource(src: string, notePath?: string): string | null`; `MarkdownPreview` prop `notePath?: string`.

- [ ] **Step 1: Write failing tests**

`src/document/imageSource.test.ts`:

```ts
import { expect, it } from "vitest";
import { imageSource } from "./imageSource";
it.each([
  ["plan.assets/shot.png", "Ideas/plan.md", "/note-image/Ideas/plan.assets/shot.png"],
  ["my%20plan.assets/a%20b.jpg", "my plan.md", "/note-image/my%20plan.assets/a%20b.jpg"],
  ["./plan.assets/x.webp", "plan.md", "/note-image/plan.assets/x.webp"],
  ["../Other/n.assets/x.GIF", "Ideas/plan.md", "/note-image/Other/n.assets/x.GIF"],
  ["plan.assets/shot.png?v=1#top", "plan.md", "/note-image/plan.assets/shot.png"],
])("maps %s from %s", (src, note, want) => {
  expect(imageSource(src, note)).toBe(want);
});
it.each([
  ["https://evil.example/plan.assets/x.png", "plan.md"],
  ["file:///etc/plan.assets/x.png", "plan.md"],
  ["data:image/png;base64,AAAA", "plan.md"],
  ["/abs/plan.assets/x.png", "plan.md"],
  ["//host/plan.assets/x.png", "plan.md"],
  ["../plan.assets/x.png", "plan.md"],
  ["secret.png", "plan.md"],
  ["plan.assets/x.svg", "plan.md"],
  ["plan.assets/nested/x.png", "plan.md"],
  ["plan.assets%2F..%2F..%2Fx.png", "plan.md"],
  ["plan.assets/%E0%A4%A.png", "plan.md"],
  ["plan.assets/x.png", undefined],
  ["", "plan.md"],
])("rejects %s from %s", (src, note) => {
  expect(imageSource(src, note)).toBeNull();
});
```

Append to `src/document/MarkdownPreview.test.tsx`:

```tsx
it("loads images stored in the note's .assets folder through the image route", () => {
  render(
    <MarkdownPreview
      notePath="Ideas/plan.md"
      body={
        "![Shot](plan.assets/shot.png)\n\n![remote](https://evil.example/plan.assets/x.png)\n\n![local image](../secret.png)"
      }
    />,
  );
  expect(screen.getByRole("img", { name: "Shot" })).toHaveAttribute(
    "src",
    "/note-image/Ideas/plan.assets/shot.png",
  );
  expect(screen.getAllByRole("img")).toHaveLength(1);
  expect(screen.getByText(/remote.*unavailable/)).toBeInTheDocument();
  expect(screen.getByText(/local image.*unavailable/)).toBeInTheDocument();
});
it("falls back to the placeholder when a stored image fails to load", () => {
  render(
    <MarkdownPreview notePath="plan.md" body="![Shot](plan.assets/shot.png)" />,
  );
  fireEvent.error(screen.getByRole("img", { name: "Shot" }));
  expect(screen.queryByRole("img")).toBeNull();
  expect(screen.getByText(/Shot.*unavailable/)).toBeInTheDocument();
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd desktop/frontend && npm test -- imageSource MarkdownPreview`
Expected: FAIL (`imageSource` module missing; no `img` rendered).

- [ ] **Step 3: Implement `src/document/imageSource.ts`**

```ts
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
```

- [ ] **Step 4: Update `MarkdownPreview.tsx`**

Add `import { useState } from "react";` and `import { imageSource } from "./imageSource";`. Add components above `MarkdownPreview`:

```tsx
function Placeholder({ alt }: { alt: string }) {
  return <span className="image-placeholder">{alt || "Image"} (image unavailable)</span>;
}
function NoteImage({ src, alt }: { src: string; alt: string }) {
  const [failed, setFailed] = useState(false);
  return failed ? (
    <Placeholder alt={alt} />
  ) : (
    <img src={src} alt={alt} loading="lazy" onError={() => setFailed(true)} />
  );
}
```

Change the signature to accept `notePath`:

```tsx
export function MarkdownPreview({
  body,
  notePath,
  openExternal = (url: string) => {
    void hostAPI.openExternal(url);
  },
}: {
  body: string;
  notePath?: string;
  openExternal?: (url: string) => void;
}) {
```

and in `<ReactMarkdown>` replace `urlTransform={safeURL}` and the `img` override:

```tsx
        urlTransform={(url, key, node) =>
          key === "src" && node.tagName === "img"
            ? (imageSource(url, notePath) ?? "")
            : safeURL(url)
        }
        components={{
          img: ({ src, alt }) =>
            typeof src === "string" && src ? (
              <NoteImage key={src} src={src} alt={alt ?? ""} />
            ) : (
              <Placeholder alt={alt ?? ""} />
            ),
```

(keep the existing `a` override unchanged).

- [ ] **Step 5: Pass the note path and style images**

`src/document/DocumentPane.tsx`: `<MarkdownPreview body={save.body} notePath={state.document.path} />`.

`src/app.css`, after the `.image-placeholder` rule:

```css
.markdown img {
  display: block;
  max-width: 100%;
  height: auto;
  margin: 12px 0;
  border-radius: 6px;
}
```

- [ ] **Step 6: Run tests**

Run: `cd desktop/frontend && npm run typecheck && npm test && npx prettier --check src`
Expected: PASS, including the existing "keeps unsafe content inert and does not fetch images" test (no `notePath` → no `img`).

- [ ] **Step 7: Commit**

```bash
git add desktop/frontend/src
git commit -m "Show images stored in a note's attachment folder in Preview

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: Drop and paste images into the editor

**Files:**
- Create: `desktop/frontend/src/document/imageInput.ts`, `src/document/imageInput.test.ts`, `src/document/dropGuard.ts`, `src/document/dropGuard.test.ts`
- Modify: `desktop/frontend/src/document/DocumentPane.tsx`, `src/document/DocumentPane.test.tsx`, `src/App.tsx`, `src/app.css`

**Interfaces:**
- Consumes: Task 7 `SaveImageInput`, `SaveImageReply`, `DesktopAPI.saveImage`.
- Produces:
  - `imageInput(options: ImageInputOptions): Extension` where `interface ImageInputOptions { save(name: string, data: string): Promise<SavedImage>; report(message: string): void }`, `interface SavedImage { name: string; link: string }`
  - `pastedImageName(now?: Date): string` → `pasted-YYYY-MM-DD-HHMMSS`
  - `imageMarkdown(saved: SavedImage): string` → `![<name without extension>](<link>)`
  - `supportedImageTypes`, `maxImageBytes`
  - `guardFileDrops(target?: Window): () => void`
  - `DocumentPane` prop `saveImage?: (input: SaveImageInput) => Promise<SaveImageReply>`

- [ ] **Step 1: Write failing tests for the plugin**

`src/document/imageInput.test.ts`:

```ts
import { afterEach, expect, it, vi } from "vitest";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { imageInput, maxImageBytes, pastedImageName } from "./imageInput";
const views: EditorView[] = [];
afterEach(() => views.splice(0).forEach((v) => v.destroy()));
function file(name: string, type = "image/png") {
  return new File([new Uint8Array([0x89, 0x50])], name, { type });
}
function editor(
  save: (name: string, data: string) => Promise<{ name: string; link: string }>,
  report = vi.fn(),
  doc = "Hello",
  readOnly = false,
) {
  const view = new EditorView({
    parent: document.body,
    state: EditorState.create({
      doc,
      selection: { anchor: doc.length },
      extensions: [
        imageInput({ save, report }),
        EditorState.readOnly.of(readOnly),
      ],
    }),
  });
  views.push(view);
  return { view, report };
}
// A minimal DataTransfer stand-in; jsdom has none. getData feeds CodeMirror's
// own text paste/drop handling when the image plugin declines an event.
function send(
  view: EditorView,
  type: "paste" | "drop",
  files: File[],
  text = "",
) {
  const event = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(event, type === "paste" ? "clipboardData" : "dataTransfer", {
    value: {
      files,
      types: files.length ? ["Files"] : ["text/plain"],
      items: [],
      getData: (format: string) => (format.toLowerCase().startsWith("text") ? text : ""),
    },
  });
  view.contentDOM.dispatchEvent(event);
  return event;
}
const ok = vi.fn(async (name: string) => ({
  name: name || "image.png",
  link: "plan.assets/" + (name || "image.png"),
}));
afterEach(() => ok.mockClear());

it("pastes an image on its own line at the cursor", async () => {
  const { view } = editor(ok);
  const event = send(view, "paste", [file("shot.png")]);
  expect(event.defaultPrevented).toBe(true);
  await vi.waitFor(() =>
    expect(view.state.doc.toString()).toBe(
      "Hello\n![shot](plan.assets/shot.png)\n",
    ),
  );
  expect(ok).toHaveBeenCalledWith("shot.png", "iVA=");
});
it("names pasted images that have no filename", async () => {
  const { view } = editor(ok);
  send(view, "paste", [file("")]);
  await vi.waitFor(() => expect(ok).toHaveBeenCalled());
  expect(ok.mock.calls[0][0]).toMatch(/^pasted-\d{4}-\d{2}-\d{2}-\d{6}$/);
  expect(pastedImageName(new Date(2026, 10, 4, 9, 5, 7))).toBe(
    "pasted-2026-11-04-090507",
  );
});
it("leaves text-only pastes to CodeMirror", () => {
  const { view } = editor(ok);
  send(view, "paste", [], " world");
  expect(view.state.doc.toString()).toBe("Hello world");
  expect(ok).not.toHaveBeenCalled();
});
it("leaves drops without files alone", () => {
  const { view } = editor(ok);
  expect(send(view, "drop", []).defaultPrevented).toBe(false);
  expect(ok).not.toHaveBeenCalled();
});
it("ignores non-image clipboard files so their text pastes normally", () => {
  const { view } = editor(ok);
  send(view, "paste", [file("report.pdf", "application/pdf")], " report.pdf");
  expect(view.state.doc.toString()).toBe("Hello report.pdf");
  expect(ok).not.toHaveBeenCalled();
});
it("keeps order and tracks the insertion point while the user types", async () => {
  let release!: () => void;
  const gate = new Promise<void>((resolve) => (release = resolve));
  const save = vi.fn(async (name: string) => {
    await gate;
    return { name, link: "plan.assets/" + name };
  });
  const { view } = editor(save);
  send(view, "paste", [file("a.png"), file("b.png")]);
  await vi.waitFor(() => expect(save).toHaveBeenCalledTimes(1));
  view.dispatch({ changes: { from: 0, insert: "Typed " } });
  release();
  await vi.waitFor(() =>
    expect(view.state.doc.toString()).toBe(
      "Typed Hello\n![a](plan.assets/a.png)\n![b](plan.assets/b.png)\n",
    ),
  );
});
it("reports unsupported, oversized and failed images and still adds the rest", async () => {
  const save = vi.fn(async (name: string) => {
    if (name === "broken.png") throw new Error("disk full");
    return { name, link: "plan.assets/" + name };
  });
  const big = file("big.png");
  Object.defineProperty(big, "size", { value: maxImageBytes + 1 });
  const { view, report } = editor(save);
  send(view, "drop", [
    file("photo.heic", "image/heic"),
    big,
    file("broken.png"),
    file("good.png"),
    file("notes.txt", "text/plain"),
  ]);
  await vi.waitFor(() =>
    expect(view.state.doc.toString()).toBe(
      "Hello\n![good](plan.assets/good.png)\n",
    ),
  );
  expect(report.mock.calls.map((c) => c[0])).toEqual([
    "Couldn't add photo.heic: only PNG, JPEG, GIF and WebP images are supported",
    "Couldn't add big.png: images must be 20 MB or smaller",
    "Couldn't add broken.png: disk full",
    "Couldn't add notes.txt: only PNG, JPEG, GIF and WebP images are supported",
  ]);
});
it("does nothing but cancel the default while read-only", () => {
  const { view } = editor(ok, vi.fn(), "Hello", true);
  expect(send(view, "paste", [file("shot.png")]).defaultPrevented).toBe(true);
  expect(ok).not.toHaveBeenCalled();
});
```

`src/document/dropGuard.test.ts`:

```ts
import { afterEach, expect, it } from "vitest";
import { guardFileDrops } from "./dropGuard";
let release = () => {};
afterEach(() => {
  release();
  document.body.innerHTML = "";
});
function drag(target: Element, type: "dragover" | "drop", types: string[]) {
  const event = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(event, "dataTransfer", {
    value: { types, dropEffect: "copy" },
  });
  target.dispatchEvent(event);
  return event;
}
it("cancels file drags outside the editor text only", () => {
  release = guardFileDrops(window);
  document.body.innerHTML =
    '<div class="preview"></div><div class="cm-editor"><div class="cm-gutters"></div><div class="cm-content"><p>x</p></div></div>';
  const preview = document.querySelector(".preview")!,
    gutter = document.querySelector(".cm-gutters")!,
    content = document.querySelector(".cm-content p")!;
  expect(drag(preview, "drop", ["Files"]).defaultPrevented).toBe(true);
  expect(drag(preview, "dragover", ["Files"]).defaultPrevented).toBe(true);
  expect(drag(gutter, "drop", ["Files"]).defaultPrevented).toBe(true);
  expect(drag(content, "drop", ["Files"]).defaultPrevented).toBe(false);
  expect(drag(preview, "drop", ["text/plain"]).defaultPrevented).toBe(false);
  release();
  expect(drag(preview, "drop", ["Files"]).defaultPrevented).toBe(false);
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd desktop/frontend && npm test -- imageInput dropGuard`
Expected: FAIL (modules missing).

- [ ] **Step 3: Implement `src/document/imageInput.ts`**

```ts
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
              options.report(`Couldn't add ${label}: images must be 20 MB or smaller`);
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
          if (!view.state.readOnly) void this.insert(files, dropPosition(view, event));
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
```

- [ ] **Step 4: Implement `src/document/dropGuard.ts`**

```ts
// WKWebView opens a file dropped anywhere outside editable content, replacing
// the app with that file. Cancel file drags everywhere except over the Markdown
// editor's text, where the image plugin handles them.
export function guardFileDrops(target: Window = window): () => void {
  const handler = (event: Event) => {
    const transfer = (event as DragEvent).dataTransfer;
    if (!transfer || !Array.from(transfer.types ?? []).includes("Files")) return;
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
```

- [ ] **Step 5: Run the unit tests**

Run: `cd desktop/frontend && npm test -- imageInput dropGuard`
Expected: PASS.

- [ ] **Step 6: Write the failing DocumentPane test**

Append to `src/document/DocumentPane.test.tsx` (uses the file's existing `setup()`, `doc` with `name: "a"`):

```tsx
function pasteImage(container: HTMLElement, name: string) {
  const event = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "clipboardData", {
    value: {
      files: [new File([new Uint8Array([1])], name, { type: "image/png" })],
      types: ["Files"],
      items: [],
    },
  });
  container.querySelector(".cm-content")!.dispatchEvent(event);
}
it("pastes an image into the note and reports failures", async () => {
  const { c, w, api } = await setup();
  const { container, unmount } = render(
    <DocumentPane workspace={w} coordinator={c} saveImage={api.saveImage} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  pasteImage(container, "shot.png");
  // A new editor's cursor starts at 0, so the link goes before the heading.
  await vi.waitFor(() =>
    expect(c.snapshot().body).toBe("![shot](a.assets/shot.png)\n# Original"),
  );
  const failing = vi.fn(async () => ({
    name: "",
    link: "",
    problem: { code: "not_found", message: "Document not found", path: "" },
  }));
  unmount();
  const second = render(
    <DocumentPane workspace={w} coordinator={c} saveImage={failing} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Edit" }));
  pasteImage(second.container, "again.png");
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Couldn't add again.png: Document not found",
  );
  second.unmount();
  c.dispose();
});
```

- [ ] **Step 7: Wire DocumentPane**

In `src/document/DocumentPane.tsx`:
- import `type { OperationReply, SaveImageInput, SaveImageReply } from "../api/types";` and `import { imageInput } from "./imageInput";`
- add prop `saveImage?: (input: SaveImageInput) => Promise<SaveImageReply>;` (destructure it with the others)
- add state `const [imageProblem, setImageProblem] = useState("");`
- in the editor-creating `useEffect`, before `new EditorView`, capture the target and build the extension:

```tsx
    const target = { session: state.session, id: state.document.id };
    const images = saveImage
      ? [
          imageInput({
            save: async (name, data) => {
              const r = await saveImage({ ...target, name, data });
              if (r.problem) throw new Error(r.problem.message);
              return { name: r.name, link: r.link };
            },
            report: setImageProblem,
          }),
        ]
      : [];
```

  then add `...images,` at the end of the `extensions` array and add `saveImage` to the effect's dependency array.
- render the banner just before the `{save.indexingWarning && ...}` block:

```tsx
      {imageProblem && (
        <div className="image-error" role="alert">
          {imageProblem}
          <button
            aria-label="Dismiss image error"
            onClick={() => setImageProblem("")}
          >
            ×
          </button>
        </div>
      )}
```

`src/app.css`: extend the `.save-error, .index-warning` selector to `.save-error, .index-warning, .image-error` and add:

```css
.image-error button {
  float: right;
  border: 0;
  background: none;
}
```

- [ ] **Step 8: Wire App**

In `src/App.tsx`: `import { guardFileDrops } from "./document/dropGuard";`, add `useEffect(() => guardFileDrops(), []);` near the other top-level effects, and pass `saveImage={api.saveImage}` to `<DocumentPane ... />`.

- [ ] **Step 9: Run all frontend tests**

Run: `cd desktop/frontend && npm run typecheck && npm test && npx prettier --check src`
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add desktop/frontend/src
git commit -m "Drop and paste images into the Markdown editor

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: End-to-end test and documentation

**Files:**
- Create: `desktop/frontend/tests/images.spec.ts`
- Modify: `docs/desktop.md` (lines ~105-106 and ~151), `docs/cli.md` (around line 56), `docs/implementation-decisions.md` (append ruling 14)

**Interfaces:**
- Consumes: everything above; the Playwright fixture at `/test-app.html` (fake bridge with note `Work/Reading.md`, name `Reading`).

- [ ] **Step 1: Write the end-to-end test**

```ts
import { test, expect } from "@playwright/test";
// A valid 1×1 PNG.
const png =
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==";
test("pasting an image links it in the note and Preview shows it", async ({
  page,
}) => {
  // The fixture has no Go backend; stand in for the confined image route.
  await page.route("**/note-image/**", (route) =>
    route.fulfill({
      status: 200,
      contentType: "image/png",
      body: Buffer.from(png, "base64"),
    }),
  );
  await page.goto("/test-app.html");
  await page.getByRole("treeitem", { name: "Work", exact: true }).click();
  await page.getByRole("treeitem", { name: "Reading", exact: true }).click();
  await page.getByRole("button", { name: "Edit", exact: true }).click();
  const editor = page.locator(".cm-content");
  await editor.click();
  await page.keyboard.press("Meta+ArrowDown");
  await editor.evaluate((el, data) => {
    const bytes = Uint8Array.from(atob(data), (c) => c.charCodeAt(0));
    const transfer = new DataTransfer();
    transfer.items.add(new File([bytes], "", { type: "image/png" }));
    el.dispatchEvent(
      new ClipboardEvent("paste", {
        clipboardData: transfer,
        bubbles: true,
        cancelable: true,
      }),
    );
  }, png);
  await expect(editor).toContainText(
    /!\[pasted-\d{4}-\d{2}-\d{2}-\d{6}\]\(Reading\.assets\/pasted-\d{4}-\d{2}-\d{2}-\d{6}\.png\)/,
  );
  await page.getByRole("button", { name: "Preview", exact: true }).click();
  const image = page.locator(".markdown img");
  await expect(image).toHaveAttribute(
    "src",
    /^\/note-image\/Work\/Reading\.assets\/pasted-\d{4}-\d{2}-\d{2}-\d{6}\.png$/,
  );
  await expect
    .poll(() => image.evaluate((img: HTMLImageElement) => img.naturalWidth))
    .toBe(1);
});
```

- [ ] **Step 2: Run end-to-end tests**

Run: `cd desktop/frontend && npm run test:e2e`
Expected: all specs PASS, including the existing `security.spec.ts` (no non-local requests, no `img` for remote sources).

- [ ] **Step 3: Update `docs/desktop.md`**

Replace the two bullets about Preview images/attachments (currently "Preview renders Markdown/GFM without running raw HTML or fetching images. External http/https links open only on explicit click. Attachments, relative link navigation, and executable embeds are not supported.") with:

```markdown
- Preview renders Markdown/GFM without running raw HTML. It shows images stored
  in a note's attachment folder (below) and never fetches remote images, which
  appear as placeholders. External http/https links open only on explicit click.
  Relative link navigation and executable embeds are not supported.
- In Edit, drop image files onto the text or paste an image with ⌘V. Recall
  copies each into the note's attachment folder, `<note name>.assets/` beside
  the note (for `Ideas/plan.md`, `Ideas/plan.assets/`), and inserts
  `![name](plan.assets/name.png)` where you dropped or at the cursor. PNG, JPEG,
  GIF and WebP up to 20 MB are accepted, identified by content; other files are
  refused with a message. Dropped files keep a cleaned-up name; pasted images
  are named `pasted-YYYY-MM-DD-HHMMSS`. Existing images are never overwritten.
  Removing a link does not delete its image.
- Attachment folders (any folder whose name ends in `.assets`) are hidden from
  the tree, search and every client; a Markdown file placed inside one is not a
  note.
```

In the deletion bullet, add after "after confirmation.": "Deleting a note also deletes its attachment folder; the confirmation says how many images it holds." On the "V1 has no tabs, split pane, drag-and-drop moves, or rename UI." line, change "drag-and-drop moves" to "drag-and-drop moves of tree items".

- [ ] **Step 4: Update `docs/cli.md`**

After the paragraph starting "Move and delete accept optional `--if-revision`" add:

```markdown
`document delete` also removes the note's `<name>.assets/` image folder. If the
note is deleted but the folder cannot be (for example, it is a symlink, which is
never followed), the command fails with `partial_failure` naming the folder.
`document move` carries the folder along; when the filename changes, the folder
is renamed to match and the note's Markdown links that start with the old folder
name are repointed. A move whose destination already has an attachment folder
fails with `conflict` before anything changes. Folders whose names end in
`.assets` are attachment folders: they are not listed, indexed or diagnosed.
```

- [ ] **Step 5: Add the decision record**

Append to the "Coordinator rulings, in order" list in `docs/implementation-decisions.md`:

```markdown
14. **Serve note images through a confined asset-server fallback.** The desktop
    registers a Wails `assetserver.Options.Handler` that answers only
    `GET /note-image/<path>` for files directly inside `*.assets` folders, read
    with the no-follow store and accepted only when their bytes match PNG, JPEG,
    GIF or WebP and their extension; everything else is a plain 404. Preview
    maps only relative sources that resolve into an attachment folder.
    **Cost if wrong:** a new webview-reachable file read path; a confinement bug
    there would expose collection images (never other files, by the format and
    folder checks) to authored Markdown in the same collection.
```

- [ ] **Step 6: Full verification**

Run:

```bash
gofmt -l . && go vet ./... && go test ./...
(cd desktop/frontend && npm run typecheck && npm test && npm run test:e2e && npx prettier --check src tests && npm run build)
(cd desktop && go vet ./... && go test ./...)
```

Expected: no gofmt output; every command passes.

- [ ] **Step 7: Commit**

```bash
git add desktop/frontend/tests/images.spec.ts docs
git commit -m "Document image attachments and test paste-to-Preview end to end

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
