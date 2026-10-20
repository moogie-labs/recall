// Package fsstore provides root-confined source storage. Callers MUST hold
// WithLock around every mutation and scanner reconciliation. Mutators never
// acquire locks themselves, so one application transaction can mutate sources,
// reconcile them, and update the derived index under a single lock.
package fsstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"recall/internal/config"
	"recall/internal/model"
)

type Entry struct {
	Path      string `json:"path"`
	IsDir     bool   `json:"is_dir"`
	IsSymlink bool   `json:"is_symlink"`
}

type Store struct {
	content     *os.File
	run         *os.File
	contentPath string
}

// Open requires explicit initialization; it never creates the layout. Directory
// handles pin the real content/run roots, including across external renames.
func Open(cfg config.Config) (*Store, error) {
	r, err := os.OpenRoot(cfg.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, appError("not_initialized", "", "initialize the collection first")
	}
	if err != nil {
		return nil, err
	}
	defer r.Close()
	// Start with Go's root-confined anchor, then pin each layout directory
	// with a descriptor-producing no-follow open, not Root.OpenRoot (which
	// can follow a relative symlink installed after a layout stat).
	root, err := r.Open(".")
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for _, name := range []string{"config.json", "content", "run", "models", "runtime"} {
		stat, err := checkedStatAt(root, name, name)
		if model.ErrorCode(err) == "not_found" {
			return nil, appError("not_initialized", name, "collection initialization is incomplete")
		}
		if err != nil {
			return nil, err
		}
		if name == "config.json" && fileType(stat) != unix.S_IFREG || name != "config.json" && fileType(stat) != unix.S_IFDIR {
			return nil, appError("not_initialized", name, "invalid collection layout")
		}
	}
	content, err := openDirectoryAt(root, "content", "content")
	if err != nil {
		return nil, err
	}
	run, err := openDirectoryAt(root, "run", "run")
	if err != nil {
		content.Close()
		return nil, err
	}
	return &Store{content: content, run: run, contentPath: filepath.Join(cfg.Root, "content")}, nil
}

// SourcePath resolves an actual regular source for an external editor. Unlike
// descriptor-based reads, an editor needs a pathname: reject replaced roots and
// symlinked ancestors as well as symlinked content entries at handoff time.
// No pathname API can prevent a non-cooperating rename after this check.
func (s *Store) SourcePath(ctx context.Context, path string) (string, error) {
	if _, err := s.Read(ctx, path); err != nil {
		return "", err
	}
	if !filepath.IsAbs(s.contentPath) {
		return "", appError("outside_root", path, "absolute content root required")
	}
	// Reuse the descriptor-producing no-follow directory walk for the actual
	// absolute path too; don't follow a root/ancestor link even to inspect it.
	dir, err := os.Open(string(filepath.Separator))
	if err != nil {
		return "", err
	}
	for _, part := range strings.Split(strings.TrimPrefix(s.contentPath, string(filepath.Separator)), string(filepath.Separator)) {
		next, err := openDirectoryAt(dir, part, path)
		dir.Close()
		if err != nil {
			return "", err
		}
		dir = next
	}
	defer dir.Close()
	current, err := dir.Stat()
	if err != nil {
		return "", pathError(path, err)
	}
	pinned, err := s.content.Stat()
	if err != nil {
		return "", err
	}
	if !os.SameFile(current, pinned) {
		return "", appError("outside_root", path, "content root was replaced; reopen the collection")
	}
	return filepath.Join(s.contentPath, path), nil
}

func (s *Store) Close() error { return errors.Join(s.content.Close(), s.run.Close()) }

func (s *Store) Read(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parent, name, err := parentAt(s.content, path)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	raw, _, err := readAt(ctx, parent, name, path)
	return raw, err
}

