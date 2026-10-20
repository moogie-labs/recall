package bootstrap

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"recall/internal/config"
)

// Fetch rechecks cached bytes before any network I/O. Downloads always restart
// into a private same-directory stage: a partial response can never be appended
// to a server's full 200 response or replace a previously installed asset.
func Fetch(ctx context.Context, client *http.Client, manifest Manifest, destination string, progress func(done, total int64)) error {
	r, err := os.OpenRoot(filepath.Dir(destination))
	if err != nil {
		return err
	}
	defer r.Close()
	return fetchAt(ctx, client, manifest, r, filepath.Base(destination), progress)
}

func fetchAt(ctx context.Context, client *http.Client, m Manifest, r *os.Root, name string, progress func(done, total int64)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateManifest(m); err != nil {
		return err
	}
	f, err := openRegular(r, name)
	if err == nil {
		verifyErr := verify(ctx, f, m)
		closeErr := f.Close()
		if verifyErr == nil {
			if progress != nil {
				progress(m.Size, m.Size)
			}
			return closeErr
		}
		var mismatch *assetMismatch
		if !errors.As(verifyErr, &mismatch) {
			return verifyErr
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %v", unavailable("asset download failed"), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return unavailable(fmt.Sprintf("asset download returned HTTP %d", resp.StatusCode))
	}
	if resp.ContentLength >= 0 && resp.ContentLength != m.Size {
		return invalidAsset("download size differs from pinned size")
	}
	tmp := stageName("download")
	f, err = r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer r.Remove(tmp)
	hash := sha256.New()
	reader := &progressReader{ctx: ctx, r: io.LimitReader(resp.Body, m.Size+1), total: m.Size, progress: progress}
	n, copyErr := io.Copy(io.MultiWriter(f, hash), reader)
	if copyErr == nil && (n != m.Size || !strings.EqualFold(fmt.Sprintf("%x", hash.Sum(nil)), m.SHA256)) {
		copyErr = invalidAsset("download checksum or size differs from pinned identity")
	}
	if copyErr == nil {
		copyErr = ctx.Err()
	}
	if copyErr == nil {
		copyErr = f.Sync()
	}
	if err := errors.Join(copyErr, f.Close()); err != nil {
		return err
	}
	if info, err := r.Lstat(name); err == nil && !info.Mode().IsRegular() {
		return invalidAsset("asset destination must be a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := r.Rename(tmp, name); err != nil {
		return err
	}
	return syncDir(r, ".")
}

// EnsureModel uses only the compiled-in identity, never cfg-supplied paths or a
// mutable release manifest. Configuration identities must match when supplied.
func EnsureModel(ctx context.Context, cfg config.Config, progress func(done, total int64)) error {
	m, _, err := pinnedManifests()
	if err != nil {
		return err
	}
	return ensureModel(ctx, cfg, m, nil, progress)
}

func ensureModel(ctx context.Context, cfg config.Config, m Manifest, client *http.Client, progress func(done, total int64)) error {
	if (cfg.ModelID != "" && cfg.ModelID != m.ID) || (cfg.ModelSHA256 != "" && cfg.ModelSHA256 != m.SHA256) {
		return invalidAsset("configured model differs from bundled model identity")
	}
	r, err := assetRoot(cfg, "models")
	if err != nil {
		return err
	}
	defer r.Close()
	return fetchAt(ctx, client, m, r, m.Filename, progress)
}

func assetRoot(cfg config.Config, name string) (*os.Root, error) {
	r, err := os.OpenRoot(cfg.Root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	info, err := r.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, invalidAsset("asset directory must be a real directory")
	}
	return openCheckedRoot(r, name, info)
}

// Root.OpenRoot confines traversal but can follow internal relative symlinks.
// Compare the opened inode with the checked real directory, so a concurrent
// layout swap cannot redirect asset writes into the user's content directory.
func openCheckedRoot(parent *os.Root, name string, checked os.FileInfo) (*os.Root, error) {
	r, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := r.Stat(".")
	if err != nil || !os.SameFile(checked, opened) {
		r.Close()
		if err != nil {
			return nil, err
		}
		return nil, invalidAsset("asset directory changed while opening it")
	}
	return r, nil
}

// assetMismatch distinguishes a stale cache from an I/O failure without
// suppressing read errors or cancellations and retrying them as downloads.
type assetMismatch struct{}

func (*assetMismatch) Error() string { return "asset checksum or size differs from pinned identity" }

func verify(ctx context.Context, f *os.File, m Manifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != m.Size {
		return &assetMismatch{}
	}
	hash := sha256.New()
	n, err := io.Copy(hash, &progressReader{ctx: ctx, r: io.LimitReader(f, m.Size+1)})
	if err != nil {
		return err
	}
	if n != m.Size || !strings.EqualFold(fmt.Sprintf("%x", hash.Sum(nil)), m.SHA256) {
		return &assetMismatch{}
	}
	return ctx.Err()
}

func openRegular(r *os.Root, name string) (*os.File, error) {
	f, err := r.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		if err != nil {
			return nil, err
		}
		return nil, invalidAsset("asset must be a regular file")
	}
	return f, nil
}

type progressReader struct {
	ctx         context.Context
	r           io.Reader
	done, total int64
	progress    func(done, total int64)
}

func (r *progressReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	r.done += int64(n)
	if r.progress != nil && n > 0 {
		r.progress(r.done, r.total)
	}
	return n, err
}

func stageName(kind string) string {
	var id [16]byte
	_, _ = rand.Read(id[:])
	return fmt.Sprintf(".recall-%s-%x.tmp", kind, id)
}

func syncDir(r *os.Root, name string) error {
	f, err := r.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
