package bootstrap

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"recall/assets"
	"recall/internal/config"
	"recall/internal/model"
	"recall/internal/runtimetree"
)

const archivePrefix = "llama-b11669/"

// InstallRuntime verifies the already bundled archive; it never downloads or
// executes a runtime. The entire payload stays together, including sibling
// dylibs and safe relative dylib symlinks.
func InstallRuntime(ctx context.Context, cfg config.Config, archivePath string) error {
	if err := supportedPlatform(runtime.GOOS + "-" + runtime.GOARCH); err != nil {
		return err
	}
	_, m, err := pinnedManifests()
	if err != nil {
		return err
	}
	return installRuntime(ctx, cfg, archivePath, m)
}

func installRuntime(ctx context.Context, cfg config.Config, archivePath string, m Manifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateManifest(m); err != nil {
		return err
	}
	if !singleName(m.ID) || (cfg.RuntimeID != "" && cfg.RuntimeID != m.ID) {
		return invalidAsset("configured runtime differs from bundled runtime identity")
	}
	directory, err := assets.RuntimeInstallationDirectory(m.ID, m.SHA256, m.InstallationID)
	if err != nil {
		return invalidAsset(err.Error())
	}
	r, err := assetRoot(cfg, "runtime")
	if err != nil {
		return err
	}
	defer r.Close()
	archiveRoot, err := os.OpenRoot(filepath.Dir(archivePath))
	if err != nil {
		return fmt.Errorf("%w: %v", unavailable("bundled runtime archive is missing"), err)
	}
	defer archiveRoot.Close()
	f, err := openRegular(archiveRoot, filepath.Base(archivePath))
	if err != nil {
		return fmt.Errorf("%w: %v", unavailable("cannot open bundled runtime archive"), err)
	}
	defer f.Close()
	if err := verify(ctx, f, m); err != nil {
		var mismatch *assetMismatch
		if errors.As(err, &mismatch) {
			return invalidAsset("bundled runtime checksum or size differs from pinned identity")
		}
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	stage := stageName("runtime")
	if err := r.Mkdir(stage, 0700); err != nil {
		return err
	}
	defer r.RemoveAll(stage) // Only this invocation's randomly named private stage.
	staging, err := r.OpenRoot(stage)
	if err != nil {
		return err
	}
	defer staging.Close()
	if err := extractVerifiedRuntime(ctx, f, staging, m); err != nil {
		return err
	}
	server, err := openRegular(staging, "llama-server")
	if err != nil {
		return invalidAsset("runtime payload is missing a regular llama-server")
	}
	info, statErr := server.Stat()
	closeErr := server.Close()
	if err := errors.Join(statErr, closeErr); err != nil {
		return err
	}
	if info.Mode()&0111 == 0 {
		return invalidAsset("llama-server is not executable")
	}
	if m.TreeSHA256 != "" {
		// The launch-time check compares against this same pinned digest, so
		// only the exact payload installed here can ever be executed.
		digest, err := runtimetree.Digest(ctx, staging)
		if err != nil {
			return err
		}
		if digest != m.TreeSHA256 {
			return invalidAsset("extracted runtime differs from pinned payload digest")
		}
	}
	if info, err := r.Lstat(directory); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return invalidAsset("installed runtime must be a real directory")
		}
		installed, err := openCheckedRoot(r, directory, info)
		if err != nil {
			return err
		}
		defer installed.Close()
		want, err := runtimetree.Entries(ctx, staging)
		if err != nil {
			return err
		}
		got, err := runtimetree.Entries(ctx, installed)
		if err != nil {
			return err
		}
		if !maps.Equal(got, want) {
			return invalidAsset("installed runtime differs from verified payload; existing files preserved")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.Rename(stage, directory); err != nil {
		return err
	}
	return syncDir(r, ".")
}

// Hash the compressed bytes actually consumed by extraction as well as checking
// the original file. An archive modified after the initial check cannot publish
// a payload built from different bytes, even though the descriptor is pinned.
func extractVerifiedRuntime(ctx context.Context, archive io.Reader, r *os.Root, m Manifest) error {
	hash := sha256.New()
	counted := &progressReader{ctx: ctx, r: io.LimitReader(archive, m.Size+1)}
	reader := io.TeeReader(counted, hash)
	if err := extractRuntime(ctx, reader, r); err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return err
	}
	if counted.done != m.Size || !strings.EqualFold(fmt.Sprintf("%x", hash.Sum(nil)), m.SHA256) {
		return invalidAsset("runtime archive changed during extraction")
	}
	return ctx.Err()
}

