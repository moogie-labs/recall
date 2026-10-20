// Package syncer treats filesystem notifications as hints for full source
// reconciliation. Watching is an optimization, never the source of truth.
package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"recall/internal/model"
)

type settings struct {
	quietPeriod, rescanInterval time.Duration
	newWatcher                  func() (*watchSource, error)
}

type watchSource struct {
	refresh func(context.Context, string) error
	forget  func(fsnotify.Event) error
	Close   func() error
	events  <-chan fsnotify.Event
	errors  <-chan error
}

// Run monitors content recursively, including directories created or moved into
// contentRoot and editors' temp-file-and-rename saves. On Darwin a confined
// metadata poll replaces fsnotify's symlink-following kqueue backend, using a
// 100ms polling interval. It reconciles on startup, after 250ms of quiet, every
// 30 seconds, and after notification errors. Callers wrap App.Sync, which owns locking,
// normalization, and complete index application; Run never writes source bytes.
//
// Reconciliation and report callbacks are serialized and must honor ctx. Errors
// are reported without terminating recovery; transient missing-source/conflict
// failures retry after the quiet period. Run closes its watcher and returns
// ctx.Err() on cancellation. A nil report discards diagnostics.
func Run(ctx context.Context, contentRoot string, reconcile func(context.Context) error, report func(error)) error {
	return run(ctx, contentRoot, reconcile, report, settings{
		quietPeriod: 250 * time.Millisecond, rescanInterval: 30 * time.Second,
		newWatcher: newWatchSource,
	})
}

func run(ctx context.Context, contentRoot string, reconcile func(context.Context) error, report func(error), opts settings) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(contentRoot) || reconcile == nil {
		return &model.Error{Code: "invalid_argument", Message: "absolute content root and reconciliation callback are required"}
	}
	contentRoot = filepath.Clean(contentRoot)
	info, err := os.Lstat(contentRoot)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return &model.Error{Code: "outside_root", Message: "content root must not be a symlink"}
	}
	if !info.IsDir() {
		return &model.Error{Code: "invalid_argument", Message: "content root must be a directory"}
	}
	if report == nil {
		report = func(error) {}
	}
	var watcher *watchSource
	var events <-chan fsnotify.Event
	var failures <-chan error
	reset := func() {
		if watcher != nil {
			watcher.Close()
		}
		watcher, events, failures = nil, nil, nil
	}
	defer reset()

	quiet := time.NewTimer(opts.quietPeriod)
	quiet.Stop()
	defer quiet.Stop()
	var pending <-chan time.Time
	schedule := func() {
		quiet.Reset(opts.quietPeriod)
		pending = quiet.C
	}
	periodic := time.NewTicker(opts.rescanInterval)
	defer periodic.Stop()

	scan := func() {
		if ctx.Err() != nil {
			return
		}
		if watcher == nil {
			var err error
			watcher, err = opts.newWatcher()
			if err != nil {
				report(err)
			} else {
				events, failures = watcher.events, watcher.errors
			}
		}
		if watcher != nil {
			if err := watcher.refresh(ctx, contentRoot); err != nil && ctx.Err() == nil {
				report(err)
				if transient(err) {
					schedule()
				}
			}
		}
		if ctx.Err() != nil {
			return
		}
		if err := reconcile(ctx); err != nil && ctx.Err() == nil {
			report(err)
			if transient(err) {
				schedule()
			}
		}
	}
	scan()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-events:
			if !ok {
				report(errors.New("filesystem watcher closed; rebuilding watches and rescanning"))
				reset()
			} else if watcher.forget != nil {
				if err := watcher.forget(event); err != nil {
					report(err)
					reset()
				}
			}
			schedule()
		case err, ok := <-failures:
			if !ok {
				err = errors.New("filesystem watcher closed; rebuilding watches and rescanning")
			}
			if err != nil {
				report(err)
			}
			// Overflow can lose directory topology as well as file events. Replace
			// all watches before rescanning instead of trusting an old watch list.
			reset()
			schedule()
		case <-pending:
			pending = nil
			scan()
		case <-periodic.C:
			quiet.Stop()
			pending = nil
			scan()
		}
	}
}

func transient(err error) bool {
	return errors.Is(err, os.ErrNotExist) || model.ErrorCode(err) == "not_found" || model.ErrorCode(err) == "conflict"
}
