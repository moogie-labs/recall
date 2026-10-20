package syncer

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/sys/unix"
)

const metadataPollInterval = 100 * time.Millisecond

// fsnotify's Darwin backend opens symlink targets implicitly, including after
// directory changes. Use descriptor-relative metadata polling instead: no source
// bytes are read, all directory opens are O_NOFOLLOW, and each child stat is
// AT_SYMLINK_NOFOLLOW. The cost is O(entries) metadata work per 100ms, not a full
// scanner/index pass. Only actual metadata changes emit a coalesced hint.
type metadataWatcher struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	events chan fsnotify.Event
	errors chan error
	mu     sync.Mutex
	root   *os.File
	path   string
	last   map[string]metadataStamp
}

type metadataStamp struct {
	dev      int32
	ino      uint64
	mode     uint16
	size     int64
	modified unix.Timespec
	changed  unix.Timespec
}

func newWatchSource() (*watchSource, error) {
	ctx, cancel := context.WithCancel(context.Background())
	w := &metadataWatcher{
		ctx: ctx, cancel: cancel, done: make(chan struct{}),
		events: make(chan fsnotify.Event, 1), errors: make(chan error, 1),
	}
	go w.poll()
	return &watchSource{refresh: w.refresh, Close: w.Close, events: w.events, errors: w.errors}, nil
}

func (w *metadataWatcher) refresh(ctx context.Context, root string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.ctx.Err() != nil {
		return os.ErrClosed
	}
	if w.root != nil {
		return nil // The poll discovers additions/removals without path reopens.
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	anchor := os.NewFile(uintptr(fd), root)
	initial, err := metadataSnapshot(ctx, anchor, root)
	if err != nil {
		anchor.Close()
		return err
	}
	w.root, w.path, w.last = anchor, root, initial
	return nil
}

func (w *metadataWatcher) Close() error {
	w.cancel()
	<-w.done
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.root == nil {
		return nil
	}
	err := w.root.Close()
	w.root = nil
	return err
}

func (w *metadataWatcher) poll() {
	defer close(w.done)
	defer close(w.events)
	defer close(w.errors)
	ticker := time.NewTicker(metadataPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
			w.mu.Lock()
			if w.root == nil {
				w.mu.Unlock()
				continue
			}
			current, err := metadataSnapshot(w.ctx, w.root, w.path)
			changed := err == nil && !maps.Equal(current, w.last)
			if err == nil {
				w.last = current
			}
			path := w.path
			w.mu.Unlock()
			if w.ctx.Err() != nil {
				return
			}
			if err != nil {
				select {
				case w.errors <- err:
				default:
				}
			} else if changed {
				// A queued hint already obliges a complete App.Sync. Coalescing
				// additional differences cannot lose a newer saved source state.
				select {
				case w.events <- fsnotify.Event{Name: path, Op: fsnotify.Write}:
				default:
				}
			}
		}
	}
}

func metadataSnapshot(ctx context.Context, root *os.File, rootPath string) (map[string]metadataStamp, error) {
	entries := make(map[string]metadataStamp)
	var walk func(*os.File, string) error
	walk = func(dir *os.File, path string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var stat unix.Stat_t
		if err := unix.Fstat(int(dir.Fd()), &stat); err != nil {
			return err
		}
		entries[path] = stamp(stat)
		// A fresh stream is essential: dup would share the directory offset.
		fd, err := unix.Openat(int(dir.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		reader := os.NewFile(uintptr(fd), path)
		names, err := reader.Readdirnames(-1)
		reader.Close()
		if err != nil {
			return err
		}
		for _, name := range names {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := unix.Fstatat(int(dir.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				if disappearingEntry(err) {
					continue
				}
				return err
			}
			childPath := filepath.Join(path, name)
			if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
				// Symlink metadata is safe to observe; its target is never opened,
				// statted, traversed, or included in this fingerprint.
				entries[childPath] = stamp(stat)
				continue
			}
			fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
			if err != nil {
				if disappearingEntry(err) {
					continue
				}
				return err
			}
			child := os.NewFile(uintptr(fd), childPath)
			err = walk(child, childPath)
			child.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	err := walk(root, rootPath)
	return entries, err
}

func stamp(stat unix.Stat_t) metadataStamp {
	// Deliberately omit access time: enumerating a directory must not produce
	// another hint on every poll. ctime catches same-size, restored-mtime edits.
	return metadataStamp{stat.Dev, stat.Ino, stat.Mode, stat.Size, stat.Mtim, stat.Ctim}
}

func disappearingEntry(err error) bool {
	return errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ELOOP)
}
