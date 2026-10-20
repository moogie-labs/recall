package app

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"recall/internal/model"
)

// Recovery must not require a valid projection, but must still require a typed,
// regular, confined file. ID operations must never pick a duplicate arbitrarily.
func TestEditorPathAndDeleteMalformedExplicitSources(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a, cfg := newTestApp(t)
	defer a.Close()
	for _, raw := range []string{"---\nid: [broken\n---\nbody", "---\nid: conflict\ntype: task\n---\nbody"} {
		path := filepath.Join(cfg.Content, "repair.note.md")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := a.EditorPath(t.Context(), model.Ref{Path: "repair.note.md"})
		if err != nil || got != path {
			t.Fatalf("actual recovery path: %q %v", got, err)
		}
		wantCode(t, a.Delete(t.Context(), model.Ref{Path: "repair.note.md"}, "stale"), "conflict")
		rev := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
		if err := a.Delete(t.Context(), model.Ref{Path: "repair.note.md"}, rev); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("not deleted: %v", err)
		}
	}
	d := mustCreate(t, a, CreateInput{Kind: model.Note, Name: "one"})
	if err := os.WriteFile(filepath.Join(cfg.Content, "two.note.md"), []byte(d.Raw), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := a.EditorPath(t.Context(), model.Ref{ID: d.ID})
	wantCode(t, err, "duplicate_id")
	wantCode(t, a.Delete(t.Context(), model.Ref{ID: d.ID}, ""), "duplicate_id")
	if _, err := a.EditorPath(t.Context(), model.Ref{Path: "two.note.md"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(t.Context(), model.Ref{Path: "two.note.md"}, d.Revision); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Content, "repair.note.md"), []byte("fixed body"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if repaired, err := a.Read(t.Context(), model.Ref{Path: "repair.note.md"}); err != nil || repaired.Body != "fixed body" {
		t.Fatalf("repair: %+v %v", repaired, err)
	}
}

func TestEditorRecoveryRejectsUnmanagedSymlinkAndRootReplacement(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a, cfg := newTestApp(t)
	defer a.Close()
	d := mustCreate(t, a, CreateInput{Kind: model.Note, Name: "safe"})
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "out.note.md"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cfg.Content, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "out.note.md"), filepath.Join(cfg.Content, "link.note.md")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../out.note.md", ".", cfg.Content, "link/out.note.md", "link.note.md"} {
		_, err := a.EditorPath(t.Context(), model.Ref{Path: path})
		wantCode(t, err, "outside_root")
		wantCode(t, a.Delete(t.Context(), model.Ref{Path: path}, ""), "outside_root")
	}
	if err := os.WriteFile(filepath.Join(cfg.Content, "plain.md"), []byte("unmanaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.EditorPath(t.Context(), model.Ref{Path: "plain.md"}); err == nil {
		t.Fatal("untyped editor path accepted")
	}
	if err := a.Delete(t.Context(), model.Ref{Path: "plain.md"}, ""); err == nil {
		t.Fatal("untyped deletion accepted")
	}
	if err := os.Rename(cfg.Content, cfg.Content+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cfg.Content+"-old", cfg.Content); err != nil {
		t.Fatal(err)
	}
	_, err := a.EditorPath(t.Context(), model.Ref{Path: d.Path})
	wantCode(t, err, "outside_root")
}

func TestEditorRejectsReplacedActualDirectoryRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a, cfg := newTestApp(t)
	defer a.Close()
	d := mustCreate(t, a, CreateInput{Kind: model.Note, Name: "safe"})
	if err := os.Rename(cfg.Content, cfg.Content+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cfg.Content, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Content, d.Path), []byte(d.Raw), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := a.EditorPath(t.Context(), model.Ref{Path: d.Path})
	wantCode(t, err, "outside_root")
}
