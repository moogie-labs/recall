package fsstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
	if err := s.checkPath(path); err != nil {
		return err
	}
	mode := os.FileMode(0600)
	if err := s.checkRevision(ctx, path, expectedRevision); err != nil {
		return err
	}
	if expectedRevision != "" {
		info, err := s.content.Lstat(path)
		if err != nil {
			return pathError(path, err)
		}
		mode = info.Mode()
	}
	var id [16]byte
	_, _ = rand.Read(id[:])
	tmp := filepath.Join(filepath.Dir(path), fmt.Sprintf(".recall-%x.tmp", id))
	f, err := s.content.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return pathError(path, err)
	}
	defer s.content.Remove(tmp)
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
	if err := s.checkRevision(ctx, path, expectedRevision); err != nil {
		return err
	}
	if expectedRevision == "" {
		// Link is an atomic, no-replace publication. Rename alone would violate
		// create-only semantics if an external creator appeared after our check.
		if err := s.content.Link(tmp, path); err != nil {
			return pathError(path, err)
		}
		if err := s.content.Remove(tmp); err != nil {
			return pathError(path, err)
		}
	} else if err := s.content.Rename(tmp, path); err != nil {
		return pathError(path, err)
	}
	return s.syncDir(filepath.Dir(path))
}

func (s *Store) checkRevision(ctx context.Context, path, expected string) error {
	if expected == "" {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.checkPath(path); err != nil {
			return err
		}
		info, err := s.content.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return pathError(path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return appError("outside_root", path, "symlinks are not allowed")
		}
		return appError("already_exists", path, "create-only target already exists")
	}
	raw, err := s.Read(ctx, path)
	if model.ErrorCode(err) == "not_found" {
		return appError("conflict", path, "source no longer exists")
	}
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != expected {
		return appError("conflict", path, "source revision changed")
	}
	return nil
}
