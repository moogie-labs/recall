package syncer

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"recall/internal/app"
	"recall/internal/config"
	"recall/internal/document"
	"recall/internal/index"
	"recall/internal/model"
)

type collection struct {
	a   *app.App
	cfg config.Config
	idx *index.Store
	doc model.Document
}

func newCollection(t *testing.T) collection {
	t.Helper()
	cfg, err := config.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	d, err := a.Create(t.Context(), app.CreateInput{Kind: model.Task, Name: "work", Body: "old body"})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := index.Open(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	return collection{a: a, cfg: cfg, idx: idx, doc: d}
}

func (c collection) sync(ctx context.Context) error { _, err := c.a.Sync(ctx); return err }

// This reads the derived index directly. app.Read/Tasks/List would perform their
// own Sync and could conceal a watcher that never reconciled anything.
func (c collection) tasks(t *testing.T) []model.Document {
	t.Helper()
	page, err := c.idx.Tasks(t.Context(), model.TaskQuery{})
	if err != nil {
		t.Fatal(err)
	}
	return page.Items
}

func eventually(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for watcher reconciliation")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func write(t *testing.T, path, raw string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
}

// Neovim-style save: replace the pathname, not the previously watched inode.
func replace(t *testing.T, path, raw string) {
	t.Helper()
	write(t, path+".tmp", raw)
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatal(err)
	}
}

func start(t *testing.T, root string, reconcile func(context.Context) error, opts *settings) (<-chan error, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done, scans, reports := make(chan error, 1), make(chan error, 128), make(chan error, 128)
	go func() {
		callback := func(ctx context.Context) error {
			err := reconcile(ctx)
			scans <- err
			return err
		}
		report := func(err error) { reports <- err }
		if opts == nil {
			done <- Run(ctx, root, callback, report)
		} else {
			done <- run(ctx, root, callback, report, *opts)
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Run exit = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Run did not stop after cancellation")
		}
		for {
			select {
			case err := <-reports:
				// A temporary source/directory entry can disappear while a
				// backend or reconciliation is enumerating children.
				if !errors.Is(err, os.ErrNotExist) {
					t.Errorf("unexpected watcher report: %v", err)
				}
			default:
				return
			}
		}
	})
	return scans, reports
}

func next(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for scan/report")
		return nil
	}
}

func fastSettings() settings {
	return settings{quietPeriod: 25 * time.Millisecond, rescanInterval: time.Hour, newWatcher: newWatchSource}
}

// Catches missing startup scans, inode-only watching, premature reconciliation,
// and duplicate final history after the scanner's own normalization events.
func TestRunStartupAndAtomicReplacement(t *testing.T) {
	c := newCollection(t)
	path := filepath.Join(c.cfg.Content, c.doc.Path)
	write(t, path, strings.Replace(c.doc.Raw, "old body", "offline body", 1))
	scans, _ := start(t, c.cfg.Content, c.sync, nil)
	if err := next(t, scans); err != nil {
		t.Fatal(err)
	}
	if got := c.tasks(t); len(got) != 1 || got[0].Body != "offline body" {
		t.Fatalf("startup did not recover offline edit: %+v", got)
	}
	for _, state := range []string{"in_progress", "stalled", "completed"} {
		raw := strings.Replace(c.doc.Raw, "state: planned", "state: "+state, 1)
		replace(t, path, strings.Replace(raw, "old body", "latest body 自由\r\n---\r\n", 1))
	}
	select {
	case err := <-scans:
		t.Fatalf("reconciled before the 250ms quiet period: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	eventually(t, func() bool {
		got := c.tasks(t)
		return len(got) == 1 && got[0].State == model.Completed && got[0].Body == "latest body 自由\r\n---\r\n"
	})
	time.Sleep(600 * time.Millisecond)
	got := c.tasks(t)[0]
	if len(got.History) != 2 || got.History[1].State != model.Completed {
		t.Fatalf("duplicate or intermediate history: %+v", got.History)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := document.Parse(c.doc.Path, raw)
	if err != nil || p.Document.Body != got.Body || len(p.Document.History) != 2 {
		t.Fatalf("latest source was lost: %+v %v", p.Document, err)
	}
	// A second replacement must still be observed after the first inode vanished.
	replace(t, path, strings.Replace(string(raw), "latest body", "another save", 1))
	eventually(t, func() bool { return strings.HasPrefix(c.tasks(t)[0].Body, "another save") })
}

func TestRunKeepsInPlaceEditsWithRestoredMtime(t *testing.T) {
	c := newCollection(t)
	path := filepath.Join(c.cfg.Content, c.doc.Path)
	opts := fastSettings()
	scans, _ := start(t, c.cfg.Content, c.sync, &opts)
	if err := next(t, scans); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Same byte length, no directory entry change, and no mtime difference:
	// monitoring just directories/size/mtime would miss this saved body.
	write(t, path, strings.Replace(c.doc.Raw, "old body", "new body", 1))
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return c.tasks(t)[0].Body == "new body" })
	if got := c.tasks(t)[0]; len(got.History) != 1 {
		t.Fatalf("body-only save changed history: %+v", got.History)
	}
}

