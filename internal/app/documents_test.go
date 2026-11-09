package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"recall/internal/config"
	"recall/internal/model"
)

func newTestApp(t testing.TB) (*App, config.Config) {
	t.Helper()
	cfg, err := config.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	a, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a, cfg
}

func mustCreate(t testing.TB, a *App, input CreateInput) model.Document {
	t.Helper()
	d, err := a.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func wantCode(t testing.TB, err error, code string) {
	t.Helper()
	if got := model.ErrorCode(err); got != code {
		t.Fatalf("error code = %q, want %q: %v", got, code, err)
	}
}

// Catches dropped bodies, stale reads and unsafe moves.
func TestDocumentCRUDAndDirectRefresh(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	ctx := t.Context()
	if err := a.CreateFolder(ctx, "shared space"); err != nil {
		t.Fatal(err)
	}
	n := mustCreate(t, a, CreateInput{Folder: "shared space", Name: "same", Body: "自由\r\n---\r\n"})
	d := mustCreate(t, a, CreateInput{Folder: "shared space", Name: "other", Body: "details"})
	root := mustCreate(t, a, CreateInput{Name: "root"})
	if want := "---\nid: " + root.ID + "\ntitle: root\n---\n"; root.Raw != want {
		t.Fatalf("new note metadata = %q, want %q", root.Raw, want)
	}
	if root.Path != "root.md" || n.Body != "自由\r\n---\r\n" {
		t.Fatalf("unexpected documents: %+v %+v %+v", n, d, root)
	}
	s, err := a.List(ctx, "shared space", false)
	if err != nil || len(s.Documents) != 2 {
		t.Fatalf("list: %+v %v", s, err)
	}
	_, err = a.Create(ctx, CreateInput{Folder: "shared space", Name: "same"})
	wantCode(t, err, "already_exists")
	_, err = a.Move(ctx, model.Ref{ID: d.ID}, root.Path, d.Revision)
	wantCode(t, err, "already_exists")
	moved, err := a.Move(ctx, model.Ref{ID: d.ID}, "moved.md", d.Revision)
	if err != nil || moved.ID != d.ID || moved.Path != "moved.md" || moved.Raw != d.Raw {
		t.Fatalf("move: %+v %v", moved, err)
	}
	_, err = a.Move(ctx, model.Ref{ID: d.ID}, "wrong.txt", "")
	wantCode(t, err, "invalid_document")
	updated, err := a.Update(ctx, model.Ref{Path: moved.Path}, moved.Revision, strings.Replace(moved.Raw, "details", "changed", 1))
	if err != nil || updated.Body != "changed" {
		t.Fatalf("update: %+v %v", updated, err)
	}
	_, err = a.Update(ctx, model.Ref{ID: d.ID}, moved.Revision, moved.Raw)
	wantCode(t, err, "conflict")
	_, err = a.Update(ctx, model.Ref{ID: d.ID}, "", updated.Raw)
	wantCode(t, err, "invalid_argument")
	_, err = a.Update(ctx, model.Ref{ID: d.ID}, updated.Revision, strings.Replace(updated.Raw, d.ID, "changed-id", 1))
	wantCode(t, err, "invalid_argument")
	external := strings.Replace(updated.Raw, "changed", "external", 1)
	if err := os.WriteFile(filepath.Join(cfg.Content, updated.Path), []byte(external), 0600); err != nil {
		t.Fatal(err)
	}
	read, err := a.Read(ctx, model.Ref{ID: d.ID})
	if err != nil || read.Body != "external" || read.Revision == updated.Revision {
		t.Fatalf("refresh: %+v %v", read, err)
	}
	if err := a.Delete(ctx, model.Ref{ID: d.ID}, updated.Revision); model.ErrorCode(err) != "conflict" {
		t.Fatalf("stale delete: %v", err)
	}
	if err := a.Delete(ctx, model.Ref{ID: n.ID}, n.Revision); err != nil {
		t.Fatal(err)
	}
	_, err = a.Read(ctx, model.Ref{ID: n.ID})
	wantCode(t, err, "not_found")
}

func TestNameCollisionsAndSuffixHandling(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	ctx := t.Context()
	first := mustCreate(t, a, CreateInput{Name: "foo"})
	if first.Path != "foo.md" {
		t.Fatalf("path = %q", first.Path)
	}
	_, err := a.Create(ctx, CreateInput{Name: "foo"})
	wantCode(t, err, "already_exists")
	_, err = a.Create(ctx, CreateInput{Name: "foo.md"})
	wantCode(t, err, "invalid_argument")
	dotted := mustCreate(t, a, CreateInput{Name: "foo.note"})
	if dotted.Path != "foo.note.md" || dotted.Title != "foo.note" {
		t.Fatalf("dotted name: %+v", dotted)
	}
	_, err = a.Move(ctx, model.Ref{ID: dotted.ID}, "foo.md", dotted.Revision)
	wantCode(t, err, "already_exists")
	if err := a.CreateFolder(ctx, "dir.md"); err != nil {
		t.Fatal(err)
	}
	_, err = a.Create(ctx, CreateInput{Name: "dir"})
	wantCode(t, err, "already_exists")
	_, err = a.Move(ctx, model.Ref{ID: dotted.ID}, "dir.md", dotted.Revision)
	wantCode(t, err, "already_exists")
	s, err := a.List(ctx, "", true)
	if err != nil || len(s.Documents) != 2 || len(s.Diagnostics) != 0 {
		t.Fatalf("folder named like a note must stay a folder: %+v %v", s, err)
	}
	_, err = a.Create(ctx, CreateInput{Name: ".secret"})
	wantCode(t, err, "invalid_document")
	if err := os.Mkdir(filepath.Join(cfg.Content, ".hidden"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = a.Create(ctx, CreateInput{Folder: ".hidden", Name: "inside"})
	wantCode(t, err, "invalid_document")
	_, err = a.Move(ctx, model.Ref{ID: first.ID}, ".hidden/moved.md", first.Revision)
	wantCode(t, err, "invalid_document")
}

// Runtime archives and third-party notices can contain .md files. Only content/
// is scanned, so nothing Recall owns beside it can become a note or be rewritten.
func TestRecallOwnedPathsAreNeverScannedAsNotes(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	owned := map[string]string{
		"README.md":                    "beside the layout\n",
		"runtime/b11669/LICENSE.md":    "runtime notice\n",
		"runtime/b11669/docs/about.md": "---\nid: [broken\n---\n",
		"models/NOTICE.md":             "model notice\n",
		"run/stale.md":                 "run file\n",
	}
	for path, raw := range owned {
		full := filepath.Join(cfg.Root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	mustCreate(t, a, CreateInput{Name: "real"})
	if _, err := a.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err := a.List(t.Context(), "", true)
	if err != nil || len(s.Documents) != 1 || s.Documents[0].Path != "real.md" || len(s.Diagnostics) != 0 {
		t.Fatalf("owned paths scanned: %+v %v", s, err)
	}
	if !slices.Equal(s.Folders, []string{"."}) {
		t.Fatalf("owned folders exposed: %v", s.Folders)
	}
	for path, raw := range owned {
		if got, err := os.ReadFile(filepath.Join(cfg.Root, path)); err != nil || string(got) != raw {
			t.Fatalf("%s rewritten: %q %v", path, got, err)
		}
		_, err := a.Read(t.Context(), model.Ref{Path: path})
		if model.ErrorCode(err) != "not_found" {
			t.Fatalf("%s reachable as a document: %v", path, err)
		}
	}
}

func TestReferencesDiagnosticsAndConfinement(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	ctx := t.Context()
	d := mustCreate(t, a, CreateInput{Name: "original"})
	for _, ref := range []model.Ref{{}, {ID: d.ID, Path: d.Path}} {
		_, err := a.Read(ctx, ref)
		wantCode(t, err, "invalid_argument")
	}
	for _, path := range []string{".", "../escape.md", "/absolute.md", "x/../original.md"} {
		_, err := a.Read(ctx, model.Ref{Path: path})
		wantCode(t, err, "outside_root")
		_, err = a.Move(ctx, model.Ref{ID: d.ID}, path, "")
		wantCode(t, err, "outside_root")
	}
	for _, input := range []CreateInput{{Name: "../escape"}, {Name: "ok", Folder: ".."}, {}} {
		if _, err := a.Create(ctx, input); err == nil {
			t.Fatalf("accepted %+v", input)
		}
	}
	if err := os.WriteFile(filepath.Join(cfg.Content, "duplicate.md"), []byte(d.Raw), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := a.Read(ctx, model.Ref{ID: d.ID})
	wantCode(t, err, "duplicate_id")
	_, err = a.Read(ctx, model.Ref{Path: d.Path})
	wantCode(t, err, "duplicate_id")
	if err := os.WriteFile(filepath.Join(cfg.Content, "broken.md"), []byte("---\nid: [broken\n---\nsecret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(cfg.Content, "link")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"link", "link/escape.md"} {
		_, err = a.Read(ctx, model.Ref{Path: path})
		wantCode(t, err, "outside_root")
	}
	_, err = a.Create(ctx, CreateInput{Folder: "link", Name: "escape"})
	wantCode(t, err, "outside_root")
	_, err = a.Read(ctx, model.Ref{Path: "broken.md"})
	wantCode(t, err, "invalid_document")
	s, err := a.List(ctx, "", false)
	if err != nil || len(s.Documents) != 0 || len(s.Diagnostics) != 4 {
		t.Fatalf("diagnostics: %+v %v", s, err)
	}
	// Explicit-path deletion is an intentional recovery operation (ruling 11).
	if err := a.Delete(ctx, model.Ref{Path: "broken.md"}, ""); err != nil {
		t.Fatalf("cannot delete invalid source: %v", err)
	}
}

func TestOpenRequiresInitializationAndHonorsCancellation(t *testing.T) {
	cfg, err := config.Resolve(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Open(t.Context(), cfg)
	wantCode(t, err, "not_initialized")
	if _, err := os.Stat(cfg.Root); !os.IsNotExist(err) {
		t.Fatalf("implicitly initialized: %v", err)
	}
	a, _ := newTestApp(t)
	defer a.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := a.Sync(ctx); err != context.Canceled {
		t.Fatalf("cancel: %v", err)
	}
}

func TestCreateUsesLiteralBodyAndInjectedIdentity(t *testing.T) {
	a, _ := newTestApp(t)
	defer a.Close()
	a.newID = func() string { return "0123456789abcdef0123456789abcdef" }
	body := "---\nid: not-metadata\n---\n自由\r\n"
	d := mustCreate(t, a, CreateInput{Name: "literal", Body: body})
	if d.ID != "0123456789abcdef0123456789abcdef" || d.Body != body || d.Title != "literal" {
		t.Fatalf("literal body/identity: %+v", d)
	}
	_, err := a.Create(t.Context(), CreateInput{Name: "another"})
	wantCode(t, err, "duplicate_id")
	_, err = a.Read(t.Context(), model.Ref{Path: "another.md"})
	wantCode(t, err, "not_found")
}

func TestOpenRejectsSymlinkedIndex(t *testing.T) {
	a, cfg := newTestApp(t)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "untouched")
	if err := os.WriteFile(outside, []byte("not an index"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cfg.Database); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, cfg.Database); err != nil {
		t.Fatal(err)
	}
	_, err := Open(t.Context(), cfg)
	wantCode(t, err, "outside_root")
	got, err := os.ReadFile(outside)
	if err != nil || string(got) != "not an index" {
		t.Fatalf("followed index symlink: %q %v", got, err)
	}
}
