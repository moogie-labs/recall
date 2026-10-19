// Package fsstore provides root-confined source storage. Callers MUST hold
// WithLock around every mutation and scanner reconciliation. Mutators never
// acquire locks themselves, so one application transaction can mutate sources,
// reconcile them, and update the derived index under a single lock.
package fsstore

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"recall/internal/config"
)

type Entry struct {
	Path      string `json:"path"`
	IsDir     bool   `json:"is_dir"`
	IsSymlink bool   `json:"is_symlink"`
}

type Store struct {
	content *os.Root
	run     *os.Root
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
	for _, name := range []string{"config.json", "content", "run", "models", "runtime"} {
		info, err := r.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			return nil, appError("not_initialized", name, "collection initialization is incomplete")
		}
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, appError("outside_root", name, "symlinks are not allowed")
		}
		if name == "config.json" && !info.Mode().IsRegular() || name != "config.json" && !info.IsDir() {
			return nil, appError("not_initialized", name, "invalid collection layout")
		}
	}
	content, err := r.OpenRoot("content")
	if err != nil {
		return nil, err
	}
	run, err := r.OpenRoot("run")
	if err != nil {
		content.Close()
		return nil, err
	}
	return &Store{content: content, run: run}, nil
}

func (s *Store) Close() error { return errors.Join(s.content.Close(), s.run.Close()) }

func (s *Store) Read(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.checkPath(path); err != nil {
		return nil, err
	}
	f, err := openNoFollow(s.content, path, os.O_RDONLY, 0)
	if err != nil {
		return nil, pathError(path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, appError("invalid_argument", path, "a regular file is required")
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, pathError(path, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return raw, nil
}

// Mkdir creates one directory; its parent must already exist. Caller holds WithLock.
func (s *Store) Mkdir(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkPath(path); err != nil {
		return err
	}
	if err := s.content.Mkdir(path, 0755); err != nil {
		return pathError(path, err)
	}
	return s.syncDir(filepath.Dir(path))
}

// Move rejects existing destinations under the caller-owned WithLock. Like
// revision-aware writes, the check/rename pair coordinates cooperating writers.
func (s *Store) Move(ctx context.Context, from, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkPath(from); err != nil {
		return err
	}
	if err := s.checkPath(to); err != nil {
		return err
	}
	if _, err := s.content.Lstat(to); err == nil {
		return appError("already_exists", to, "path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return pathError(to, err)
	}
	if err := s.content.Rename(from, to); err != nil {
		return pathError(from, err)
	}
	if err := s.syncDir(filepath.Dir(from)); err != nil {
		return err
	}
	if filepath.Dir(from) != filepath.Dir(to) {
		return s.syncDir(filepath.Dir(to))
	}
	return nil
}

// Remove permanently deletes a source path. A recursive deletion is preflighted
// to reject symlinks before removing anything. Caller holds WithLock.
func (s *Store) Remove(ctx context.Context, path string, recursive bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkPath(path); err != nil {
		return err
	}
	if _, err := s.content.Lstat(path); err != nil {
		return pathError(path, err)
	}
	if recursive {
		err := fs.WalkDir(s.content.FS(), path, func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return pathError(name, err)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return appError("outside_root", name, "symlinks are not allowed")
			}
			return nil
		})
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.content.RemoveAll(path); err != nil {
			return pathError(path, err)
		}
	} else if err := s.content.Remove(path); err != nil {
		return pathError(path, err)
	}
	return s.syncDir(filepath.Dir(path))
}

// Walk lists relative paths in lexical order, excluding the content root.
// Symlinks are returned for unmanaged diagnostics, never traversed.
func (s *Store) Walk(ctx context.Context) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries := []Entry{}
	err := fs.WalkDir(s.content.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return pathError(path, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path != "." {
			entries = append(entries, Entry{Path: path, IsDir: entry.IsDir(), IsSymlink: entry.Type()&os.ModeSymlink != 0})
		}
		return nil
	})
	return entries, err
}
