package fsstore

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"recall/internal/config"
	"recall/internal/model"
)

func openStore(t *testing.T) (*Store, config.Config) {
	t.Helper()
	cfg, err := config.Resolve(filepath.Join(t.TempDir(), "Recall collection with spaces"))
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	s, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s, cfg
}

func locked(t *testing.T, s *Store, fn func() error) {
	t.Helper()
	if err := s.WithLock(context.Background(), fn); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRequiresInitialization(t *testing.T) {
	cfg, err := config.Resolve(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(cfg); model.ErrorCode(err) != "not_initialized" {
		t.Fatalf("open: %v", err)
	}
	if _, err := os.Stat(cfg.Root); !os.IsNotExist(err) {
		t.Fatalf("Open created root: %v", err)
	}
	if err := os.MkdirAll(cfg.Content, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(cfg); model.ErrorCode(err) != "not_initialized" {
		t.Fatalf("partial initialization: %v", err)
	}
}

func TestRejectTraversalAndSymlinks(t *testing.T) {
	s, cfg := openStore(t)
	ctx := context.Background()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.note.md")
	if err := os.WriteFile(secret, []byte("external bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cfg.Content, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(cfg.Content, "link.note.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cfg.Content, "real"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(cfg.Content, "internal-link")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../secret.note.md", "real/../../secret.note.md", "/etc/passwd", "escape/secret.note.md", "link.note.md", "internal-link/new.note.md"} {
		t.Run(path, func(t *testing.T) {
			locked(t, s, func() error {
				checks := []error{s.Write(ctx, path, "", []byte("changed")), s.Mkdir(ctx, path), s.Move(ctx, path, "target"), s.Move(ctx, "real", path), s.Remove(ctx, path, true)}
				_, err := s.Read(ctx, path)
				checks = append(checks, err)
				for i, err := range checks {
					if model.ErrorCode(err) != "outside_root" {
						t.Errorf("operation %d: %v", i, err)
					}
				}
				return nil
			})
		})
	}
	for _, path := range []string{"", ".", "real/.."} {
		locked(t, s, func() error {
			for _, err := range []error{s.Mkdir(ctx, path), s.Remove(ctx, path, true), s.Move(ctx, path, "other"), s.Write(ctx, path, "", nil)} {
				if model.ErrorCode(err) != "outside_root" {
					t.Errorf("root mutation %q: %v", path, err)
				}
			}
			return nil
		})
	}
	got, err := os.ReadFile(secret)
	if err != nil || string(got) != "external bytes" {
		t.Fatalf("external target changed: %q %v", got, err)
	}
}

func TestWalkAndFolderOperations(t *testing.T) {
	s, cfg := openStore(t)
	ctx := context.Background()
	locked(t, s, func() error {
		if err := s.Mkdir(ctx, "folder with spaces"); err != nil {
			return err
		}
		if err := s.Mkdir(ctx, "folder with spaces/sub"); err != nil {
			return err
		}
		return s.Write(ctx, "folder with spaces/sub/a.note.md", "", []byte("body"))
	})
	if err := os.Symlink(t.TempDir(), filepath.Join(cfg.Content, "unmanaged")); err != nil {
		t.Fatal(err)
	}
	entries, err := s.Walk(ctx)
	want := []Entry{{Path: "folder with spaces", IsDir: true}, {Path: "folder with spaces/sub", IsDir: true}, {Path: "folder with spaces/sub/a.note.md"}, {Path: "unmanaged", IsSymlink: true}}
	if err != nil || !reflect.DeepEqual(entries, want) {
		t.Fatalf("walk=%+v err=%v", entries, err)
	}
	locked(t, s, func() error {
		if err := s.Remove(ctx, "folder with spaces", false); model.ErrorCode(err) != "folder_not_empty" {
			t.Fatalf("nonempty: %v", err)
		}
		if err := s.Mkdir(ctx, "occupied"); err != nil {
			return err
		}
		if err := s.Move(ctx, "folder with spaces", "occupied"); model.ErrorCode(err) != "already_exists" {
			t.Fatalf("move overwrite: %v", err)
		}
		if err := s.Move(ctx, "folder with spaces", "renamed"); err != nil {
			return err
		}
		if err := s.Remove(ctx, "renamed", true); err != nil {
			return err
		}
		return s.Remove(ctx, "occupied", false)
	})
	if _, err := s.Read(ctx, "renamed/sub/a.note.md"); model.ErrorCode(err) != "not_found" {
		t.Fatalf("removed: %v", err)
	}
}

func TestRecursiveRemoveRejectsNestedSymlink(t *testing.T) {
	s, cfg := openStore(t)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfg.Content, "folder", "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cfg.Content, "folder", "sub", "escape")); err != nil {
		t.Fatal(err)
	}
	locked(t, s, func() error {
		if err := s.Remove(context.Background(), "folder", true); model.ErrorCode(err) != "outside_root" {
			t.Fatalf("recursive symlink: %v", err)
		}
		return nil
	})
	if _, err := os.Lstat(filepath.Join(cfg.Content, "folder", "sub", "escape")); err != nil {
		t.Fatalf("preflight must not delete entries: %v", err)
	}
}

func TestLockCancellation(t *testing.T) {
	s, cfg := openStore(t)
	other, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	for _, contender := range []*Store{s, other} {
		locked(t, s, func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			called := false
			err := contender.WithLock(ctx, func() error { called = true; return nil })
			if !errors.Is(err, context.DeadlineExceeded) || called {
				t.Fatalf("waiting lock: called=%v err=%v", called, err)
			}
			return nil
		})
		locked(t, contender, func() error { return nil })
	}
}

func TestWithLockSerializesAndReleasesAfterError(t *testing.T) {
	s, cfg := openStore(t)
	other, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var active atomic.Int32
	errs := make(chan error, 12)
	for i := range 12 {
		go func() {
			store := []*Store{s, other}[i%2]
			errs <- store.WithLock(context.Background(), func() error {
				if active.Add(1) != 1 {
					t.Error("overlapping critical sections")
				}
				time.Sleep(time.Millisecond)
				active.Add(-1)
				return nil
			})
		}()
	}
	for range 12 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	sentinel := errors.New("callback failed")
	if err := s.WithLock(context.Background(), func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	locked(t, other, func() error { return nil })
}

func TestCanceledOperationsDoNotMutate(t *testing.T) {
	s, _ := openStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	locked(t, s, func() error {
		for _, err := range []error{s.Write(ctx, "a.note.md", "", []byte("body")), s.Mkdir(ctx, "folder"), s.Move(ctx, "from", "to"), s.Remove(ctx, "missing", true)} {
			if !errors.Is(err, context.Canceled) {
				t.Errorf("cancellation: %v", err)
			}
		}
		return nil
	})
	if _, err := s.Read(ctx, "a.note.md"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.Walk(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := s.WithLock(ctx, func() error { t.Error("canceled callback ran"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	entries, err := s.Walk(context.Background())
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled operation mutated: %+v %v", entries, err)
	}
}

func TestStorePinsContentRoot(t *testing.T) {
	s, cfg := openStore(t)
	outside := t.TempDir()
	retired := filepath.Join(cfg.Root, "original content")
	if err := os.Rename(cfg.Content, retired); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, cfg.Content); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	locked(t, s, func() error { return s.Write(ctx, "a.note.md", "", []byte("original root")) })
	raw, err := os.ReadFile(filepath.Join(retired, "a.note.md"))
	if err != nil || string(raw) != "original root" {
		t.Fatalf("unpinned content root: %q %v", raw, err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("escaped content root: %+v %v", entries, err)
	}
	if _, err := Open(cfg); model.ErrorCode(err) != "outside_root" {
		t.Fatalf("new store accepted replaced layout: %v", err)
	}
}

func TestLockRejectsSymlink(t *testing.T) {
	s, cfg := openStore(t)
	outside := filepath.Join(t.TempDir(), "lock target")
	if err := os.WriteFile(outside, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cfg.RunDir, "store.lock")); err != nil {
		t.Fatal(err)
	}
	err := s.WithLock(context.Background(), func() error { t.Error("callback ran through lock symlink"); return nil })
	if model.ErrorCode(err) != "outside_root" {
		t.Fatalf("lock symlink: %v", err)
	}
	raw, err := os.ReadFile(outside)
	if err != nil || string(raw) != "unchanged" {
		t.Fatalf("lock target changed: %q %v", raw, err)
	}
}

func TestFlockCoordinatesProcesses(t *testing.T) {
	const helperRoot = "RECALL_FSSTORE_TEST_PROCESS_ROOT"
	if root := os.Getenv(helperRoot); root != "" {
		cfg, err := config.Resolve(root)
		if err != nil {
			t.Fatal(err)
		}
		s, err := Open(cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.WithLock(context.Background(), func() error {
			if _, err := io.WriteString(os.Stdout, "locked\n"); err != nil {
				return err
			}
			_, err := io.ReadFull(os.Stdin, make([]byte, 1))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	s, cfg := openStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFlockCoordinatesProcesses$")
	cmd.Env = append(os.Environ(), helperRoot+"="+cfg.Root)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("child did not acquire lock: %q %v", line, err)
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, 40*time.Millisecond)
	defer waitCancel()
	err = s.WithLock(waitCtx, func() error { t.Error("cross-process lock overlapped"); return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("cross-process wait: %v", err)
	}
	if _, err := stdin.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	locked(t, s, func() error { return nil })
}

func TestConcurrentSymlinkSwapsCannotEscape(t *testing.T) {
	s, cfg := openStore(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside.note.md"), []byte("external bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	active, parked := filepath.Join(cfg.Content, "active"), filepath.Join(cfg.Content, "parked")
	if err := os.Mkdir(active, 0700); err != nil {
		t.Fatal(err)
	}
	stop, done := make(chan struct{}), make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			for _, op := range []func() error{
				func() error { return os.Rename(active, parked) },
				func() error { return os.Symlink(outside, active) },
				func() error { return os.Remove(active) },
				func() error { return os.Rename(parked, active) },
			} {
				if err := op(); err != nil {
					done <- err
					return
				}
			}
		}
	}()
	ctx := context.Background()
	locked(t, s, func() error {
		for i := range 64 {
			// All errors are allowed while a noncooperating editor swaps an
			// ancestor. No operation may read or mutate the outside directory.
			if raw, err := s.Read(ctx, "active/outside.note.md"); err == nil {
				t.Errorf("read escaped root: %q", raw)
			}
			_ = s.Write(ctx, fmt.Sprintf("active/new-%d.note.md", i), "", []byte("local"))
			_ = s.Mkdir(ctx, fmt.Sprintf("active/folder-%d", i))
			_ = s.Move(ctx, "active/outside.note.md", "stolen.note.md")
			_ = s.Remove(ctx, "active/outside.note.md", true)
		}
		return nil
	})
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(outside, "outside.note.md"))
	if err != nil || string(raw) != "external bytes" {
		t.Fatalf("external target modified: %q %v", raw, err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 1 {
		t.Fatalf("created entries outside content: %+v %v", entries, err)
	}
}

// checkpointContext deterministically schedules a filesystem replacement at an
// existing cancellation checkpoint. All filesystem calls remain real.
type checkpointContext struct {
	context.Context
	checks, at int
	action     func()
}

func (c *checkpointContext) Err() error {
	c.checks++
	if c.checks == c.at {
		c.action()
	}
	return c.Context.Err()
}

func seedAncestorSwap(t *testing.T, cfg config.Config) func() {
	t.Helper()
	for _, name := range []string{"active", "victim"} {
		if err := os.Mkdir(filepath.Join(cfg.Content, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"active/a.note.md": "original", "victim/a.note.md": "victim"} {
		if err := os.WriteFile(filepath.Join(cfg.Content, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return func() {
		if err := os.Rename(filepath.Join(cfg.Content, "active"), filepath.Join(cfg.Content, "parked")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("victim", filepath.Join(cfg.Content, "active")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenNoFollowRejectsAncestorSwapAfterValidation(t *testing.T) {
	s, cfg := openStore(t)
	swap := seedAncestorSwap(t, cfg)
	// Reproduce the exact validation/use gap without timing or a production
	// hook: first validate real components, then replace the ancestor, then
	// invoke the opener sharing Read's component resolver.
	for _, path := range []string{"active", "active/a.note.md"} {
		info, err := os.Lstat(filepath.Join(cfg.Content, path))
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("initial validation: %v", err)
		}
	}
	swap()
	f, err := openNoFollow(s.content, "active/a.note.md", os.O_RDONLY, 0)
	if err == nil {
		defer f.Close()
		raw, readErr := io.ReadAll(f)
		t.Fatalf("followed replaced ancestor: raw=%q readErr=%v", raw, readErr)
	}
	if model.ErrorCode(err) != "outside_root" {
		t.Fatalf("ancestor swap: %v", err)
	}
}

func TestDirectoryOpenRejectsSymlinkInstalledAfterStat(t *testing.T) {
	for _, layout := range []bool{false, true} {
		name := "ancestor"
		if layout {
			name = "layout"
		}
		t.Run(name, func(t *testing.T) {
			s, cfg := openStore(t)
			parent, component := s.content, "active"
			var swap func()
			if !layout {
				swap = seedAncestorSwap(t, cfg)
			} else {
				r, err := os.OpenRoot(cfg.Root)
				if err != nil {
					t.Fatal(err)
				}
				defer r.Close()
				parent, err = r.Open(".")
				if err != nil {
					t.Fatal(err)
				}
				defer parent.Close()
				component = "content"
				swap = func() {
					if err := os.Rename(cfg.Content, filepath.Join(cfg.Root, "parked")); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink("models", cfg.Content); err != nil {
						t.Fatal(err)
					}
				}
			}
			stat, err := checkedStatAt(parent, component, component)
			if err != nil || fileType(stat) != unix.S_IFDIR {
				t.Fatalf("directory validation: %v", err)
			}
			swap()
			// This is the actual descriptor-producing operation used after the
			// stat by both parent resolution and layout pinning. A second stat
			// cannot make this test pass: the kernel must reject the link.
			f, err := openAt(parent, component, component, os.O_RDONLY|unix.O_DIRECTORY, 0)
			if err == nil {
				f.Close()
				t.Fatal("directory open followed a post-stat relative symlink")
			}
		})
	}
}

func TestWalkDoesNotFollowDirectorySwap(t *testing.T) {
	s, cfg := openStore(t)
	swap := seedAncestorSwap(t, cfg)
	if err := os.Remove(filepath.Join(cfg.Content, "active", "a.note.md")); err != nil {
		t.Fatal(err)
	}
	// In the original implementation, the third checkpoint is the callback
	// for the already classified active directory, before WalkDir opens it.
	ctx := &checkpointContext{Context: context.Background(), at: 3, action: swap}
	entries, err := s.Walk(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.checks < ctx.at {
		t.Fatal("swap checkpoint was not reached")
	}
	for _, entry := range entries {
		if entry.Path == "active/a.note.md" {
			t.Fatal("walk followed replacement into victim")
		}
	}
}

func TestOpenDoesNotPinRelativeLayoutSymlink(t *testing.T) {
	_, cfg := openStore(t)
	if err := os.WriteFile(filepath.Join(cfg.Content, "marker"), []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.ModelsDir, "marker"), []byte("models"), 0600); err != nil {
		t.Fatal(err)
	}
	parked := filepath.Join(cfg.Root, "parked-content")
	stop, done := make(chan struct{}), make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			for _, op := range []func() error{
				func() error { return os.Rename(cfg.Content, parked) },
				func() error { return os.Symlink("models", cfg.Content) },
				func() error { return os.Remove(cfg.Content) },
				func() error { return os.Rename(parked, cfg.Content) },
			} {
				if err := op(); err != nil {
					done <- err
					return
				}
			}
		}
	}()
	defer func() {
		close(stop)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for range 256 {
		s, err := Open(cfg)
		if err != nil {
			continue
		}
		raw, err := s.Read(context.Background(), "marker")
		closeErr := s.Close()
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		if err != nil || string(raw) != "content" {
			t.Fatalf("pinned redirected layout: %q %v", raw, err)
		}
	}
}

func TestRemoveKeepsValidatedParentAcrossAncestorSwap(t *testing.T) {
	s, cfg := openStore(t)
	swap := seedAncestorSwap(t, cfg)
	// The old recursive removal validates this file and then checks context
	// in its walk callback. Replace the ancestor at that exact boundary.
	ctx := &checkpointContext{Context: context.Background(), at: 2, action: swap}
	locked(t, s, func() error { return s.Remove(ctx, "active/a.note.md", true) })
	if ctx.checks < ctx.at {
		t.Fatal("swap checkpoint was not reached")
	}
	raw, err := os.ReadFile(filepath.Join(cfg.Content, "victim", "a.note.md"))
	if err != nil || string(raw) != "victim" {
		t.Fatalf("removed redirected victim: %q %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Content, "parked", "a.note.md")); !os.IsNotExist(err) {
		t.Fatalf("original source was not removed: %v", err)
	}
}

func TestMutatorsKeepPinnedParentsAcrossAncestorSwap(t *testing.T) {
	for _, operation := range []string{"mkdir", "move_source", "move_target", "remove_tree"} {
		t.Run(operation, func(t *testing.T) {
			s, cfg := openStore(t)
			swap := seedAncestorSwap(t, cfg)
			if err := os.WriteFile(filepath.Join(cfg.Content, "move.note.md"), []byte("move"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, dir := range []string{"active/tree", "victim/tree"} {
				if err := os.Mkdir(filepath.Join(cfg.Content, dir), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cfg.Content, dir, "child.note.md"), []byte(dir), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx := &checkpointContext{Context: context.Background(), at: 2, action: swap}
			locked(t, s, func() error {
				switch operation {
				case "mkdir":
					return s.Mkdir(ctx, "active/new")
				case "move_source":
					return s.Move(ctx, "active/a.note.md", "moved.note.md")
				case "move_target":
					return s.Move(ctx, "move.note.md", "active/new.note.md")
				default:
					return s.Remove(ctx, "active/tree", true)
				}
			})
			if ctx.checks < ctx.at {
				t.Fatal("swap checkpoint was not reached")
			}
			for path, want := range map[string]string{"victim/a.note.md": "victim", "victim/tree/child.note.md": "victim/tree"} {
				raw, err := os.ReadFile(filepath.Join(cfg.Content, path))
				if err != nil || string(raw) != want {
					t.Fatalf("redirected mutation of %s: %q %v", path, raw, err)
				}
			}
			for _, name := range []string{"new", "new.note.md"} {
				if _, err := os.Lstat(filepath.Join(cfg.Content, "victim", name)); !os.IsNotExist(err) {
					t.Fatalf("created a victim entry %s: %v", name, err)
				}
			}
			switch operation {
			case "mkdir":
				if info, err := os.Stat(filepath.Join(cfg.Content, "parked", "new")); err != nil || !info.IsDir() {
					t.Fatalf("original directory creation: %v", err)
				}
			case "move_source", "move_target":
				path, want := "moved.note.md", "original"
				if operation == "move_target" {
					path, want = "parked/new.note.md", "move"
				}
				raw, err := os.ReadFile(filepath.Join(cfg.Content, path))
				if err != nil || string(raw) != want {
					t.Fatalf("original directory move: %q %v", raw, err)
				}
			case "remove_tree":
				if _, err := os.Lstat(filepath.Join(cfg.Content, "parked", "tree")); !os.IsNotExist(err) {
					t.Fatalf("original tree was not removed: %v", err)
				}
			}
		})
	}
}

func TestRemoveRejectsSymlinkIntroducedAfterPreflight(t *testing.T) {
	s, cfg := openStore(t)
	for _, dir := range []string{"tree", "tree/sub", "victim"} {
		if err := os.Mkdir(filepath.Join(cfg.Content, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"tree/sub/a.note.md", "victim/a.note.md"} {
		if err := os.WriteFile(filepath.Join(cfg.Content, path), []byte(path), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Checkpoints 3..6 preflight tree/sub/a.note.md; checkpoint 7 is the
	// deletion pass's first child, after that clean preflight has finished.
	ctx := &checkpointContext{Context: context.Background(), at: 7, action: func() {
		if err := os.Rename(filepath.Join(cfg.Content, "tree", "sub"), filepath.Join(cfg.Content, "parked")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../victim", filepath.Join(cfg.Content, "tree", "sub")); err != nil {
			t.Fatal(err)
		}
	}}
	locked(t, s, func() error {
		if err := s.Remove(ctx, "tree", true); model.ErrorCode(err) != "outside_root" {
			t.Fatalf("post-preflight symlink: %v", err)
		}
		return nil
	})
	if ctx.checks < ctx.at {
		t.Fatal("swap checkpoint was not reached")
	}
	raw, err := os.ReadFile(filepath.Join(cfg.Content, "victim", "a.note.md"))
	if err != nil || string(raw) != "victim/a.note.md" {
		t.Fatalf("victim deletion after preflight: %q %v", raw, err)
	}
}
