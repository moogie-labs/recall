package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"recall/internal/model"
)

var teamIdentifier = regexp.MustCompile(`(?m)^TeamIdentifier=([A-Z0-9]{10})$`)

// Fetch confirms this copy may upgrade itself, downloads check's release into
// a private directory and verifies it. It returns the verified package path.
// Any failure removes the download.
func (u *Updater) Fetch(ctx context.Context, check Check, progress func(stage string, done, total int64)) (string, error) {
	if check.State != Available || check.Release == nil {
		return "", unsupported("no newer Recall release is available")
	}
	if progress == nil {
		progress = func(string, int64, int64) {}
	}
	team, err := u.eligible(ctx)
	if err != nil {
		return "", err
	}
	release := check.Release
	total := release.Package.Size
	if total <= 0 || total > maxPackage {
		return "", unavailable("the Recall installer on GitHub has an unexpected size")
	}
	if !filepath.IsAbs(u.Cache) {
		return "", unavailable("no cache directory is available for the download")
	}
	if err := os.MkdirAll(u.Cache, 0o700); err != nil {
		return "", err
	}
	u.removeStale()
	dir, err := os.MkdirTemp(u.Cache, "upgrade-")
	if err != nil {
		return "", err
	}
	verified := false
	defer func() {
		if !verified {
			os.RemoveAll(dir)
		}
	}()
	var sum bytes.Buffer
	if _, err := u.get(ctx, release.Checksum.URL, &sum, maxChecksum, nil); err != nil {
		return "", err
	}
	pkg := filepath.Join(dir, release.Package.Name)
	file, err := os.OpenFile(pkg, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	last := int64(-1)
	progress("downloading", 0, total)
	n, err := u.get(ctx, release.Package.URL, io.MultiWriter(file, hash), total, func(done int64) {
		if percent := done * 100 / total; percent != last {
			last = percent
			progress("downloading", done, total)
		}
	})
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if n != total {
		return "", unavailable("the Recall installer download is incomplete")
	}
	progress("verifying", total, total)
	if err := verifyChecksum(sum.Bytes(), release.Package.Name, hash.Sum(nil)); err != nil {
		return "", err
	}
	if err := u.verifyPackage(ctx, pkg, team); err != nil {
		return "", err
	}
	verified = true
	return pkg, nil
}

// Open hands a verified package to macOS Installer.
func (u *Updater) Open(ctx context.Context, pkg string) error {
	if _, _, err := u.Run(ctx, "/usr/bin/open", "-b", "com.apple.installer", pkg); err != nil {
		return &model.Error{Code: "runtime_unavailable", Message: "could not open the installer in macOS Installer"}
	}
	return nil
}

// eligible requires the release installer's copy, Developer ID signed; it
// returns that signature's team, which the new package must match.
func (u *Updater) eligible(ctx context.Context) (string, error) {
	where, err := u.Location()
	if err != nil {
		return "", unsupported("could not locate this copy of Recall")
	}
	if where != u.Expected {
		return "", unsupported("only the copy installed by the Recall installer at " + u.Expected + " can upgrade itself; download the installer from " + ReleasesPage)
	}
	_, stderr, err := u.Run(ctx, "/usr/bin/codesign", "-d", "--verbose=2", where)
	if err != nil {
		return "", unsupported("could not read this copy's code signature")
	}
	m := teamIdentifier.FindStringSubmatch(stderr)
	if m == nil {
		return "", unsupported("this copy of Recall is not Developer ID signed; download the installer from " + ReleasesPage)
	}
	return m[1], nil
}

// removeStale deletes downloads older than a day. Newer ones may still be open
// in Installer from a previous upgrade.
func (u *Updater) removeStale() {
	entries, err := os.ReadDir(u.Cache)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "upgrade-") {
			continue
		}
		if info, err := e.Info(); err == nil && u.Now().Sub(info.ModTime()) > 24*time.Hour {
			os.RemoveAll(filepath.Join(u.Cache, e.Name()))
		}
	}
}

// get downloads at most limit bytes of raw into w from an allowed host.
func (u *Updater) get(ctx context.Context, raw string, w io.Writer, limit int64, onRead func(int64)) (int64, error) {
	target, err := url.Parse(raw)
	if err != nil {
		return 0, unavailable("GitHub returned an invalid download URL")
	}
	if err := u.allowed(target, false); err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return 0, unavailable("GitHub returned an invalid download URL")
	}
	req.Header.Set("User-Agent", "recall/"+u.Current)
	resp, err := u.client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, unavailable("could not download the Recall installer from GitHub")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, unavailable(fmt.Sprintf("GitHub returned HTTP %d for %s", resp.StatusCode, filepath.Base(target.Path)))
	}
	n, err := io.Copy(w, &counter{r: io.LimitReader(resp.Body, limit+1), on: onRead})
	if err != nil {
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		return n, unavailable("the download from GitHub was interrupted")
	}
	if n > limit {
		return n, unavailable("the download from GitHub is larger than published")
	}
	return n, nil
}

type counter struct {
	r    io.Reader
	done int64
	on   func(int64)
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 && c.on != nil {
		c.done += int64(n)
		c.on(c.done)
	}
	return n, err
}
