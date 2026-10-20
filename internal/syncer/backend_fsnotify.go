//go:build !darwin

package syncer

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"

	"recall/internal/model"
)

func newWatchSource() (*watchSource, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &watchSource{
		refresh: func(ctx context.Context, root string) error { return refreshNotify(ctx, w, root) },
		forget: func(event fsnotify.Event) error {
			if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
				for _, path := range w.WatchList() {
					if path == event.Name || strings.HasPrefix(path, event.Name+string(filepath.Separator)) {
						if err := w.Remove(path); !absentWatch(err) {
							return err
						}
					}
				}
			}
			return nil
		},
		Close: w.Close, events: w.Events, errors: w.Errors,
	}, nil
}

func absentWatch(err error) bool {
	return err == nil || errors.Is(err, fsnotify.ErrNonExistentWatch) || errors.Is(err, os.ErrNotExist)
}

// Non-Darwin fsnotify registration retains the existing directory-watch path.
// Darwin cannot use it: its backend implicitly opens child symlink targets and
// reinstalls those watches independently of our refresh/cleanup.
func refreshNotify(ctx context.Context, watcher *fsnotify.Watcher, root string) error {
	current := make(map[string]bool)
	for _, path := range watcher.WatchList() {
		current[path] = true
	}
	desired := make(map[string]bool)
	var addErr error
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		if path == root && (!entry.IsDir() || entry.Type()&os.ModeSymlink != 0) {
			return &model.Error{Code: "outside_root", Message: "content root must be a real directory"}
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if err := watcher.Remove(path); !absentWatch(err) {
				addErr = errors.Join(addErr, err)
			}
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		desired[path] = true
		if !current[path] {
			addErr = errors.Join(addErr, watcher.Add(path))
		}
		return nil
	})
	if err != nil {
		return errors.Join(err, addErr)
	}
	for path := range current {
		if !desired[path] {
			if err := watcher.Remove(path); !absentWatch(err) {
				addErr = errors.Join(addErr, err)
			}
		}
	}
	return addErr
}
