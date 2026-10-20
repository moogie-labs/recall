package app

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"recall/internal/model"
)

func TestFolderOperationsAndScopedLists(t *testing.T) {
	a, _ := newTestApp(t)
	defer a.Close()
	ctx := t.Context()
	for _, path := range []string{"shared", "shared/sub", "shared2", "empty"} {
		if err := a.CreateFolder(ctx, path); err != nil {
			t.Fatal(err)
		}
	}
	root := mustCreate(t, a, CreateInput{Kind: model.Task, Name: "root"})
	n := mustCreate(t, a, CreateInput{Kind: model.Note, Folder: "shared", Name: "note"})
	d := mustCreate(t, a, CreateInput{Kind: model.Task, Folder: "shared/sub", Name: "task"})
	mustCreate(t, a, CreateInput{Kind: model.Task, Folder: "shared2", Name: "other"})
	for _, tc := range []struct {
		folder    string
		recursive bool
		count     int
	}{{"", false, 4}, {".", false, 1}, {".", true, 4}, {"shared", false, 1}, {"shared", true, 2}} {
		s, err := a.List(ctx, tc.folder, tc.recursive)
		if err != nil || len(s.Documents) != tc.count {
			t.Fatalf("list %+v: %+v %v", tc, s, err)
		}
		if tc.folder == "." && !tc.recursive && s.Documents[0].ID != root.ID {
			t.Fatal("root task omitted")
		}
	}
	wantCode(t, a.DeleteFolder(ctx, "shared", false), "folder_not_empty")
	wantCode(t, a.MoveFolder(ctx, "shared", "shared2"), "already_exists")
	wantCode(t, a.DeleteFolder(ctx, n.Path, true), "invalid_argument")
	wantCode(t, a.MoveFolder(ctx, n.Path, "renamed"), "invalid_argument")
	if err := a.MoveFolder(ctx, "shared", "renamed"); err != nil {
		t.Fatal(err)
	}
	for _, doc := range []model.Document{n, d} {
		got, err := a.Read(ctx, model.Ref{ID: doc.ID})
		if err != nil || got.Raw != doc.Raw || got.Path == doc.Path {
			t.Fatalf("folder move: %+v %v", got, err)
		}
	}
	if err := a.DeleteFolder(ctx, "empty", false); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteFolder(ctx, "renamed", true); err != nil {
		t.Fatal(err)
	}
	folders, err := a.Folders(ctx)
	if err != nil || !slices.Equal(folders, []string{".", "shared2"}) {
		t.Fatalf("folders: %v %v", folders, err)
	}
	_, err = a.Read(ctx, model.Ref{ID: d.ID})
	wantCode(t, err, "not_found")
	for _, path := range []string{".", "", "../escape", "/absolute"} {
		wantCode(t, a.DeleteFolder(ctx, path, true), "outside_root")
		wantCode(t, a.CreateFolder(ctx, path), "outside_root")
		wantCode(t, a.MoveFolder(ctx, "shared2", path), "outside_root")
	}
}

func TestFolderSymlinkDeletionPreflight(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	ctx := t.Context()
	if err := a.CreateFolder(ctx, "unsafe"); err != nil {
		t.Fatal(err)
	}
	d := mustCreate(t, a, CreateInput{Kind: model.Note, Folder: "unsafe", Name: "keep"})
	if err := os.Symlink(t.TempDir(), filepath.Join(cfg.Content, "unsafe/link")); err != nil {
		t.Fatal(err)
	}
	wantCode(t, a.DeleteFolder(ctx, "unsafe", true), "outside_root")
	wantCode(t, a.DeleteFolder(ctx, "unsafe/link", true), "outside_root")
	wantCode(t, a.MoveFolder(ctx, "unsafe/link", "elsewhere"), "outside_root")
	got, err := os.ReadFile(filepath.Join(cfg.Content, d.Path))
	if err != nil || string(got) != d.Raw {
		t.Fatalf("preflight removed source: %v", err)
	}
}