func TestRunTracksNewNestedMovedDeletedAndRecreatedFolders(t *testing.T) {
	c := newCollection(t)
	opts := fastSettings()
	scans, _ := start(t, c.cfg.Content, c.sync, &opts)
	if err := next(t, scans); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(c.cfg.Content, "new", "nested")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "nested.task.md")
	write(t, path, "nested body")
	eventually(t, func() bool { return len(c.tasks(t)) == 2 })
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replace(t, path, strings.Replace(string(raw), "nested body", "nested edited", 1))
	eventually(t, func() bool {
		for _, d := range c.tasks(t) {
			if d.Path == "new/nested/nested.task.md" && d.Body == "nested edited" {
				return true
			}
		}
		return false
	})
	if err := os.Rename(filepath.Join(c.cfg.Content, "new"), filepath.Join(c.cfg.Content, "moved")); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		for _, d := range c.tasks(t) {
			if d.Path == "moved/nested/nested.task.md" {
				return true
			}
		}
		return false
	})
	for _, path := range []string{"moved/nested/nested.task.md", "moved/nested", "moved"} {
		if err := os.Remove(filepath.Join(c.cfg.Content, path)); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, func() bool { return len(c.tasks(t)) == 1 })
	dir = filepath.Join(c.cfg.Content, "moved", "nested")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, "recreated.task.md")
	write(t, path, "recreated")
	eventually(t, func() bool { return len(c.tasks(t)) == 2 })
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replace(t, path, strings.Replace(string(raw), "recreated", "watched again", 1))
	eventually(t, func() bool {
		for _, d := range c.tasks(t) {
			if d.Body == "watched again" {
				return true
			}
		}
		return false
	})
}

