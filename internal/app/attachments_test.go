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
