// Package update finds, downloads and verifies newer Recall releases published
// on GitHub. It never installs anything itself: a verified package is handed to
// macOS Installer, which asks for authorization and re-checks the signature.
package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"recall/internal/model"
	"recall/internal/version"
)

const (
	Repository   = "moogie-labs/recall"
	LatestAPI    = "https://api.github.com/repos/" + Repository + "/releases/latest"
	ReleasesPage = "https://github.com/" + Repository + "/releases"
	// Where the release installer puts each copy (scripts/release-macos.py).
	CLIInstall = "/usr/local/lib/recall/recall"
	AppInstall = "/Applications/Recall.app"

	maxReleaseJSON = 1 << 20
	maxChecksum    = 4 << 10
	maxPackage     = 512 << 20
	checkTimeout   = 15 * time.Second
)

// DownloadHosts are the only hosts release assets are fetched from, including
// after redirects. GitHub redirects asset downloads to release-assets.
var DownloadHosts = []string{"github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com"}

type State string

const (
	Current     State = "current"
	Available   State = "available"
	Unsupported State = "unsupported" // development (unversioned) build
)

type Asset struct {
	Name string
	URL  string
	Size int64
}

type Release struct {
	Version  string // without "v"
	URL      string // GitHub release page
	Package  Asset
	Checksum Asset
}

type Check struct {
	State   State
	Current string
	Latest  string
	Release *Release // the latest release, when one was found
}

// Runner executes an absolute-path tool without a shell.
type Runner func(ctx context.Context, name string, args ...string) (stdout, stderr string, err error)

// Updater holds every external dependency so each step can be tested.
type Updater struct {
	Current  string                 // running version
	API      string                 // latest-release API URL
	Client   *http.Client           // base client; redirect policy is added
	Hosts    []string               // allowed https download hosts
	Run      Runner                 // codesign, pkgutil, spctl, open
	Location func() (string, error) // resolved path of the running copy
	Expected string                 // where the release installer puts it
	Cache    string                 // absolute parent of upgrade-* downloads
	Now      func() time.Time
}

// ForCLI upgrades the installed CLI (/usr/local/bin/recall is a symlink to it).
func ForCLI(current string) *Updater {
	return newUpdater(current, CLIInstall, func() (string, error) {
		executable, err := os.Executable()
		if err != nil {
			return "", err
		}
		return filepath.EvalSymlinks(executable)
	})
}

// ForApp upgrades the installed desktop app bundle.
func ForApp(current string) *Updater {
	return newUpdater(current, AppInstall, func() (string, error) {
		executable, err := os.Executable()
		if err != nil {
			return "", err
		}
		if executable, err = filepath.EvalSymlinks(executable); err != nil {
			return "", err
		}
		// Recall.app/Contents/MacOS/Recall -> Recall.app
		return filepath.Dir(filepath.Dir(filepath.Dir(executable))), nil
	})
}

func newUpdater(current, expected string, location func() (string, error)) *Updater {
	cache, err := os.UserCacheDir()
	if err == nil {
		cache = filepath.Join(cache, "recall")
	}
	return &Updater{Current: current, API: LatestAPI, Hosts: DownloadHosts, Run: execRunner, Location: location, Expected: expected, Cache: cache, Now: time.Now}
}

// PackageName is the release installer's asset name for version v.
func PackageName(v string) string { return "Recall-" + v + "-darwin-arm64.pkg" }

// Check compares the running version with the latest stable GitHub release.
// Development builds report Unsupported without contacting GitHub.
func (u *Updater) Check(ctx context.Context) (Check, error) {
	current, ok := version.Parse(u.Current)
	if !ok {
		return Check{State: Unsupported, Current: u.Current}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	release, err := u.latest(ctx)
	if err != nil {
		return Check{}, err
	}
	latest, _ := version.Parse(release.Version)
	check := Check{State: Current, Current: u.Current, Latest: release.Version, Release: release}
	if version.Compare(latest, current) > 0 {
		check.State = Available
	}
	return check, nil
}

type apiRelease struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

func (u *Updater) latest(ctx context.Context) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.API, nil)
	if err != nil {
		return nil, unavailable("the release API URL is invalid")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "recall/"+u.Current)
	resp, err := u.client().Do(req)
	if err != nil {
		return nil, unavailable("could not reach GitHub to check for updates")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, unavailable("no Recall release has been published yet")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, unavailable(fmt.Sprintf("GitHub returned HTTP %d while checking for updates", resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReleaseJSON+1))
	if err != nil {
		return nil, unavailable("could not read release information from GitHub")
	}
	if len(body) > maxReleaseJSON {
		return nil, unavailable("GitHub release information is too large")
	}
	var raw apiRelease
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, unavailable("GitHub returned invalid release information")
	}
	return parseRelease(raw)
}

func parseRelease(raw apiRelease) (*Release, error) {
	tag, hasV := strings.CutPrefix(raw.TagName, "v")
	v, valid := version.Parse(tag)
	if !hasV || !valid || !v.Stable() || raw.Draft || raw.Prerelease {
		return nil, unavailable("the latest GitHub release is not a stable Recall release")
	}
	page, err := url.Parse(raw.HTMLURL)
	if err != nil || page.Scheme != "https" || page.Host == "" {
		return nil, unavailable("the latest GitHub release has an invalid page URL")
	}
	release := &Release{Version: tag, URL: raw.HTMLURL}
	want := map[string]*Asset{PackageName(tag): &release.Package, PackageName(tag) + ".sha256": &release.Checksum}
	for _, a := range raw.Assets {
		if target, ok := want[a.Name]; ok {
			*target = Asset{Name: a.Name, URL: a.URL, Size: a.Size}
		}
	}
	if release.Package.URL == "" || release.Checksum.URL == "" {
		return nil, unavailable("Recall " + tag + " has no macOS installer on GitHub yet")
	}
	return release, nil
}

// client adds the redirect policy to the configured (or default) client.
func (u *Updater) client() *http.Client {
	c := http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second, DisableKeepAlives: true}}
	if u.Client != nil {
		c = *u.Client
	}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return u.allowed(req.URL, true)
	}
	return &c
}

// allowed accepts https URLs on the download hosts. Redirects may also stay on
// the release API's own host (GitHub redirects renamed repositories).
func (u *Updater) allowed(target *url.URL, redirect bool) error {
	hosts := u.Hosts
	if redirect {
		if api, err := url.Parse(u.API); err == nil {
			hosts = append(slices.Clone(hosts), api.Hostname())
		}
	}
	if target.Scheme != "https" || target.User != nil || !slices.Contains(hosts, target.Hostname()) {
		return unavailable("refusing to download from " + target.Host)
	}
	return nil
}

func execRunner(ctx context.Context, name string, args ...string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + os.Getenv("HOME"), "LC_ALL=en_US.UTF-8"}
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func unavailable(message string) error {
	return &model.Error{Code: "update_unavailable", Message: message}
}
func unsupported(message string) error {
	return &model.Error{Code: "upgrade_unsupported", Message: message}
}
func unverified(message string) error {
	return &model.Error{Code: "verification_failed", Message: message}
}
