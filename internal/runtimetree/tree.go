// Package runtimetree describes an installed native runtime payload. Install
// and launch share this description, so a payload modified after verified
// installation (or supplied by an untrusted collection) cannot be executed.
package runtimetree

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"

	"golang.org/x/sys/unix"
)

// Entries maps each relative path beneath r to its type, regular-file
// permissions and content hash, or symlink target. Directory permissions are
// excluded because extraction creates them subject to the process umask.
// Symlinks are recorded, never followed.
func Entries(ctx context.Context, r *os.Root) (map[string]string, error) {
	tree := make(map[string]string)
	err := fs.WalkDir(r.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			tree[name] = "directory"
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := r.Readlink(name)
			tree[name] = "link:" + target
			return err
		}
		f, err := r.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		info, statErr := f.Stat()
		if statErr == nil && !info.Mode().IsRegular() {
			statErr = errors.New("runtime entry must be a regular file, directory or symlink")
		}
		hash := sha256.New()
		var readErr error
		if statErr == nil {
			_, readErr = io.Copy(hash, contextReader{ctx, f})
		}
		if err := errors.Join(statErr, readErr, f.Close()); err != nil {
			return err
		}
		tree[name] = fmt.Sprintf("%o:%x", info.Mode().Perm(), hash.Sum(nil))
		return nil
	})
	return tree, err
}

// Digest is a canonical SHA-256 of Entries. Length prefixes keep the encoding
// unambiguous for any path or link target bytes.
func Digest(ctx context.Context, r *os.Root) (string, error) {
	tree, err := Entries(ctx, r)
	if err != nil {
		return "", err
	}
	return DigestEntries(tree), nil
}

// DigestEntries hashes an already collected tree description.
func DigestEntries(tree map[string]string) string {
	names := make([]string, 0, len(tree))
	for name := range tree {
		names = append(names, name)
	}
	slices.Sort(names)
	hash := sha256.New()
	for _, name := range names {
		fmt.Fprintf(hash, "%d:%s%d:%s", len(name), name, len(tree[name]), tree[name])
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
