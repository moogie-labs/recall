package fsstore

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"recall/internal/model"
)

func appError(code, path, message string) error {
	return &model.Error{Code: code, Path: path, Message: message}
}

func validatePath(path string) error {
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

// withFD keeps even a shared anchor alive during its syscall, so concurrent
// Close cannot cause a recycled descriptor to be used as a directory root.
func withFD(file *os.File, fn func(int) error) error {
	conn, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var syscallErr error
	err = conn.Control(func(fd uintptr) { syscallErr = fn(int(fd)) })
	return errors.Join(err, syscallErr)
}

func statAt(parent *os.File, name, path string) (unix.Stat_t, error) {
	var stat unix.Stat_t
	err := withFD(parent, func(fd int) error { return unix.Fstatat(fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW) })
	return stat, pathError(path, err)
}

func fileType(stat unix.Stat_t) uint32 { return uint32(stat.Mode) & unix.S_IFMT }

func checkedStatAt(parent *os.File, name, path string) (unix.Stat_t, error) {
	stat, err := statAt(parent, name, path)
	if err == nil && fileType(stat) == unix.S_IFLNK {
		err = appError("outside_root", path, "symlinks are not allowed")
	}
	return stat, err
}

// name is always a single validated component (or "." when duplicating an
// anchor), never a path with ancestors the kernel could resolve implicitly.
func openAt(parent *os.File, name, path string, flags int, mode os.FileMode) (*os.File, error) {
	var fd int
	err := withFD(parent, func(parentFD int) (err error) {
		fd, err = unix.Openat(parentFD, name, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, uint32(mode.Perm()))
		return err
	})
	if err != nil {
		return nil, pathError(path, err)
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openDirectoryAt(parent *os.File, name, path string) (*os.File, error) {
	stat, err := checkedStatAt(parent, name, path)
	if err != nil {
		return nil, err
	}
	if fileType(stat) != unix.S_IFDIR {
		return nil, appError("invalid_argument", path, "a directory is required")
	}
	// The descriptor-producing operation enforces no-follow too; the stat
	// above is for validation/diagnostics, not the confinement boundary.
	return openAt(parent, name, path, os.O_RDONLY|unix.O_DIRECTORY, 0)
}

// parentAt walks one component at a time with O_DIRECTORY|O_NOFOLLOW. The
// returned private descriptor pins the checked parent across all subsequent
// operations, including an ancestor's rename/replacement by an in-root link.
func parentAt(root *os.File, path string) (*os.File, string, error) {
	if err := validatePath(path); err != nil {
		return nil, "", err
	}
	parent, err := openDirectoryAt(root, ".", ".")
	if err != nil {
		return nil, "", err
	}
	parts := strings.Split(path, string(filepath.Separator))
	for i, part := range parts[:len(parts)-1] {
		next, err := openDirectoryAt(parent, part, filepath.Join(parts[:i+1]...))
		parent.Close()
		if err != nil {
			return nil, "", err
		}
		parent = next
	}
	return parent, parts[len(parts)-1], nil
}

func openNoFollow(root *os.File, path string, flags int, mode os.FileMode) (*os.File, error) {
	parent, name, err := parentAt(root, path)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	return openAt(parent, name, path, flags, mode)
}

func readAt(ctx context.Context, parent *os.File, name, path string) ([]byte, os.FileMode, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	f, err := openAt(parent, name, path, os.O_RDONLY, 0)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, appError("invalid_argument", path, "a regular file is required")
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, 0, pathError(path, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	return raw, info.Mode(), nil
}