// Inject only the notification failure; keep the production backend, sources,
// scanner, locks, and index. Suppressing hints first makes the save unobservable
// without error recovery (the periodic fallback is disabled for this test).
func TestRunRecoversOverflowErrorsAndClosedWatcher(t *testing.T) {
	for _, mode := range []string{"overflow", "error", "missing temporary file", "closed"} {
		t.Run(mode, func(t *testing.T) {
			c := newCollection(t)
			opts := fastSettings()
			created := make(chan *watchSource, 16)
			// Never send on backend-owned channels: Close may race a test sender.
			// Inject only the error stream and hide the first backend's hints.
			injectedErrors := make(chan error, 1)
			first := true
			opts.newWatcher = func() (*watchSource, error) {
				w, err := newWatchSource()
				if err == nil {
					if first {
						w.errors = injectedErrors
						if mode != "closed" {
							w.events = nil
						}
						first = false
					}
					created <- w
				}
				return w, err
			}
			scans, reports := start(t, c.cfg.Content, c.sync, &opts)
			if err := next(t, scans); err != nil {
				t.Fatal(err)
			}
			w := <-created
			if mode == "closed" {
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(c.cfg.Content, c.doc.Path)
			replace(t, path, strings.Replace(c.doc.Raw, "old body", "missed save", 1))
			injected := errors.New("notification backend failed")
			if mode == "overflow" {
				injected = fsnotify.ErrEventOverflow
			}
			if mode == "missing temporary file" {
				injected = &os.PathError{Op: "lstat", Path: "editor.tmp", Err: os.ErrNotExist}
			}
			if mode != "closed" {
				injectedErrors <- injected
			}
			err := next(t, reports)
			if err == nil || (mode != "closed" && !errors.Is(err, injected)) {
				t.Fatalf("notification error not reported: %v", err)
			}
			eventually(t, func() bool { return c.tasks(t)[0].Body == "missed save" })
			// Error recovery must restore live watches, not just do one rescan.
			replace(t, path, strings.Replace(c.doc.Raw, "old body", "after recovery", 1))
			eventually(t, func() bool { return c.tasks(t)[0].Body == "after recovery" })
		})
	}
}

func TestRunPeriodicScanRecoversWithoutNotification(t *testing.T) {
	c := newCollection(t)
	opts := fastSettings()
	opts.rescanInterval = 75 * time.Millisecond
	opts.newWatcher = func() (*watchSource, error) {
		w, err := newWatchSource()
		if err == nil {
			w.events = nil // Simulate lost hints without changing source behavior.
		}
		return w, err
	}
	scans, _ := start(t, c.cfg.Content, c.sync, &opts)
	if err := next(t, scans); err != nil {
		t.Fatal(err)
	}
	replace(t, filepath.Join(c.cfg.Content, c.doc.Path), strings.Replace(c.doc.Raw, "old body", "periodic recovery", 1))
	eventually(t, func() bool { return c.tasks(t)[0].Body == "periodic recovery" })
}

// A failed watcher constructor must not suppress startup or periodic Sync.
func TestRunRecoversWatcherCreationFailure(t *testing.T) {
	c := newCollection(t)
	opts := fastSettings()
	opts.rescanInterval = 75 * time.Millisecond
	injected := errors.New("watch resources unavailable")
	attempts := 0
	opts.newWatcher = func() (*watchSource, error) {
		attempts++
		if attempts == 1 {
			return nil, injected
		}
		return newWatchSource()
	}
	scans, reports := start(t, c.cfg.Content, c.sync, &opts)
	if err := next(t, reports); !errors.Is(err, injected) {
		t.Fatalf("constructor error not reported: %v", err)
	}
	if err := next(t, scans); err != nil {
		t.Fatalf("startup did not run without a watcher: %v", err)
	}
	path := filepath.Join(c.cfg.Content, c.doc.Path)
	replace(t, path, strings.Replace(c.doc.Raw, "old body", "saved without watcher", 1))
	eventually(t, func() bool { return c.tasks(t)[0].Body == "saved without watcher" })
}

func TestRunRetriesTransientReconcileErrorsAfterQuietPeriod(t *testing.T) {
	c := newCollection(t)
	opts := fastSettings()
	attempts := 0
	scans, reports := start(t, c.cfg.Content, func(ctx context.Context) error {
		attempts++
		switch attempts {
		case 1:
			return &model.Error{Code: "not_found", Message: "source temporarily absent"}
		case 2:
			return os.ErrNotExist
		case 3:
			return &model.Error{Code: "conflict", Message: "source changed"}
		default:
			return c.sync(ctx)
		}
	}, &opts)
	for range 3 {
		if next(t, scans) == nil || next(t, reports) == nil {
			t.Fatal("transient error not surfaced")
		}
	}
	if err := next(t, scans); err != nil {
		t.Fatal(err)
	}
}

// Hold SQLite's writer lock so App.Sync is provably inside reconciliation:
// scanner normalization has reached disk, but complete index Apply cannot finish.
// Replace the source then release Apply. A later watcher pass must publish the
// latest body and exactly one final transition instead of the earlier snapshot.
func TestRunKeepsEditsMadeDuringReconciliation(t *testing.T) {
	c := newCollection(t)
	opts := fastSettings()
	scans, _ := start(t, c.cfg.Content, c.sync, &opts)
	if err := next(t, scans); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", c.cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("ROLLBACK")
	path := filepath.Join(c.cfg.Content, c.doc.Path)
	replace(t, path, strings.Replace(strings.Replace(c.doc.Raw, "state: planned", "state: stalled", 1), "old body", "intermediate body", 1))
	var normalized string
	eventually(t, func() bool {
		raw, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		p, err := document.Parse(c.doc.Path, raw)
		if err == nil && len(p.Document.History) == 2 && p.Document.History[1].State == model.Stalled {
			normalized = string(raw)
			return true
		}
		return false
	})
	replace(t, path, strings.Replace(strings.Replace(normalized, "state: stalled", "state: completed", 1), "intermediate body", "forced latest body", 1))
	if _, err := db.Exec("COMMIT"); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool {
		got := c.tasks(t)
		return len(got) == 1 && got[0].Body == "forced latest body" && got[0].State == model.Completed
	})
	time.Sleep(150 * time.Millisecond)
	got := c.tasks(t)[0]
	if len(got.History) != 3 || got.History[1].State != model.Stalled || got.History[2].State != model.Completed {
		t.Fatalf("duplicated or stale history: %+v", got.History)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := document.Parse(c.doc.Path, raw)
	if err != nil || p.Document.Body != "forced latest body" || len(p.Document.History) != 3 {
		t.Fatalf("forced edit lost: %+v %v", p.Document, err)
	}
}

func TestRunDoesNotWatchSymlinkDirectories(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	opts := fastSettings()
	var calls atomic.Int32
	scans, _ := start(t, root, func(context.Context) error { calls.Add(1); return nil }, &opts)
	if err := next(t, scans); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(outside, "outside.task.md"), "outside")
	time.Sleep(150 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("followed an unmanaged symlink: %d scans", calls.Load())
	}
}

