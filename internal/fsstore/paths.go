package fsstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"recall/internal/model"
)

func appError(code, path, message string) error {
	return &model.Error{Code: code, Path: path, Message: message}
}

// checkPath rejects lexical escapes and every observed symlink component.
// All subsequent I/O still uses os.Root: the checks are not the security
// boundary, and a concurrent external rename cannot escape the content root.
func (s *Store) checkPath(path string) error {
	if path == "" || path == "." || filepath.IsAbs(path) {
		return appError("outside_root", path, "a path beneath content is required")
	}
	parts := strings.Split(path, string(filepath.Separator))
	for _, part := range parts {
		if part == ".." {
			return appError("outside_root", path, "parent traversal is not allowed")
		}
		if part == "" || part == "." || strings.ContainsRune(part, 0) {
			return appError("invalid_argument", path, "invalid path component")
		}
	}
	for i := range parts {
		info, err := s.content.Lstat(filepath.Join(parts[:i+1]...))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return pathError(path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return appError("outside_root", path, "symlinks are not allowed")
		}
	}
	return nil
}

func pathError(path string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, os.ErrNotExist):
		return appError("not_found", path, "path not found")
	case errors.Is(err, unix.ENOTEMPTY):
		return appError("folder_not_empty", path, "folder is not empty")
	case errors.Is(err, os.ErrExist):
		return appError("already_exists", path, "path already exists")
	case errors.Is(err, unix.ELOOP):
		return appError("outside_root", path, "symlinks are not allowed")
	case errors.Is(err, unix.EISDIR), errors.Is(err, unix.ENOTDIR):
		return appError("invalid_argument", path, "unexpected file type")
	default:
		return err
	}
}

func (s *Store) syncDir(path string) error {
	f, err := s.content.Open(path)
	if err != nil {
		return pathError(path, err)
	}
	return errors.Join(f.Sync(), f.Close())
}

// os.Root.OpenFile resolves symlinks even when passed O_NOFOLLOW. Resolve the
// parent through the confined Root, then use a no-follow openat on the single
// basename so a concurrently replaced leaf cannot be followed either.
func openNoFollow(root *os.Root, path string, flags int, mode os.FileMode) (*os.File, error) {
	parent, err := root.Open(filepath.Dir(path))
	if err != nil {
		return nil, pathError(path, err)
	}
	defer parent.Close()
	fd, err := unix.Openat(int(parent.Fd()), filepath.Base(path), flags|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, uint32(mode.Perm()))
	if err != nil {
		return nil, pathError(path, err)
	}
	return os.NewFile(uintptr(fd), path), nil
}
