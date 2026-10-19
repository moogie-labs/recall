package fsstore

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"recall/internal/model"
)

func TestStaleRevisionDoesNotWrite(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	locked(t, s, func() error { return s.Write(ctx, "a.note.md", "", []byte("original")) })
	locked(t, s, func() error {
		if err := s.Write(ctx, "a.note.md", "wrong-revision", []byte("changed")); model.ErrorCode(err) != "conflict" {
			t.Fatalf("stale write: %v", err)
		}
		return nil
	})
	got, err := s.Read(ctx, "a.note.md")
	if err != nil || string(got) != "original" {
		t.Fatalf("stale write modified source: %q %v", got, err)
	}
}

func TestWriteCreateOnlyAndRevisionUpdate(t *testing.T) {
	s, cfg := openStore(t)
	ctx := context.Background()
	locked(t, s, func() error {
		if err := s.Write(ctx, "a.note.md", "", []byte("original")); err != nil {
			return err
		}
		if err := s.Write(ctx, "a.note.md", "", []byte("overwrite")); model.ErrorCode(err) != "already_exists" {
			t.Fatalf("create overwrite: %v", err)
		}
		if err := s.Write(ctx, "missing.note.md", "nonempty", nil); model.ErrorCode(err) != "conflict" {
			t.Fatalf("deleted source: %v", err)
		}
		return s.Write(ctx, "a.note.md", fmt.Sprintf("%x", sha256.Sum256([]byte("original"))), []byte("replacement"))
	})
	got, err := s.Read(ctx, "a.note.md")
	if err != nil || string(got) != "replacement" {
		t.Fatalf("replacement: %q %v", got, err)
	}
	entries, err := os.ReadDir(cfg.Content)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files leaked: %+v %v", entries, err)
	}
}

func TestWritePreservesExistingMode(t *testing.T) {
	s, cfg := openStore(t)
	path := filepath.Join(cfg.Content, "a.note.md")
	if err := os.WriteFile(path, []byte("original"), 0640); err != nil {
		t.Fatal(err)
	}
	locked(t, s, func() error {
		return s.Write(context.Background(), "a.note.md", fmt.Sprintf("%x", sha256.Sum256([]byte("original"))), []byte("replacement"))
	})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0640 {
		t.Fatalf("mode=%o want=640", info.Mode().Perm())
	}
}

func TestCompetingRevisionWritesHaveOneWinner(t *testing.T) {
	s, cfg := openStore(t)
	other, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx := context.Background()
	locked(t, s, func() error { return s.Write(ctx, "a.note.md", "", []byte("initial")) })
	revision := fmt.Sprintf("%x", sha256.Sum256([]byte("initial")))
	start, results := make(chan struct{}), make(chan error, 2)
	for i, store := range []*Store{s, other} {
		go func() {
			<-start
			results <- store.WithLock(ctx, func() error { return store.Write(ctx, "a.note.md", revision, []byte(fmt.Sprintf("writer-%d", i))) })
		}()
	}
	close(start)
	wins, conflicts := 0, 0
	for range 2 {
		err := <-results
		switch model.ErrorCode(err) {
		case "":
			wins++
		case "conflict":
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
	got, err := s.Read(ctx, "a.note.md")
	if err != nil || (string(got) != "writer-0" && string(got) != "writer-1") {
		t.Fatalf("winner bytes: %q %v", got, err)
	}
}

func TestCreateOnlyRejectsExistingDirectory(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	locked(t, s, func() error {
		if err := s.Mkdir(ctx, "existing"); err != nil {
			return err
		}
		if err := s.Write(ctx, "existing", "", []byte("body")); model.ErrorCode(err) != "already_exists" {
			t.Fatalf("create-only directory: %v", err)
		}
		return nil
	})
}

func TestWriteKeepsValidatedParentAcrossAncestorSwap(t *testing.T) {
	s, cfg := openStore(t)
	swap := seedAncestorSwap(t, cfg)
	// Read's post-read context check occurs after revision validation and
	// before staging. Replacing the ancestor here must not redirect staging,
	// the revision reread, publication, cleanup, or directory fsync.
	ctx := &checkpointContext{Context: context.Background(), at: 3, action: swap}
	revision := fmt.Sprintf("%x", sha256.Sum256([]byte("original")))
	locked(t, s, func() error { return s.Write(ctx, "active/a.note.md", revision, []byte("replacement")) })
	if ctx.checks < ctx.at {
		t.Fatal("swap checkpoint was not reached")
	}
	for path, want := range map[string]string{"parked/a.note.md": "replacement", "victim/a.note.md": "victim"} {
		raw, err := os.ReadFile(filepath.Join(cfg.Content, path))
		if err != nil || string(raw) != want {
			t.Fatalf("%s: %q %v", path, raw, err)
		}
	}
	for _, dir := range []string{"parked", "victim"} {
		entries, err := os.ReadDir(filepath.Join(cfg.Content, dir))
		if err != nil || len(entries) != 1 {
			t.Fatalf("temporary staging leaked into %s: %+v %v", dir, entries, err)
		}
	}
}

func TestWriteDeletedParentIsConflict(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	locked(t, s, func() error {
		if err := s.Mkdir(ctx, "folder"); err != nil {
			return err
		}
		if err := s.Write(ctx, "folder/a.note.md", "", []byte("original")); err != nil {
			return err
		}
		if err := s.Remove(ctx, "folder", true); err != nil {
			return err
		}
		revision := fmt.Sprintf("%x", sha256.Sum256([]byte("original")))
		if err := s.Write(ctx, "folder/a.note.md", revision, []byte("replacement")); model.ErrorCode(err) != "conflict" {
			t.Fatalf("deleted parent: %v", err)
		}
		return nil
	})
}

func TestCreateKeepsPinnedParentAcrossAncestorSwap(t *testing.T) {
	s, cfg := openStore(t)
	swap := seedAncestorSwap(t, cfg)
	ctx := &checkpointContext{Context: context.Background(), at: 2, action: swap}
	locked(t, s, func() error { return s.Write(ctx, "active/new.note.md", "", []byte("created")) })
	if ctx.checks < ctx.at {
		t.Fatal("swap checkpoint was not reached")
	}
	raw, err := os.ReadFile(filepath.Join(cfg.Content, "parked", "new.note.md"))
	if err != nil || string(raw) != "created" {
		t.Fatalf("original directory create: %q %v", raw, err)
	}
	entries, err := os.ReadDir(filepath.Join(cfg.Content, "victim"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("created or leaked staging in victim: %+v %v", entries, err)
	}
}