func TestRunRejectsInvalidContentRootsBeforeReconciliation(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "file")
	write(t, file, "not a directory")
	for _, tt := range []struct{ path, code string }{
		{"relative", "invalid_argument"},
		{link, "outside_root"},
		{file, "invalid_argument"},
	} {
		t.Run(tt.code+filepath.Base(tt.path), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			called := false
			err := Run(ctx, tt.path, func(context.Context) error { called = true; return nil }, nil)
			if model.ErrorCode(err) != tt.code || called {
				t.Fatalf("invalid root = %v; reconcile called = %v", err, called)
			}
		})
	}
}

func TestRunCancellationStopsReconciliationAndClosesWatcher(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	opts := fastSettings()
	created := make(chan *watchSource, 1)
	opts.newWatcher = func() (*watchSource, error) {
		w, err := newWatchSource()
		if err == nil {
			created <- w
		}
		return w, err
	}
	entered := make(chan struct{})
	done := make(chan error, 1)
	root := t.TempDir()
	go func() {
		done <- run(ctx, root, func(ctx context.Context) error {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		}, nil, opts)
	}()
	<-entered
	w := <-created
	cancel()
	if err := next(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	select {
	case _, ok := <-w.events:
		if ok {
			t.Fatal("watcher event stream was not closed")
		}
	case <-time.After(time.Second):
		t.Fatal("watcher was not closed")
	}
	if err := Run(ctx, t.TempDir(), func(context.Context) error { t.Error("called after cancellation"); return nil }, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled Run = %v", err)
	}
}
