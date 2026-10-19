package fsstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"

	"recall/internal/model"
)

// Write stages a same-directory temporary file, fsyncs it, rereads the source
// revision, commits, and fsyncs the directory. Caller MUST hold WithLock across
// Write and reconciliation/index application. An empty revision is create-only.
// Atomic replacement by noncooperating editors can still race the final rename;
// the revision check is an optimistic check, not a filesystem compare-and-swap.
func (s *Store) Write(ctx context.Context, path, expectedRevision string, raw []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parent, name, err := parentAt(s.content, path)
	if err != nil {
		if expectedRevision != "" && model.ErrorCode(err) == "not_found" {
			return appError("conflict", path, "source parent no longer exists")
		}
		return err
	}
	defer parent.Close()
	mode, err := checkRevisionAt(ctx, parent, name, path, expectedRevision)
	if err != nil {
		return err
	}
	var id [16]byte
	_, _ = rand.Read(id[:])
	tmp := fmt.Sprintf(".recall-%x.tmp", id)
	f, err := openAt(parent, tmp, path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return pathError(path, err)
	}
	// Every stage uses this same pinned parent, never a re-resolved path.
	defer unix.Unlinkat(int(parent.Fd()), tmp, 0)
	_, writeErr := f.Write(raw)
	if writeErr == nil {
		writeErr = f.Chmod(mode)
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	if _, err := checkRevisionAt(ctx, parent, name, path, expectedRevision); err != nil {
		return err
	}
	if expectedRevision == "" {
		// Link is an atomic, no-replace publication. Rename alone would violate
		// create-only semantics if an external creator appeared after our check.
		if err := unix.Linkat(int(parent.Fd()), tmp, int(parent.Fd()), name, 0); err != nil {
			return pathError(path, err)
		}
		if err := unix.Unlinkat(int(parent.Fd()), tmp, 0); err != nil {
			return pathError(path, err)
		}
	} else if err := unix.Renameat(int(parent.Fd()), tmp, int(parent.Fd()), name); err != nil {
		return pathError(path, err)
	}
	return parent.Sync()
}

func checkRevisionAt(ctx context.Context, parent *os.File, name, path, expected string) (os.FileMode, error) {
	if expected == "" {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		_, err := checkedStatAt(parent, name, path)
		if model.ErrorCode(err) == "not_found" {
			return 0600, nil
		}
		if err != nil {
			return 0, err
		}
		return 0, appError("already_exists", path, "create-only target already exists")
	}
	raw, mode, err := readAt(ctx, parent, name, path)
	if model.ErrorCode(err) == "not_found" {
		return 0, appError("conflict", path, "source no longer exists")
	}
	if err != nil {
		return 0, err
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != expected {
		return 0, appError("conflict", path, "source revision changed")
	}
	return mode, nil
}