func extractRuntime(ctx context.Context, archive io.Reader, r *os.Root) error {
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return runtimeDecodingError(ctx, err, "invalid runtime gzip archive")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := make(map[string]bool)
	links := make(map[string]string)
	var dirs []string
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return runtimeDecodingError(ctx, err, "invalid runtime tar archive")
		}
		name, err := archiveName(h.Name)
		if err != nil {
			return err
		}
		if name == "." && h.Typeflag == tar.TypeDir {
			continue
		}
		if seen[name] {
			return invalidAsset("duplicate runtime archive entry")
		}
		seen[name] = true
		if err := r.MkdirAll(path.Dir(name), 0700); err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := r.MkdirAll(name, 0700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			f, err := r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return invalidAsset("conflicting runtime archive entry")
			}
			_, writeErr := io.Copy(f, &progressReader{ctx: ctx, r: tr})
			if writeErr == nil {
				writeErr = f.Chmod(os.FileMode(h.Mode)&0777 | 0600)
			}
			if writeErr == nil {
				writeErr = f.Sync()
			}
			if err := errors.Join(writeErr, f.Close()); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// Only internal relative dylib links are part of the native payload.
			// Delay creation until all ordinary entries have been written, so an
			// archive can never write through a symlink it just introduced.
			target := path.Join(path.Dir(name), h.Linkname)
			if !strings.HasSuffix(name, ".dylib") || !strings.HasSuffix(h.Linkname, ".dylib") || path.IsAbs(h.Linkname) || !filepath.IsLocal(target) || strings.ContainsAny(h.Linkname, "\\\x00") {
				return invalidAsset("unsafe runtime symlink")
			}
			links[name] = h.Linkname
		default:
			return invalidAsset("unsupported runtime archive entry type")
		}
	}
	// Drain to validate the gzip trailer, not just the tar end marker.
	if _, err := io.Copy(io.Discard, &progressReader{ctx: ctx, r: gz}); err != nil {
		return runtimeDecodingError(ctx, err, "invalid runtime gzip trailer")
	}
	for name, target := range links {
		if err := r.Symlink(target, name); err != nil {
			return invalidAsset("conflicting runtime symlink")
		}
	}
	for name := range links {
		// os.Root resolves chains without permitting escape. Dangling links,
		// cycles, and links to directories are not usable runtime libraries.
		info, err := r.Stat(name)
		if err != nil || !info.Mode().IsRegular() {
			return invalidAsset("runtime symlink must resolve to an internal regular library")
		}
	}
	// Include implicit parents too, and fsync children before their parents.
	err = fs.WalkDir(r.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			dirs = append(dirs, name)
		}
		return err
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := syncDir(r, dirs[i]); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func runtimeDecodingError(ctx context.Context, err error, message string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return invalidAsset(message)
}

func archiveName(name string) (string, error) {
	if name == strings.TrimSuffix(archivePrefix, "/") {
		return ".", nil
	}
	if !strings.HasPrefix(name, archivePrefix) || strings.ContainsAny(name, "\\\x00") {
		return "", invalidAsset("runtime archive entry is outside the known archive prefix")
	}
	name = strings.TrimSuffix(strings.TrimPrefix(name, archivePrefix), "/")
	if name == "" {
		return ".", nil
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return "", invalidAsset("runtime archive entry contains traversal")
		}
	}
	if !filepath.IsLocal(name) {
		return "", invalidAsset("runtime archive entry is not relative")
	}
	return name, nil
}

func supportedPlatform(platform string) error {
	if platform != "darwin-arm64" {
		return &model.Error{Code: "unsupported_platform", Message: "bundled inference requires darwin/arm64; metadata operations remain available"}
	}
	return nil
}

// RuntimeArchiveDigest extracts a runtime archive into a private temporary
// directory exactly as installation would and returns its payload digest.
// Release tooling uses it to pin the tree that launch-time checks require.
func RuntimeArchiveDigest(ctx context.Context, archivePath string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	dir, err := os.MkdirTemp("", "recall-runtime-digest-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	r, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer r.Close()
	if err := extractRuntime(ctx, f, r); err != nil {
		return "", err
	}
	return runtimetree.Digest(ctx, r)
}