// Mkdir creates one directory; its parent must already exist. Caller holds WithLock.
func (s *Store) Mkdir(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parent, name, err := parentAt(s.content, path)
	if err != nil {
		return err
	}
	defer parent.Close()
	if _, err := checkedStatAt(parent, name, path); err != nil && model.ErrorCode(err) != "not_found" {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Mkdirat(int(parent.Fd()), name, 0755); err != nil {
		return pathError(path, err)
	}
	return parent.Sync()
}

// Move rejects existing destinations under the caller-owned WithLock. Like
// revision-aware writes, the check/rename pair coordinates cooperating writers.
func (s *Store) Move(ctx context.Context, from, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Validate both lexical paths before any filesystem lookup, so an invalid
	// destination is rejected even when its source does not exist.
	if err := validatePath(from); err != nil {
		return err
	}
	if err := validatePath(to); err != nil {
		return err
	}
	source, sourceName, err := parentAt(s.content, from)
	if err != nil {
		return err
	}
	defer source.Close()
	target, targetName, err := parentAt(s.content, to)
	if err != nil {
		return err
	}
	defer target.Close()
	if _, err := checkedStatAt(source, sourceName, from); err != nil {
		return err
	}
	if _, err := checkedStatAt(target, targetName, to); err == nil {
		return appError("already_exists", to, "path already exists")
	} else if model.ErrorCode(err) != "not_found" {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Renameat(int(source.Fd()), sourceName, int(target.Fd()), targetName); err != nil {
		return pathError(from, err)
	}
	return errors.Join(source.Sync(), target.Sync())
}

// Remove permanently deletes a source path. A recursive deletion is preflighted
// to reject symlinks before removing anything. Caller holds WithLock.
func (s *Store) Remove(ctx context.Context, path string, recursive bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parent, name, err := parentAt(s.content, path)
	if err != nil {
		return err
	}
	defer parent.Close()
	stat, err := checkedStatAt(parent, name, path)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if fileType(stat) == unix.S_IFDIR && recursive {
		dir, err := openDirectoryAt(parent, name, path)
		if err != nil {
			return err
		}
		defer dir.Close()
		// Preflight the whole tree before any deletion, using no-follow
		// descriptors for discovery as well as for the deletion pass.
		if err := walkAt(ctx, dir, path, func(path string, stat unix.Stat_t) error {
			if fileType(stat) == unix.S_IFLNK {
				return appError("outside_root", path, "symlinks are not allowed")
			}
			return nil
		}); err != nil {
			return err
		}
		if err := removeChildren(ctx, dir, path); err != nil {
			return err
		}
	}
	flags := 0
	if fileType(stat) == unix.S_IFDIR {
		flags = unix.AT_REMOVEDIR
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Unlinkat(int(parent.Fd()), name, flags); err != nil {
		return pathError(path, err)
	}
	return parent.Sync()
}

// Walk lists relative paths in lexical order, excluding the content root.
// Symlinks are returned for unmanaged diagnostics, never traversed.
func (s *Store) Walk(ctx context.Context) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := openDirectoryAt(s.content, ".", ".")
	if err != nil {
		return nil, err
	}
	defer root.Close()
	entries := []Entry{}
	err = walkAt(ctx, root, "", func(path string, stat unix.Stat_t) error {
		entries = append(entries, Entry{Path: path, IsDir: fileType(stat) == unix.S_IFDIR, IsSymlink: fileType(stat) == unix.S_IFLNK})
		return nil
	})
	return entries, err
}

// Read from a fresh descriptor so preflight and deletion do not share a
// directory stream offset. Readdir on this descriptor cannot follow a link.
func directoryNames(dir *os.File) ([]string, error) {
	reader, err := openDirectoryAt(dir, ".", ".")
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	names, err := reader.Readdirnames(-1)
	slices.Sort(names)
	return names, err
}

func walkAt(ctx context.Context, dir *os.File, prefix string, visit func(string, unix.Stat_t) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	names, err := directoryNames(dir)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(prefix, name)
		stat, err := statAt(dir, name, path)
		if err != nil {
			return err
		}
		if err := visit(path, stat); err != nil {
			return err
		}
		if fileType(stat) == unix.S_IFDIR {
			child, err := openDirectoryAt(dir, name, path)
			if err != nil {
				return err
			}
			err = walkAt(ctx, child, path, visit)
			child.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func removeChildren(ctx context.Context, dir *os.File, prefix string) error {
	names, err := directoryNames(dir)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(prefix, name)
		stat, err := checkedStatAt(dir, name, path)
		if err != nil {
			return err
		}
		flags := 0
		if fileType(stat) == unix.S_IFDIR {
			child, err := openDirectoryAt(dir, name, path)
			if err != nil {
				return err
			}
			err = removeChildren(ctx, child, path)
			child.Close()
			if err != nil {
				return err
			}
			flags = unix.AT_REMOVEDIR
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := unix.Unlinkat(int(dir.Fd()), name, flags); err != nil {
			return pathError(path, err)
		}
	}
	return dir.Sync()
}
