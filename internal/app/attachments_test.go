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
