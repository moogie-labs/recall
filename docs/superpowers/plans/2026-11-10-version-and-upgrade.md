# Version Reporting and Upgrades Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Recall reports its version (CLI and desktop) and upgrades itself from GitHub Releases by downloading, verifying and handing the signed installer to macOS Installer.

**Architecture:** A new `internal/version` package holds the ldflags-injected version and SemVer comparison. A new `internal/update` package finds the latest stable GitHub release, downloads it into a private cache directory, verifies checksum + Developer ID Installer signature (same team as the running copy) + notarization + Gatekeeper, and opens it in Installer.app. The CLI (`recall version`, `recall upgrade`) and the desktop app (launch check, pop-up, version footer, ⚙ Settings, quit-then-install handoff) are thin adapters over it.

**Tech Stack:** Go 1.27 (cobra CLI, Wails v2 desktop), React 19 + TypeScript + Vitest, Python 3 release scripts (unittest).

**Spec:** `docs/superpowers/specs/2026-11-10-version-and-upgrade-design.md`

## Global Constraints

- Platform: Apple Silicon, macOS 13+. No new Go or npm dependencies.
- Repository: `moogie-labs/recall`. Release API: `https://api.github.com/repos/moogie-labs/recall/releases/latest` (excludes drafts and prereleases). Only stable releases are offered.
- Assets: `Recall-<v>-darwin-arm64.pkg` and `Recall-<v>-darwin-arm64.pkg.sha256`; checksum file format is exactly `<64 lowercase hex>  <package name>\n`.
- Download hosts (https only, also after redirects): `github.com`, `objects.githubusercontent.com`, `release-assets.githubusercontent.com`.
- Limits: release JSON ≤ 1 MiB, checksum ≤ 4 KiB, package = API-reported size and ≤ 512 MiB, launch/API check timeout 15 s.
- Downloads go to a fresh `0700` `~/Library/Caches/recall/upgrade-*` directory (package file `0600`); `upgrade-*` directories older than 24 h are removed when a new download starts.
- Installer copy locations: CLI `/usr/local/lib/recall/recall`, app `/Applications/Recall.app`. Other copies cannot upgrade.
- External tools are invoked by absolute path without a shell: `/usr/bin/codesign`, `/usr/sbin/pkgutil`, `/usr/sbin/spctl`, `/usr/bin/open`.
- Recall never installs anything itself: verified packages go to `/usr/bin/open -b com.apple.installer <pkg>`.
- `User-Agent: recall/<version>`; never send user-identifying data.
- Development builds report version `dev`, never check GitHub, and cannot upgrade.
- New error codes (all exit 1 in the CLI): `update_unavailable`, `upgrade_unsupported`, `verification_failed`.
- Desktop: the launch pop-up appears on **every** launch while an upgrade is available and the launch check is on (default on). Launch-check failures are silent.
- Commit messages follow the repo's sentence-case imperative style and end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Desktop Go tests embed `desktop/frontend/dist`; run `sh scripts/build-desktop.sh` once before `(cd desktop && go test ./...)` in a clean checkout.

## File Structure

| File | Responsibility |
| --- | --- |
| `internal/version/version.go` (new) | `Version` variable, SemVer parse/compare, `Development()` |
| `internal/update/update.go` (new) | Constants, types, `Updater`, constructors, release lookup (`Check`), HTTP host policy, tool runner |
| `internal/update/fetch.go` (new) | Eligibility, download, stale cleanup, `Fetch`, `Open` |
| `internal/update/verify.go` (new) | Checksum and signature/notarization/Gatekeeper verification |
| `internal/update/live_test.go` (new) | Opt-in test against the real latest release |
| `internal/cli/upgrade.go` (new) | `version` and `upgrade` commands, `Upgrader` interface |
| `internal/cli/root.go` | Root `--version` flag, register commands, default updater |
| `internal/desktop/preferences.go` | Merge-preserving `desktop.json`, update-check preference |
| `internal/desktop/updates.go` (new) | `AppInfo`, `CheckForUpdate`, `SetCheckForUpdates`, `StartUpgrade` |
| `internal/desktop/service.go`, `bindings.go` | Service field, renderer allowlist |
| `desktop/host.go`, `desktop/main.go`, `desktop/dispatch.go` | Quit-then-open-Installer handoff, wiring, hidden `_version` |
| `desktop/frontend/src/updates/*` (new) | `useUpdates` hook, `UpgradePrompt`, `VersionFooter`, `SettingsDialog`, tests |
| `desktop/frontend/src/api/*`, `App.tsx`, `app.css` | Bridge/types/fake, integration, styles |
| `scripts/release-macos.py`, `scripts/test_release_macos.py` | Version injection and embedded-version check |
| `README.md`, `docs/cli.md`, `docs/desktop.md`, `docs/releases.md`, `docs/verification.md` | Documentation |

---

### Task 1: Version package

**Files:**
- Create: `internal/version/version.go`
- Test: `internal/version/version_test.go`

**Interfaces:**
- Produces: `var version.Version string` (default `"dev"`); `type version.SemVer struct{Major, Minor, Patch uint64; Prerelease []string}`; `func version.Parse(s string) (SemVer, bool)`; `func (SemVer) Stable() bool`; `func version.Compare(a, b SemVer) int`; `func version.Development() bool`.

- [ ] **Step 1: Write the failing test**

`internal/version/version_test.go`:

```go
package version

import "testing"

func TestParseAcceptsOnlyReleaseVersions(t *testing.T) {
	for _, valid := range []string{"0.1.0", "1.2.3", "10.20.30", "1.0.0-rc.1", "1.0.0-alpha", "1.0.0-0.3.7", "1.0.0-x-y.7"} {
		if _, ok := Parse(valid); !ok {
			t.Errorf("rejected %q", valid)
		}
	}
	for _, invalid := range []string{"", "dev", "v1.0.0", "1.0", "01.0.0", "1.00.0", "1.0.0-", "1.0.0-01", "1.0.0+build", "1.0.0-rc..1", "1.0.0 ", "99999999999999999999.0.0"} {
		if _, ok := Parse(invalid); ok {
			t.Errorf("accepted %q", invalid)
		}
	}
}

func TestCompareUsesSemVerPrecedence(t *testing.T) {
	// Each version is lower than the next (SemVer 2.0 §11 example plus extras).
	order := []string{"0.9.9", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0", "10.0.0"}
	for i := range order {
		for j := range order {
			a, _ := Parse(order[i])
			b, _ := Parse(order[j])
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := Compare(a, b); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", order[i], order[j], got, want)
			}
		}
	}
}

func TestStableAndDevelopment(t *testing.T) {
	if v, _ := Parse("1.0.0"); !v.Stable() {
		t.Error("1.0.0 not stable")
	}
	if v, _ := Parse("1.0.0-rc.1"); v.Stable() {
		t.Error("prerelease reported stable")
	}
	previous := Version
	t.Cleanup(func() { Version = previous })
	Version = "dev"
	if !Development() {
		t.Error("dev is not a development build")
	}
	Version = "0.3.0"
	if Development() {
		t.Error("release reported as development build")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/version/`
Expected: FAIL — build error, `undefined: Parse` (package has no non-test source yet).

- [ ] **Step 3: Write the implementation**

`internal/version/version.go`:

```go
// Package version reports the running Recall release.
package version

import (
	"regexp"
	"strconv"
	"strings"
)

// Version is the release version without its "v" prefix. Release builds set it
// with -ldflags "-X recall/internal/version.Version=<release>"; source and
// local builds report "dev".
var Version = "dev"

// The same grammar scripts/release-macos.py accepts for tags (after "v"):
// MAJOR.MINOR.PATCH, optional SemVer prerelease, no build metadata.
var grammar = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-((?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*))?$`)

// SemVer is a parsed release version.
type SemVer struct {
	Major, Minor, Patch uint64
	Prerelease          []string
}

// Parse accepts a release version without "v".
func Parse(s string) (SemVer, bool) {
	m := grammar.FindStringSubmatch(s)
	if m == nil {
		return SemVer{}, false
	}
	var v SemVer
	for i, target := range []*uint64{&v.Major, &v.Minor, &v.Patch} {
		n, err := strconv.ParseUint(m[i+1], 10, 64)
		if err != nil {
			return SemVer{}, false
		}
		*target = n
	}
	if m[4] != "" {
		v.Prerelease = strings.Split(m[4], ".")
	}
	return v, true
}

// Stable reports whether v has no prerelease identifiers.
func (v SemVer) Stable() bool { return len(v.Prerelease) == 0 }

// Development reports whether the running build is not a versioned release.
func Development() bool {
	_, ok := Parse(Version)
	return !ok
}

// Compare returns -1, 0 or 1 by SemVer 2.0 precedence.
func Compare(a, b SemVer) int {
	for _, pair := range [][2]uint64{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case a.Stable() && b.Stable():
		return 0
	case a.Stable():
		return 1
	case b.Stable():
		return -1
	}
	for i := 0; i < len(a.Prerelease) && i < len(b.Prerelease); i++ {
		if c := compareIdentifier(a.Prerelease[i], b.Prerelease[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.Prerelease) < len(b.Prerelease):
		return -1
	case len(a.Prerelease) > len(b.Prerelease):
		return 1
	}
	return 0
}

func numeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// Numeric identifiers have no leading zeros (enforced by grammar), so length
// then lexical order is numeric order without overflow.
func compareIdentifier(a, b string) int {
	an, bn := numeric(a), numeric(b)
	switch {
	case an && bn:
		if len(a) != len(b) {
			if len(a) < len(b) {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	case an:
		return -1
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/version/ && gofmt -l internal/version`
Expected: `ok  recall/internal/version`, no gofmt output.

- [ ] **Step 5: Commit**

```bash
git add internal/version
git commit -m "Add the release version and SemVer comparison

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Find the latest release

**Files:**
- Create: `internal/update/update.go`
- Test: `internal/update/update_test.go`

**Interfaces:**
- Consumes: `version.Parse`, `version.Compare`, `SemVer.Stable` (Task 1); `model.Error`.
- Produces (used by Tasks 3, 4, 6, 7):
  - `const update.Repository, LatestAPI, ReleasesPage, CLIInstall, AppInstall string`; `var update.DownloadHosts []string`
  - `type update.State string` with `update.Current`, `update.Available`, `update.Unsupported`
  - `type update.Asset struct{Name, URL string; Size int64}`; `type update.Release struct{Version, URL string; Package, Checksum Asset}`
  - `type update.Check struct{State State; Current, Latest string; Release *Release}`
  - `type update.Runner func(ctx context.Context, name string, args ...string) (stdout, stderr string, err error)`
  - `type update.Updater struct{Current, API string; Client *http.Client; Hosts []string; Run Runner; Location func() (string, error); Expected, Cache string; Now func() time.Time}`
  - `func update.ForCLI(current string) *Updater`, `func update.ForApp(current string) *Updater`
  - `func (*Updater) Check(ctx context.Context) (Check, error)`
  - `func update.PackageName(v string) string`
  - unexported: `(*Updater).client()`, `(*Updater).allowed(*url.URL, bool) error`, `unavailable/unsupported/unverified(string) error`

- [ ] **Step 1: Write the failing test**

`internal/update/update_test.go`:

```go
package update

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"recall/internal/model"
)

type fakeRelease struct {
	Tag        string
	Prerelease bool
	Assets     []string // asset names; nil means the package and its checksum
	Size       int64    // reported package size
}

func releaseBody(base string, r fakeRelease) []byte {
	names := r.Assets
	if names == nil {
		v := strings.TrimPrefix(r.Tag, "v")
		names = []string{PackageName(v), PackageName(v) + ".sha256"}
	}
	assets := []map[string]any{}
	for _, name := range names {
		size := int64(96)
		if strings.HasSuffix(name, ".pkg") {
			size = r.Size
		}
		assets = append(assets, map[string]any{"name": name, "size": size, "browser_download_url": base + "/download/" + name})
	}
	body, _ := json.Marshal(map[string]any{"tag_name": r.Tag, "html_url": "https://github.com/moogie-labs/recall/releases/tag/" + r.Tag, "draft": false, "prerelease": r.Prerelease, "assets": assets})
	return body
}

type apiHandler func(w http.ResponseWriter, base string)

func latest(r fakeRelease) apiHandler {
	return func(w http.ResponseWriter, base string) { w.Write(releaseBody(base, r)) }
}

func status(code int, body string) apiHandler {
	return func(w http.ResponseWriter, _ string) { w.WriteHeader(code); io.WriteString(w, body) }
}

// newFixture serves the release API at /latest and files at /download/<name>
// over TLS, and returns an Updater pointed at it plus a request counter.
func newFixture(t *testing.T, current string, api apiHandler, files map[string][]byte) (*Updater, *atomic.Int32) {
	t.Helper()
	requests := &atomic.Int32{}
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.Header.Get("User-Agent"); got != "recall/"+current {
			t.Errorf("User-Agent %q", got)
		}
		if r.URL.Path == "/latest" {
			if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
				t.Errorf("Accept %q", got)
			}
			api(w, srv.URL)
			return
		}
		name, ok := strings.CutPrefix(r.URL.Path, "/download/")
		data, exists := files[name]
		if !ok || !exists {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	}))
	t.Cleanup(srv.Close)
	u := &Updater{Current: current, API: srv.URL + "/latest", Client: srv.Client(), Hosts: []string{"127.0.0.1"}, Cache: t.TempDir(), Now: time.Now}
	return u, requests
}

func TestCheckComparesWithTheLatestStableRelease(t *testing.T) {
	for _, tc := range []struct {
		current, tag string
		want         State
	}{
		{"0.2.0", "v0.3.0", Available},
		{"0.3.0", "v0.3.0", Current},
		{"0.4.0", "v0.3.0", Current},
		{"0.3.0-rc.1", "v0.3.0", Available},
	} {
		u, _ := newFixture(t, tc.current, latest(fakeRelease{Tag: tc.tag, Size: 10}), nil)
		check, err := u.Check(t.Context())
		if err != nil || check.State != tc.want || check.Current != tc.current || check.Latest != strings.TrimPrefix(tc.tag, "v") {
			t.Fatalf("%+v: %+v %v", tc, check, err)
		}
		if check.Release.URL != "https://github.com/moogie-labs/recall/releases/tag/"+tc.tag || check.Release.Package.Name != PackageName(check.Latest) || check.Release.Package.Size != 10 || check.Release.Checksum.Name != PackageName(check.Latest)+".sha256" {
			t.Fatalf("%+v", check.Release)
		}
	}
}

func TestDevelopmentBuildsNeverContactGitHub(t *testing.T) {
	u, requests := newFixture(t, "dev", latest(fakeRelease{Tag: "v9.0.0"}), nil)
	check, err := u.Check(t.Context())
	if err != nil || check.State != Unsupported || check.Current != "dev" || requests.Load() != 0 {
		t.Fatal(check, err, requests.Load())
	}
}

func TestCheckRejectsUnusableReleases(t *testing.T) {
	huge := append([]byte(`{"tag_name":"v1.0.0","padding":"`), append(bytes.Repeat([]byte("a"), 1<<20), '"', '}')...)
	cases := map[string]apiHandler{
		"prerelease flag":   latest(fakeRelease{Tag: "v0.3.0", Prerelease: true}),
		"prerelease tag":    latest(fakeRelease{Tag: "v0.3.0-rc.1"}),
		"tag without v":     latest(fakeRelease{Tag: "0.3.0"}),
		"missing installer": latest(fakeRelease{Tag: "v0.3.0", Assets: []string{"Recall-0.3.0-darwin-arm64.pkg.sha256"}}),
		"missing checksum":  latest(fakeRelease{Tag: "v0.3.0", Assets: []string{"Recall-0.3.0-darwin-arm64.pkg"}}),
		"server error":      status(500, ""),
		"no releases":       status(404, `{"message":"Not Found"}`),
		"invalid JSON":      status(200, "{"),
		"oversized":         func(w http.ResponseWriter, _ string) { w.Write(huge) },
		"redirect elsewhere": func(w http.ResponseWriter, _ string) {
			w.Header().Set("Location", "https://example.invalid/latest")
			w.WriteHeader(http.StatusFound)
		},
	}
	for name, api := range cases {
		t.Run(name, func(t *testing.T) {
			u, _ := newFixture(t, "0.2.0", api, nil)
			if _, err := u.Check(t.Context()); model.ErrorCode(err) != "update_unavailable" {
				t.Fatal(err)
			}
		})
	}
}

func TestConstructorsTargetInstallerLocations(t *testing.T) {
	cli, app := ForCLI("0.2.0"), ForApp("0.2.0")
	if cli.Expected != CLIInstall || app.Expected != AppInstall || cli.API != LatestAPI || len(app.Hosts) != 3 || cli.Run == nil || app.Location == nil {
		t.Fatal(cli, app)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/update/`
Expected: FAIL — build errors such as `undefined: PackageName`, `undefined: Updater`.

- [ ] **Step 3: Write the implementation**

`internal/update/update.go`:

```go
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
	c := http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second}}
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

func unavailable(message string) error { return &model.Error{Code: "update_unavailable", Message: message} }
func unsupported(message string) error { return &model.Error{Code: "upgrade_unsupported", Message: message} }
func unverified(message string) error  { return &model.Error{Code: "verification_failed", Message: message} }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race ./internal/update/ && go vet ./internal/update/ && gofmt -l internal/update`
Expected: `ok  recall/internal/update`; no vet or gofmt output.

- [ ] **Step 5: Commit**

```bash
git add internal/update
git commit -m "Find the latest stable Recall release on GitHub

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Download, verify and open the installer

**Files:**
- Create: `internal/update/fetch.go`, `internal/update/verify.go`, `internal/update/live_test.go`
- Test: `internal/update/fetch_test.go`

**Interfaces:**
- Consumes: everything from Task 2 (`Updater`, `Check`, `Available`, `PackageName`, `allowed`, `client`, error helpers).
- Produces (used by Tasks 4, 6, 7):
  - `func (*Updater) Fetch(ctx context.Context, check Check, progress func(stage string, done, total int64)) (string, error)` — stages `"downloading"` then `"verifying"`; returns the verified package path.
  - `func (*Updater) Open(ctx context.Context, pkg string) error`

Verified real tool output (Recall v0.1.0, 2026-11-10) the parser must accept:

```
Package "Recall-0.1.0-darwin-arm64.pkg":
   Status: signed by a developer certificate issued by Apple for distribution
   Notarization: trusted by the Apple notary service
   Signed with a trusted timestamp on: 2026-11-10 06:29:13 +0000
   Certificate Chain:
    1. Developer ID Installer: Gregory Krause (SS25FU43FL)
```

`codesign -d --verbose=2 <signed copy>` prints `TeamIdentifier=SS25FU43FL` on stderr; ad-hoc/platform binaries print `TeamIdentifier=not set`. `spctl --assess --type install` exits 0 with `accepted` / `source=Notarized Developer ID`.

- [ ] **Step 1: Write the failing tests**

`internal/update/fetch_test.go`:

```go
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"recall/internal/model"
)

const signedPackage = `Package "Recall-0.3.0-darwin-arm64.pkg":
   Status: signed by a developer certificate issued by Apple for distribution
   Notarization: trusted by the Apple notary service
   Signed with a trusted timestamp on: 2026-11-10 06:29:13 +0000
   Certificate Chain:
    1. Developer ID Installer: Example Developer (ABCDE12345)
       Expires: 2027-02-01 22:12:15 +0000
       ------------------------------------------------------------------------
    2. Developer ID Certification Authority
    3. Apple Root CA
`

type fakeTools struct {
	calls                         []string
	team                          string
	pkgutil                       string
	pkgutilErr, spctlErr, openErr error
}

func (f *fakeTools) run(_ context.Context, name string, args ...string) (string, string, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	switch name {
	case "/usr/bin/codesign":
		return "", "Executable=/Applications/Recall.app/Contents/MacOS/Recall\nIdentifier=local.recall.desktop\nTeamIdentifier=" + f.team + "\n", nil
	case "/usr/sbin/pkgutil":
		return f.pkgutil, "", f.pkgutilErr
	case "/usr/sbin/spctl":
		return "", "accepted\nsource=Notarized Developer ID\n", f.spctlErr
	case "/usr/bin/open":
		return "", "", f.openErr
	}
	return "", "", fmt.Errorf("unexpected tool %s", name)
}

var installer = []byte("signed installer bytes")

func checksum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) + "  " + PackageName("0.3.0") + "\n"
}

// fetchFixture serves release 0.3.0 whose package is pkg (reported as size
// bytes) with checksum file sum, for an eligible, signed 0.2.0 app.
func fetchFixture(t *testing.T, pkg []byte, sum string, size int64) (*Updater, *fakeTools, Check, *atomic.Int32) {
	t.Helper()
	files := map[string][]byte{PackageName("0.3.0"): pkg, PackageName("0.3.0") + ".sha256": []byte(sum)}
	u, requests := newFixture(t, "0.2.0", latest(fakeRelease{Tag: "v0.3.0", Size: size}), files)
	tools := &fakeTools{team: "ABCDE12345", pkgutil: signedPackage}
	u.Run, u.Expected = tools.run, AppInstall
	u.Location = func() (string, error) { return AppInstall, nil }
	check, err := u.Check(t.Context())
	if err != nil || check.State != Available {
		t.Fatal(check, err)
	}
	return u, tools, check, requests
}

func TestFetchDownloadsAndVerifiesTheInstaller(t *testing.T) {
	u, tools, check, _ := fetchFixture(t, installer, checksum(installer), int64(len(installer)))
	var stages []string
	pkg, err := u.Fetch(t.Context(), check, func(stage string, done, total int64) {
		stages = append(stages, fmt.Sprintf("%s %d/%d", stage, done, total))
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(pkg)
	file, _ := os.Stat(pkg)
	dir, _ := os.Stat(filepath.Dir(pkg))
	if string(data) != string(installer) || file.Mode().Perm() != 0o600 || dir.Mode().Perm() != 0o700 || filepath.Dir(filepath.Dir(pkg)) != u.Cache || !strings.HasPrefix(filepath.Base(filepath.Dir(pkg)), "upgrade-") || filepath.Base(pkg) != PackageName("0.3.0") {
		t.Fatal(pkg, file.Mode(), dir.Mode())
	}
	n := len(installer)
	if stages[0] != fmt.Sprintf("downloading 0/%d", n) || !slices.Contains(stages, fmt.Sprintf("downloading %d/%d", n, n)) || stages[len(stages)-1] != fmt.Sprintf("verifying %d/%d", n, n) {
		t.Fatal(stages)
	}
	want := []string{
		"/usr/bin/codesign -d --verbose=2 " + AppInstall,
		"/usr/sbin/pkgutil --check-signature " + pkg,
		"/usr/sbin/spctl --assess --type install " + pkg,
	}
	if !slices.Equal(tools.calls, want) {
		t.Fatal(tools.calls)
	}
}

func TestFetchRejectsUnverifiedInstallers(t *testing.T) {
	tampered := []byte("tampered installer bytes")
	size := int64(len(installer))
	cases := []struct {
		name  string
		pkg   []byte
		sum   string
		size  int64
		tweak func(*fakeTools)
		code  string
	}{
		{"checksum mismatch", tampered, checksum(installer), int64(len(tampered)), nil, "verification_failed"},
		{"checksum names another file", installer, strings.Replace(checksum(installer), "0.3.0", "0.2.9", 1), size, nil, "verification_failed"},
		{"malformed checksum", installer, "not a checksum\n", size, nil, "verification_failed"},
		{"smaller than published", installer, checksum(installer), size + 5, nil, "update_unavailable"},
		{"larger than published", installer, checksum(installer), size - 5, nil, "update_unavailable"},
		{"unsigned", installer, checksum(installer), size, func(f *fakeTools) { f.pkgutilErr = errors.New("exit status 1") }, "verification_failed"},
		{"not notarized", installer, checksum(installer), size, func(f *fakeTools) {
			f.pkgutil = strings.Replace(signedPackage, "   Notarization: trusted by the Apple notary service\n", "", 1)
		}, "verification_failed"},
		{"another team", installer, checksum(installer), size, func(f *fakeTools) { f.pkgutil = strings.Replace(signedPackage, "ABCDE12345", "ZZZZZ99999", 1) }, "verification_failed"},
		{"application certificate", installer, checksum(installer), size, func(f *fakeTools) {
			f.pkgutil = strings.Replace(signedPackage, "Developer ID Installer", "Developer ID Application", 1)
		}, "verification_failed"},
		{"gatekeeper rejects", installer, checksum(installer), size, func(f *fakeTools) { f.spctlErr = errors.New("exit status 3") }, "verification_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, tools, check, _ := fetchFixture(t, tc.pkg, tc.sum, tc.size)
			if tc.tweak != nil {
				tc.tweak(tools)
			}
			pkg, err := u.Fetch(t.Context(), check, nil)
			if model.ErrorCode(err) != tc.code || pkg != "" {
				t.Fatal(pkg, err)
			}
			if entries, _ := os.ReadDir(u.Cache); len(entries) != 0 {
				t.Fatal("download kept after failure", entries)
			}
		})
	}
}

func TestOnlySignedInstallerCopiesMayUpgrade(t *testing.T) {
	for name, tweak := range map[string]func(*Updater, *fakeTools){
		"moved copy":       func(u *Updater, _ *fakeTools) { u.Location = func() (string, error) { return "/Users/me/Applications/Recall.app", nil } },
		"ad-hoc signature": func(_ *Updater, f *fakeTools) { f.team = "not set" },
	} {
		t.Run(name, func(t *testing.T) {
			u, tools, check, requests := fetchFixture(t, installer, checksum(installer), int64(len(installer)))
			tweak(u, tools)
			before := requests.Load()
			if _, err := u.Fetch(t.Context(), check, nil); model.ErrorCode(err) != "upgrade_unsupported" {
				t.Fatal(err)
			}
			if requests.Load() != before {
				t.Fatal("downloaded for an ineligible copy")
			}
		})
	}
	u, _, _, _ := fetchFixture(t, installer, checksum(installer), int64(len(installer)))
	if _, err := u.Fetch(t.Context(), Check{State: Current}, nil); model.ErrorCode(err) != "upgrade_unsupported" {
		t.Fatal("fetched without an available release", err)
	}
}

func TestDownloadsOnlyFromAllowedHosts(t *testing.T) {
	u, _, check, _ := fetchFixture(t, installer, checksum(installer), int64(len(installer)))
	u.Hosts = DownloadHosts // the fixture serves from 127.0.0.1
	if _, err := u.Fetch(t.Context(), check, nil); model.ErrorCode(err) != "update_unavailable" {
		t.Fatal(err)
	}
}

func TestFetchRemovesStaleDownloads(t *testing.T) {
	u, _, check, _ := fetchFixture(t, installer, checksum(installer), int64(len(installer)))
	stale, recent := filepath.Join(u.Cache, "upgrade-stale"), filepath.Join(u.Cache, "upgrade-recent")
	for _, dir := range []string{stale, recent} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(stale, old, old)
	if _, err := u.Fetch(t.Context(), check, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale download kept")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatal("recent download removed; Installer may still have it open")
	}
}

func TestOpenHandsThePackageToInstaller(t *testing.T) {
	tools := &fakeTools{}
	u := &Updater{Run: tools.run}
	if err := u.Open(t.Context(), "/cache/upgrade-1/a.pkg"); err != nil || !slices.Equal(tools.calls, []string{"/usr/bin/open -b com.apple.installer /cache/upgrade-1/a.pkg"}) {
		t.Fatal(err, tools.calls)
	}
	tools.openErr = errors.New("exit status 1")
	if err := u.Open(t.Context(), "/cache/upgrade-1/a.pkg"); err == nil {
		t.Fatal("Installer failure ignored")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/update/`
Expected: FAIL — build errors `u.Fetch undefined`, `u.Open undefined`.

- [ ] **Step 3: Write the download code**

`internal/update/fetch.go`:

```go
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
```

- [ ] **Step 4: Write the verification code**

`internal/update/verify.go`:

```go
package update

import (
	"bytes"
	"context"
	"encoding/hex"
	"regexp"
	"strings"
)

var (
	checksumLine  = regexp.MustCompile(`^([0-9a-f]{64})  (\S+)\n$`)
	installerLeaf = regexp.MustCompile(`^1\. Developer ID Installer: .+ \(([A-Z0-9]{10})\)$`)
)

const (
	distributionStatus = "Status: signed by a developer certificate issued by Apple for distribution"
	notarizedStatus    = "Notarization: trusted by the Apple notary service"
)

// verifyChecksum checks the published "<sha256>  <name>\n" line. It detects
// only corruption: the checksum comes from the same release as the package.
func verifyChecksum(file []byte, name string, got []byte) error {
	m := checksumLine.FindSubmatch(file)
	if m == nil || string(m[2]) != name {
		return unverified("the published installer checksum is malformed")
	}
	want, err := hex.DecodeString(string(m[1]))
	if err != nil || !bytes.Equal(want, got) {
		return unverified("the downloaded installer does not match its published checksum")
	}
	return nil
}

// verifyPackage establishes authenticity: a notarized Developer ID Installer
// signature from the running copy's team, accepted by Gatekeeper.
func (u *Updater) verifyPackage(ctx context.Context, pkg, team string) error {
	out, _, err := u.Run(ctx, "/usr/sbin/pkgutil", "--check-signature", pkg)
	if err != nil {
		return unverified("the downloaded installer is not validly signed")
	}
	distribution, notarized, leaf := false, false, ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == distributionStatus:
			distribution = true
		case line == notarizedStatus:
			notarized = true
		case leaf == "" && strings.HasPrefix(line, "1. "):
			leaf = "unexpected certificate"
			if m := installerLeaf.FindStringSubmatch(line); m != nil {
				leaf = m[1]
			}
		}
	}
	if !distribution || !notarized {
		return unverified("the downloaded installer is not signed and notarized for distribution")
	}
	if leaf != team {
		return unverified("the downloaded installer was not signed by the developer of this copy of Recall")
	}
	if _, _, err := u.Run(ctx, "/usr/sbin/spctl", "--assess", "--type", "install", pkg); err != nil {
		return unverified("Gatekeeper rejected the downloaded installer")
	}
	return nil
}
```

- [ ] **Step 5: Add the opt-in live test**

`internal/update/live_test.go`:

```go
package update

import (
	"context"
	"os"
	"testing"
)

// TestLiveLatestRelease downloads the real latest release from GitHub and
// verifies it with the real Apple tools. Only the running copy's signature
// is simulated (no installed copy is needed). Opt in with the Apple Team ID
// that signs releases:
//
//	RECALL_LIVE_UPDATE_TEAM=SS25FU43FL go test ./internal/update -run Live -v
func TestLiveLatestRelease(t *testing.T) {
	team := os.Getenv("RECALL_LIVE_UPDATE_TEAM")
	if team == "" {
		t.Skip("set RECALL_LIVE_UPDATE_TEAM to verify the real latest release")
	}
	u := ForApp("0.0.1-live") // lower than every release
	u.Cache = t.TempDir()
	u.Location = func() (string, error) { return AppInstall, nil }
	real := u.Run
	u.Run = func(ctx context.Context, name string, args ...string) (string, string, error) {
		if name == "/usr/bin/codesign" {
			return "", "TeamIdentifier=" + team + "\n", nil
		}
		return real(ctx, name, args...)
	}
	check, err := u.Check(t.Context())
	if err != nil || check.State != Available {
		t.Fatal(check, err)
	}
	pkg, err := u.Fetch(t.Context(), check, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("verified", check.Latest, pkg)
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test -race ./internal/update/ && go vet ./internal/update/ && gofmt -l internal/update`
Expected: `ok  recall/internal/update` (live test skipped); no vet or gofmt output.

- [ ] **Step 7: Run the live test against the real release**

Run: `RECALL_LIVE_UPDATE_TEAM=SS25FU43FL go test ./internal/update -run Live -v`
Expected: PASS, logging `verified 0.1.0 …/upgrade-…/Recall-0.1.0-darwin-arm64.pkg`.
Then run: `RECALL_LIVE_UPDATE_TEAM=ZZZZZZZZZZ go test ./internal/update -run Live -v`
Expected: FAIL with `the downloaded installer was not signed by the developer of this copy of Recall` (proves the team check rejects a mismatch with real `pkgutil` output).

- [ ] **Step 8: Commit**

```bash
git add internal/update
git commit -m "Download and verify signed Recall installers before handing them to Installer

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: `recall version` and `recall upgrade`

**Files:**
- Create: `internal/cli/upgrade.go`
- Modify: `internal/cli/root.go` (Runner struct ~line 29-37, `Run` defaults ~line 60-75, `rootCommand` ~line 118-140)
- Modify: `docs/cli.md`
- Test: `internal/cli/upgrade_test.go`

**Interfaces:**
- Consumes: `version.Version` (Task 1); `update.Check`, `update.Release`, `update.Available`, `update.Current`, `update.Unsupported`, `update.ReleasesPage`, `update.PackageName`, `update.ForCLI`, `(*Updater).Check/Fetch/Open` (Tasks 2-3).
- Produces: `type cli.Upgrader interface{Check; Fetch; Open}`; `Runner.updater func() Upgrader` (unexported, for tests); `type versionInfo struct{Version string}`; `type upgradeReport struct{Current, Latest string; UpdateAvailable bool; ReleaseURL, Package string; OpenedInstaller bool}` (JSON: `current`, `latest`, `update_available`, `release_url`, `package` (omitempty), `opened_installer`).

- [ ] **Step 1: Write the failing tests**

`internal/cli/upgrade_test.go`:

```go
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"recall/internal/app"
	"recall/internal/config"
	"recall/internal/model"
	"recall/internal/update"
	"recall/internal/version"
)

type fakeUpgrader struct {
	check                       update.Check
	checkErr, fetchErr, openErr error
	fetches                     int
	opened                      []string
}

func (f *fakeUpgrader) Check(context.Context) (update.Check, error) { return f.check, f.checkErr }
func (f *fakeUpgrader) Fetch(_ context.Context, c update.Check, progress func(string, int64, int64)) (string, error) {
	f.fetches++
	if f.fetchErr != nil {
		return "", f.fetchErr
	}
	for _, done := range []int64{0, 50, 100} {
		progress("downloading", done, 100)
	}
	progress("verifying", 100, 100)
	return "/cache/upgrade-1/" + update.PackageName(c.Latest), nil
}
func (f *fakeUpgrader) Open(_ context.Context, pkg string) error {
	f.opened = append(f.opened, pkg)
	return f.openErr
}

const releasePage = "https://github.com/moogie-labs/recall/releases/tag/v0.3.0"

func availableCheck() update.Check {
	return update.Check{State: update.Available, Current: "0.2.0", Latest: "0.3.0", Release: &update.Release{Version: "0.3.0", URL: releasePage}}
}

// runUpgrade runs the CLI with a fake updater; neither command may open a collection.
func runUpgrade(t *testing.T, f *fakeUpgrader, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("RECALL_HOME", "relative-invalid-collection")
	r := Runner{updater: func() Upgrader { return f }, open: func(context.Context, config.Config) (*app.App, error) {
		t.Fatal("opened a collection")
		return nil, nil
	}}
	var out, logs bytes.Buffer
	code := r.Run(t.Context(), args, strings.NewReader(""), &out, &logs)
	return code, out.String(), logs.String()
}

func TestVersionCommands(t *testing.T) {
	previous := version.Version
	version.Version = "1.2.3"
	t.Cleanup(func() { version.Version = previous })
	for _, args := range [][]string{{"version"}, {"--version"}} {
		code, out, logs := runUpgrade(t, &fakeUpgrader{}, args...)
		if code != 0 || out != "recall 1.2.3\n" || logs != "" {
			t.Fatalf("%v: %d %q %q", args, code, out, logs)
		}
	}
	for _, args := range [][]string{{"--json", "version"}, {"--json", "--version"}} {
		code, out, _ := runUpgrade(t, &fakeUpgrader{}, args...)
		var got struct {
			Data versionInfo `json:"data"`
		}
		if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got.Data.Version != "1.2.3" {
			t.Fatalf("%v: %d %s", args, code, out)
		}
	}
}

func TestUpgradeCheckOnlyReports(t *testing.T) {
	f := &fakeUpgrader{check: availableCheck()}
	code, out, _ := runUpgrade(t, f, "upgrade", "--check")
	want := "Recall 0.3.0 is available (installed: 0.2.0).\nRelease notes: " + releasePage + "\nRun recall upgrade to install it.\n"
	if code != 0 || out != want || f.fetches != 0 {
		t.Fatalf("%d %q", code, out)
	}
	code, out, _ = runUpgrade(t, f, "--json", "upgrade", "--check")
	var got struct {
		Data upgradeReport `json:"data"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got.Data != (upgradeReport{Current: "0.2.0", Latest: "0.3.0", UpdateAvailable: true, ReleaseURL: releasePage}) || f.fetches != 0 {
		t.Fatal(out)
	}
}

func TestUpgradeWhenAlreadyCurrent(t *testing.T) {
	f := &fakeUpgrader{check: update.Check{State: update.Current, Current: "0.3.0", Latest: "0.3.0", Release: &update.Release{URL: releasePage}}}
	for _, args := range [][]string{{"upgrade"}, {"upgrade", "--check"}} {
		code, out, _ := runUpgrade(t, f, args...)
		if code != 0 || out != "Recall 0.3.0 is up to date.\n" || f.fetches != 0 {
			t.Fatalf("%v %d %q", args, code, out)
		}
	}
}

func TestUpgradeDownloadsVerifiesAndOpensInstaller(t *testing.T) {
	f := &fakeUpgrader{check: availableCheck()}
	code, out, logs := runUpgrade(t, f, "upgrade")
	pkg := "/cache/upgrade-1/Recall-0.3.0-darwin-arm64.pkg"
	if code != 0 || out != "Opened Installer for Recall 0.3.0. Quit the Recall app before continuing.\n" || !slices.Equal(f.opened, []string{pkg}) {
		t.Fatalf("%d %q %v", code, out, f.opened)
	}
	if logs != "Downloading Recall 0.3.0: 0%\nDownloading Recall 0.3.0: 50%\nDownloading Recall 0.3.0: 100%\nVerifying installer signature…\n" {
		t.Fatalf("%q", logs)
	}
	code, out, _ = runUpgrade(t, &fakeUpgrader{check: availableCheck()}, "--json", "upgrade")
	var got struct {
		Data upgradeReport `json:"data"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || !got.Data.OpenedInstaller || got.Data.Package != pkg || !got.Data.UpdateAvailable {
		t.Fatal(out)
	}
}

func TestUpgradeFailures(t *testing.T) {
	cases := map[string]struct {
		f     *fakeUpgrader
		code  string
		opens int
	}{
		"development build": {&fakeUpgrader{check: update.Check{State: update.Unsupported, Current: "dev"}}, "upgrade_unsupported", 0},
		"offline":           {&fakeUpgrader{checkErr: &model.Error{Code: "update_unavailable", Message: "could not reach GitHub to check for updates"}}, "update_unavailable", 0},
		"verification":      {&fakeUpgrader{check: availableCheck(), fetchErr: &model.Error{Code: "verification_failed", Message: "Gatekeeper rejected the downloaded installer"}}, "verification_failed", 0},
		"installer":         {&fakeUpgrader{check: availableCheck(), openErr: &model.Error{Code: "runtime_unavailable", Message: "could not open the installer in macOS Installer"}}, "runtime_unavailable", 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			code, out, _ := runUpgrade(t, tc.f, "--json", "upgrade")
			var got result
			if code != 1 || json.Unmarshal([]byte(out), &got) != nil || got.Error == nil || got.Error.Code != tc.code || len(tc.f.opened) != tc.opens {
				t.Fatalf("%d %s", code, out)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/ -run 'Version|Upgrade'`
Expected: FAIL — build errors `unknown field updater in struct literal of type Runner`, `undefined: Upgrader`, `undefined: versionInfo`.

- [ ] **Step 3: Add the commands**

`internal/cli/upgrade.go`:

```go
package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"recall/internal/model"
	"recall/internal/update"
	"recall/internal/version"
)

// Upgrader is the update workflow; *update.Updater implements it.
type Upgrader interface {
	Check(context.Context) (update.Check, error)
	Fetch(context.Context, update.Check, func(stage string, done, total int64)) (string, error)
	Open(context.Context, string) error
}

type versionInfo struct {
	Version string `json:"version"`
}

func (i *invocation) versionResult() any {
	if i.json {
		return versionInfo{version.Version}
	}
	return "recall " + version.Version + "\n"
}

func (i *invocation) versionCommand() *cobra.Command {
	return i.command("version", "Print the Recall version", noArgs, func(*cobra.Command, []string) (any, error) {
		return i.versionResult(), nil
	})
}

type upgradeReport struct {
	Current         string `json:"current"`
	Latest          string `json:"latest"`
	UpdateAvailable bool   `json:"update_available"`
	ReleaseURL      string `json:"release_url"`
	Package         string `json:"package,omitempty"`
	OpenedInstaller bool   `json:"opened_installer"`
}

func (i *invocation) upgradeCommand() *cobra.Command {
	var checkOnly bool
	c := i.command("upgrade", "Download, verify, and open the newest Recall installer", noArgs, func(*cobra.Command, []string) (any, error) {
		u := i.runner.updater()
		check, err := u.Check(i.ctx)
		if err != nil {
			return nil, err
		}
		if check.State == update.Unsupported {
			return nil, &model.Error{Code: "upgrade_unsupported", Message: "this is a development build of Recall; install releases from " + update.ReleasesPage}
		}
		report := upgradeReport{Current: check.Current, Latest: check.Latest, UpdateAvailable: check.State == update.Available}
		if check.Release != nil {
			report.ReleaseURL = check.Release.URL
		}
		if !report.UpdateAvailable {
			return i.upgradeResult(report, "Recall "+check.Current+" is up to date.\n"), nil
		}
		if checkOnly {
			return i.upgradeResult(report, fmt.Sprintf("Recall %s is available (installed: %s).\nRelease notes: %s\nRun recall upgrade to install it.\n", check.Latest, check.Current, report.ReleaseURL)), nil
		}
		last := -10
		pkg, err := u.Fetch(i.ctx, check, func(stage string, done, total int64) {
			if stage == "verifying" {
				fmt.Fprintln(i.stderr, "Verifying installer signature…")
				return
			}
			if total <= 0 {
				return
			}
			percent := int(done * 100 / total)
			if percent >= last+10 || percent == 100 && last != 100 {
				fmt.Fprintf(i.stderr, "Downloading Recall %s: %d%%\n", check.Latest, percent)
				last = percent
			}
		})
		if err != nil {
			return nil, err
		}
		if err := u.Open(i.ctx, pkg); err != nil {
			return nil, err
		}
		report.Package, report.OpenedInstaller = pkg, true
		return i.upgradeResult(report, "Opened Installer for Recall "+check.Latest+". Quit the Recall app before continuing.\n"), nil
	})
	c.Flags().BoolVar(&checkOnly, "check", false, "Only report whether a newer release is available")
	return c
}

func (i *invocation) upgradeResult(report upgradeReport, text string) any {
	if i.json {
		return report
	}
	return text
}
```

- [ ] **Step 4: Wire the root command**

In `internal/cli/root.go`:

1. Add the import `"recall/internal/update"` and `"recall/internal/version"`.
2. Add a field to `Runner` (after `executable`):

```go
	updater    func() Upgrader
```

3. In `Run`, after the `if r.executable == nil { ... }` block, add:

```go
	if r.updater == nil {
		r.updater = func() Upgrader { return update.ForCLI(version.Version) }
	}
```

4. In `rootCommand`, replace

```go
	c := i.group("recall", "Local notes, tasks, and offline search")
```

with

```go
	var showVersion bool
	c := i.command("recall", "Local notes, tasks, and offline search", noArgs, func(c *cobra.Command, _ []string) (any, error) {
		if showVersion {
			return i.versionResult(), nil
		}
		if err := c.Help(); err != nil {
			return nil, err
		}
		return i.data, nil
	})
	c.Flags().BoolVar(&showVersion, "version", false, "Print the Recall version")
```

5. Extend the first `c.AddCommand(...)` call with `i.versionCommand(), i.upgradeCommand()` after `i.searchCommand()`.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/cli/ && go vet ./internal/cli/ && gofmt -l internal/cli`
Expected: `ok  recall/internal/cli` (including the existing `TestHelpAndRootDoNotOpenCollection`); no vet or gofmt output.

- [ ] **Step 6: Document the commands**

In `docs/cli.md`, add this section after the `## Tasks and search` section (before the next `##` heading):

````markdown
## Version and upgrades

```sh
recall version            # or: recall --version
recall upgrade --check    # report only; downloads nothing
recall upgrade
```

`version` and `upgrade` never open a collection. `upgrade` asks the GitHub API
for the latest **stable** release of `moogie-labs/recall` (prereleases are never
offered). When it is newer, it downloads `Recall-<v>-darwin-arm64.pkg` and its
`.sha256` into a private `~/Library/Caches/recall/upgrade-*` directory, then
requires all of: a matching checksum, a notarized **Developer ID Installer**
signature from the same Apple team as the running copy, and Gatekeeper's install
assessment. It then opens the package in macOS Installer, which asks for
administrator authorization; Recall never installs anything itself and has no
confirmation prompt of its own. Progress goes to stderr; `--json` reports
`current`, `latest`, `update_available`, `release_url`, and after an upgrade
`package` and `opened_installer`.

Only the copy installed by the release installer
(`/usr/local/bin/recall` → `/usr/local/lib/recall/recall`) can upgrade itself.
Development builds report version `dev` and fail with `upgrade_unsupported`.
Other errors are `update_unavailable` (network or release problems) and
`verification_failed`; all exit 1.
````

- [ ] **Step 7: Commit**

```bash
git add internal/cli docs/cli.md
git commit -m "Add recall version and recall upgrade

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Embed and check the version in release builds

**Files:**
- Modify: `scripts/release-macos.py` (`build_stage`, ~line 498-540; add two helpers after `version()` ~line 80)
- Modify: `desktop/dispatch.go`
- Modify: `docs/releases.md`
- Test: `scripts/test_release_macos.py`, `desktop/dispatch_test.go`

**Interfaces:**
- Consumes: `version.Version` (Task 1); the CLI `version` command (Task 4).
- Produces: `release_macos.version_ldflags(release) -> str`; `release_macos.require_embedded_version(cli, app, release)`; desktop binary handles a hidden `_version` argument printing `version.Version + "\n"`.

- [ ] **Step 1: Write the failing tests**

Append to `class ReleaseTests` in `scripts/test_release_macos.py`:

```python
    def test_release_binaries_must_report_the_tag_version(self):
        self.assertEqual(self.release.version_ldflags("0.3.0-rc.1"), "-X recall/internal/version.Version=0.3.0-rc.1")
        cli, app = self.root / "recall", self.root / "Recall.app"
        binary = app / "Contents/MacOS/Recall"
        binary.parent.mkdir(parents=True)

        def tool(path, output):
            path.write_text(f"#!{sys.executable}\nprint({output!r})\n")
            path.chmod(0o755)

        tool(cli, "recall 0.3.0")
        tool(binary, "0.3.0")
        self.release.require_embedded_version(cli, app, "0.3.0")
        for stale_cli, stale_app in [("recall dev", "0.3.0"), ("recall 0.3.0", "dev")]:
            tool(cli, stale_cli)
            tool(binary, stale_app)
            with self.subTest(cli=stale_cli, app=stale_app), self.assertRaisesRegex(ValueError, "version"):
                self.release.require_embedded_version(cli, app, "0.3.0")

    def test_build_stage_injects_and_checks_the_version(self):
        import inspect
        source = inspect.getsource(self.release.build_stage)
        self.assertIn('"-ldflags", ldflags, "-o", cli', source)
        self.assertIn('"scripts/build-desktop.sh", "-ldflags", ldflags', source)
        self.assertIn('require_embedded_version(cli, staged / "Recall.app", release)', source)
```

Append to `desktop/dispatch_test.go` (add imports `"recall/internal/version"`):

```go
func TestVersionDispatchBeforeGUI(t *testing.T) {
	previous := version.Version
	version.Version = "1.2.3"
	t.Cleanup(func() { version.Version = previous })
	var out bytes.Buffer
	handled, code := dispatch([]string{"_version"}, nil, &out, io.Discard, func([]string, io.Reader, io.Writer, io.Writer) int {
		t.Fatal("version entered the worker")
		return 0
	})
	if !handled || code != 0 || out.String() != "1.2.3\n" {
		t.Fatalf("%v %d %q", handled, code, out.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `python3 -m unittest discover -s scripts -p 'test_release_macos.py' -k version`
Expected: FAIL — `AttributeError: module 'release_macos' has no attribute 'version_ldflags'`.

Run: `sh scripts/build-desktop.sh && (cd desktop && go test -run VersionDispatch ./...)`
Expected: FAIL — `handled` false / empty output.

- [ ] **Step 3: Implement the release helpers**

In `scripts/release-macos.py`, after the `version(tag)` function add:

```python
def version_ldflags(release):
    # Both binaries report this through recall/internal/version.
    return "-X recall/internal/version.Version=" + release


def require_embedded_version(cli, app, release):
    dist.require(run([cli, "version"]) == f"recall {release}\n", "CLI does not report the release version")
    dist.require(run([app / "Contents/MacOS/Recall", "_version"]) == release + "\n",
                 "desktop app does not report the release version")
```

In `build_stage`, replace the CLI build line

```python
        run(["go", "build", "-trimpath", "-o", cli, "./cmd/recall"], cwd=source, env=env)
```

with

```python
        ldflags = version_ldflags(release)
        run(["go", "build", "-trimpath", "-ldflags", ldflags, "-o", cli, "./cmd/recall"], cwd=source, env=env)
```

replace

```python
        run(["sh", "scripts/build-desktop.sh"], cwd=source, env=env)
```

with

```python
        run(["sh", "scripts/build-desktop.sh", "-ldflags", ldflags], cwd=source, env=env)
```

(`build-desktop.sh` forwards its arguments to `wails build`), and after `run([cli, "--help"])` add:

```python
        require_embedded_version(cli, staged / "Recall.app", release)
```

- [ ] **Step 4: Implement `_version` dispatch**

Replace `desktop/dispatch.go` with:

```go
package main

import (
	"fmt"
	"io"

	"recall/internal/version"
)

func dispatch(args []string, stdin io.Reader, stdout, stderr io.Writer, runWorker func([]string, io.Reader, io.Writer, io.Writer) int) (bool, int) {
	if len(args) == 1 && args[0] == "_version" {
		// Lets the release build check the app binary's embedded version.
		fmt.Fprintln(stdout, version.Version)
		return true, 0
	}
	if len(args) > 0 && args[0] == "_embedding-worker" {
		return true, runWorker(args, stdin, stdout, stderr)
	}
	return false, 0
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `python3 -m unittest discover -s scripts -p 'test_release_macos.py'`
Expected: `OK` (all tests).

Run: `(cd desktop && go test ./... && go vet ./...)`
Expected: `ok  recall/desktop`.

- [ ] **Step 6: Check a local versioned build end to end**

Run:

```bash
go build -ldflags "-X recall/internal/version.Version=0.0.9" -o build/recall ./cmd/recall
./build/recall version
sh scripts/build-desktop.sh -ldflags "-X recall/internal/version.Version=0.0.9"
build/Recall.app/Contents/MacOS/Recall _version
```

Expected: `recall 0.0.9` and `0.0.9`.

- [ ] **Step 7: Document the release change**

In `docs/releases.md`, after the paragraph that ends `…Bump that line for Go security releases. Third-party notice files are pinned by SHA-256 like the runtime.` add:

```markdown
The `build` job embeds the release version in both binaries with
`-ldflags "-X recall/internal/version.Version=<release>"` and fails unless
`recall version` prints `recall <release>` and the app binary's hidden
`_version` argument prints `<release>`. Source and local builds report `dev`.

In-app and `recall upgrade` upgrades accept a downloaded package only if its
Developer ID Installer signature is notarized and comes from the **same Apple
team** as the running copy (plus checksum and Gatekeeper checks). Renewing
certificates under the same team keeps upgrades working; moving releases to a
different team means users must install the next release manually once. Recall
0.1.0 predates upgrade support, so its users install the next release manually.
```

In the `## Publish a version` section, replace the sentence
`Quit Recall before installing/upgrading, then run \`recall init\` as your user after each upgrade to verify/install that release's runtime.`
with
`Upgrading from the app or with \`recall upgrade\` quits the app before Installer continues; when installing manually, quit Recall first. Opening a collection in the app installs that release's runtime; from the terminal, run \`recall init\` as your user after each upgrade.`

- [ ] **Step 8: Commit**

```bash
git add scripts/release-macos.py scripts/test_release_macos.py desktop/dispatch.go desktop/dispatch_test.go docs/releases.md
git commit -m "Embed the release version in both binaries and check it before packaging

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Desktop update service and preference

**Files:**
- Modify: `internal/desktop/preferences.go` (whole file)
- Create: `internal/desktop/updates.go`
- Modify: `internal/desktop/service.go` (struct field + import), `internal/desktop/bindings.go`
- Test: `internal/desktop/updates_test.go`

**Interfaces:**
- Consumes: `version.Parse` (Task 1); `update.Check`, `update.Release`, `update.Available`, `update.Current`, `update.Unsupported`, `update.PackageName` (Task 2).
- Produces (used by Tasks 7, 8):
  - `func (FilePreferences) LoadUpdateCheck() (bool, error)` (missing/unreadable ⇒ `true`), `SaveUpdateCheck(bool) error`; `SaveRoot`/`SaveUpdateCheck` preserve other keys.
  - `type desktop.Updater interface{Check(context.Context) (update.Check, error); Fetch(context.Context, update.Check, func(string, int64, int64)) (string, error)}`
  - `type desktop.UpdatePreferences interface{LoadUpdateCheck() (bool, error); SaveUpdateCheck(bool) error}`
  - `type AppInfo struct{Version string "version"; Development bool "development"; CheckForUpdates bool "checkForUpdates"}`
  - `type UpdateReply struct{State, Current, Latest, ReleaseURL string; Problem *Problem}` — JSON `state`, `current`, `latest`, `releaseURL`, `problem`; `state` ∈ `current|available|unsupported|disabled|failed`.
  - `type UpgradeProgress struct{Stage string "stage"; Done int64 "done"; Total int64 "total"}`, emitted as `desktop:upgrade-progress`.
  - `func (*Service) SetUpdater(u Updater, current string, handoff func(pkg string) error)`
  - `func (*Service) AppInfo() AppInfo`, `SetCheckForUpdates(bool) OperationReply`, `CheckForUpdate(manual bool) UpdateReply`, `StartUpgrade() OperationReply`
  - API bindings `AppInfo`, `SetCheckForUpdates`, `CheckForUpdate`, `StartUpgrade`.

- [ ] **Step 1: Write the failing tests**

`internal/desktop/updates_test.go`:

```go
package desktop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"recall/internal/model"
	"recall/internal/update"
)

type fakeUpdater struct {
	mu                 sync.Mutex
	check              update.Check
	checkErr, fetchErr error
	checks, fetches    int
	block              chan struct{}
}

func (f *fakeUpdater) Check(context.Context) (update.Check, error) {
	f.mu.Lock()
	f.checks++
	f.mu.Unlock()
	return f.check, f.checkErr
}

func (f *fakeUpdater) Fetch(_ context.Context, c update.Check, progress func(string, int64, int64)) (string, error) {
	f.mu.Lock()
	f.fetches++
	f.mu.Unlock()
	if f.block != nil {
		<-f.block
	}
	if f.fetchErr != nil {
		return "", f.fetchErr
	}
	progress("downloading", 50, 100)
	progress("verifying", 100, 100)
	return "/cache/upgrade-1/" + update.PackageName(c.Latest), nil
}

func (f *fakeUpdater) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks, f.fetches
}

const releasePage = "https://github.com/moogie-labs/recall/releases/tag/v0.3.0"

func availableCheck() update.Check {
	return update.Check{State: update.Available, Current: "0.2.0", Latest: "0.3.0", Release: &update.Release{Version: "0.3.0", URL: releasePage}}
}

func updateService(t *testing.T, current string, u Updater, handoff func(string) error) (*Service, FilePreferences) {
	t.Helper()
	prefs := FilePreferences{Directory: t.TempDir()}
	s := NewService(nil, prefs)
	s.SetUpdater(u, current, handoff)
	return s, prefs
}

func TestAppInfoAndUpdatePreference(t *testing.T) {
	s, prefs := updateService(t, "0.2.0", &fakeUpdater{}, nil)
	if got := s.AppInfo(); got != (AppInfo{Version: "0.2.0", CheckForUpdates: true}) {
		t.Fatal(got)
	}
	if r := s.SetCheckForUpdates(false); r.Problem != nil {
		t.Fatal(r.Problem)
	}
	if got := s.AppInfo(); got.CheckForUpdates {
		t.Fatal(got)
	}
	if enabled, err := prefs.LoadUpdateCheck(); err != nil || enabled {
		t.Fatal(enabled, err)
	}
	dev, _ := updateService(t, "dev", &fakeUpdater{}, nil)
	if got := dev.AppInfo(); !got.Development || got.Version != "dev" {
		t.Fatal(got)
	}
}

func TestPreferencesKeepEachOther(t *testing.T) {
	prefs := FilePreferences{Directory: t.TempDir()}
	file := filepath.Join(prefs.Directory, "desktop.json")
	root := filepath.Join(t.TempDir(), "collection")
	if err := os.WriteFile(file, []byte(`{"future":{"kept":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prefs.SaveUpdateCheck(false); err != nil {
		t.Fatal(err)
	}
	if err := prefs.SaveRoot(root); err != nil {
		t.Fatal(err)
	}
	if enabled, _ := prefs.LoadUpdateCheck(); enabled {
		t.Fatal("SaveRoot erased the update preference")
	}
	if err := prefs.SaveUpdateCheck(true); err != nil {
		t.Fatal(err)
	}
	if loaded, err := prefs.LoadRoot(); err != nil || loaded != root {
		t.Fatal("SaveUpdateCheck erased the root", loaded, err)
	}
	data, _ := os.ReadFile(file)
	info, _ := os.Stat(file)
	if !strings.Contains(string(data), `"future":{"kept":true}`) || info.Mode().Perm() != 0o600 {
		t.Fatal(string(data), info.Mode())
	}
}

func TestCorruptPreferencesCanBeReplaced(t *testing.T) {
	prefs := FilePreferences{Directory: t.TempDir()}
	os.WriteFile(filepath.Join(prefs.Directory, "desktop.json"), []byte("not JSON"), 0o600)
	if _, err := prefs.LoadRoot(); err == nil {
		t.Fatal("invalid prefs silently ignored")
	}
	if enabled, err := prefs.LoadUpdateCheck(); err == nil || !enabled {
		t.Fatal("unreadable prefs must keep the default", enabled, err)
	}
	root := filepath.Join(t.TempDir(), "collection")
	if err := prefs.SaveRoot(root); err != nil {
		t.Fatal(err)
	}
	if loaded, err := prefs.LoadRoot(); err != nil || loaded != root {
		t.Fatal(loaded, err)
	}
}

func TestLaunchCheckRespectsPreferenceAndDevelopmentBuilds(t *testing.T) {
	u := &fakeUpdater{check: availableCheck()}
	s, _ := updateService(t, "0.2.0", u, nil)
	if got := s.CheckForUpdate(false); got != (UpdateReply{State: "available", Current: "0.2.0", Latest: "0.3.0", ReleaseURL: releasePage}) {
		t.Fatal(got)
	}
	s.SetCheckForUpdates(false)
	if got := s.CheckForUpdate(false); got.State != "disabled" {
		t.Fatal(got)
	}
	if checks, _ := u.counts(); checks != 1 {
		t.Fatal("disabled launch check contacted GitHub")
	}
	if got := s.CheckForUpdate(true); got.State != "available" {
		t.Fatal("manual check ignored", got)
	}
	dev, _ := updateService(t, "dev", u, nil)
	if got := dev.CheckForUpdate(true); got.State != "unsupported" || got.Current != "dev" {
		t.Fatal(got)
	}
	if checks, _ := u.counts(); checks != 2 {
		t.Fatal("development build contacted GitHub")
	}
	failing, _ := updateService(t, "0.2.0", &fakeUpdater{checkErr: &model.Error{Code: "update_unavailable", Message: "could not reach GitHub to check for updates"}}, nil)
	if got := failing.CheckForUpdate(true); got.State != "failed" || got.Problem == nil || got.Problem.Message != "could not reach GitHub to check for updates" {
		t.Fatal(got)
	}
}

func TestStartUpgradeReportsProgressAndHandsOff(t *testing.T) {
	var handed []string
	s, _ := updateService(t, "0.2.0", &fakeUpdater{check: availableCheck()}, func(pkg string) error {
		handed = append(handed, pkg)
		return nil
	})
	var events []UpgradeProgress
	s.SetEventSink(func(name string, payload any) {
		if name == "desktop:upgrade-progress" {
			events = append(events, payload.(UpgradeProgress))
		}
	})
	if r := s.StartUpgrade(); r.Problem != nil {
		t.Fatal(r.Problem)
	}
	if !slices.Equal(handed, []string{"/cache/upgrade-1/Recall-0.3.0-darwin-arm64.pkg"}) {
		t.Fatal(handed)
	}
	if len(events) != 2 || events[0] != (UpgradeProgress{Stage: "downloading", Done: 50, Total: 100}) || events[1].Stage != "verifying" {
		t.Fatal(events)
	}
}

func TestStartUpgradeFailures(t *testing.T) {
	handoff := func(string) error { t.Fatal("handed off a failed upgrade"); return nil }
	for name, tc := range map[string]struct {
		current string
		u       *fakeUpdater
		code    string
	}{
		"development":  {"dev", &fakeUpdater{check: availableCheck()}, "upgrade_unsupported"},
		"up to date":   {"0.3.0", &fakeUpdater{check: update.Check{State: update.Current, Current: "0.3.0", Latest: "0.3.0"}}, "conflict"},
		"offline":      {"0.2.0", &fakeUpdater{checkErr: &model.Error{Code: "update_unavailable", Message: "could not reach GitHub to check for updates"}}, "update_unavailable"},
		"verification": {"0.2.0", &fakeUpdater{check: availableCheck(), fetchErr: &model.Error{Code: "verification_failed", Message: "Gatekeeper rejected the downloaded installer"}}, "verification_failed"},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := updateService(t, tc.current, tc.u, handoff)
			if r := s.StartUpgrade(); r.Problem == nil || r.Problem.Code != tc.code {
				t.Fatal(r.Problem)
			}
		})
	}
	s, _ := updateService(t, "0.2.0", &fakeUpdater{check: availableCheck()}, func(string) error { return errors.New("not ready") })
	if r := s.StartUpgrade(); r.Problem == nil || r.Problem.Message != "Could not quit Recall to install the upgrade" {
		t.Fatal(r.Problem)
	}
}

func TestOnlyOneUpgradeRunsAtATime(t *testing.T) {
	u := &fakeUpdater{check: availableCheck(), block: make(chan struct{})}
	s, _ := updateService(t, "0.2.0", u, func(string) error { return nil })
	done := make(chan OperationReply)
	go func() { done <- s.StartUpgrade() }()
	for {
		if _, fetches := u.counts(); fetches == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if r := s.StartUpgrade(); r.Problem == nil || r.Problem.Code != "conflict" {
		t.Fatal(r.Problem)
	}
	close(u.block)
	if r := <-done; r.Problem != nil {
		t.Fatal(r.Problem)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/desktop/ -run 'AppInfo|Preferences|LaunchCheck|Upgrade'`
Expected: FAIL — build errors `s.SetUpdater undefined`, `prefs.LoadUpdateCheck undefined`.

- [ ] **Step 3: Make preferences merge-preserving**

Replace `internal/desktop/preferences.go` with:

```go
package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// FilePreferences stores desktop settings in <Directory>/desktop.json. Writes
// preserve keys they do not own, so the collection root and the update-check
// setting (and settings from other app versions) never erase each other.
type FilePreferences struct{ Directory string }

func DefaultPreferences() (FilePreferences, error) {
	dir, err := os.UserConfigDir()
	return FilePreferences{Directory: filepath.Join(dir, "recall")}, err
}

func (p FilePreferences) load() (map[string]json.RawMessage, error) {
	file := filepath.Join(p.Directory, "desktop.json")
	info, err := os.Lstat(file)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("desktop preferences must be a regular file")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	values := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("invalid desktop preferences: %w", err)
	}
	if values == nil {
		values = map[string]json.RawMessage{}
	}
	return values, nil
}

func (p FilePreferences) LoadRoot() (string, error) {
	values, err := p.load()
	if err != nil {
		return "", err
	}
	var root string
	if raw, ok := values["root"]; ok {
		if err := json.Unmarshal(raw, &root); err != nil {
			return "", fmt.Errorf("invalid desktop preferences: %w", err)
		}
	}
	if root != "" && !filepath.IsAbs(root) {
		return "", fmt.Errorf("saved collection root must be absolute")
	}
	return root, nil
}

func (p FilePreferences) SaveRoot(root string) error {
	if !filepath.IsAbs(root) {
		return fmt.Errorf("collection root must be absolute")
	}
	return p.save("root", root)
}

// LoadUpdateCheck reports whether to check for updates at launch. A missing
// setting means true; an unreadable file also returns true, with its error.
func (p FilePreferences) LoadUpdateCheck() (bool, error) {
	values, err := p.load()
	if err != nil {
		return true, err
	}
	enabled := true
	if raw, ok := values["checkForUpdates"]; ok {
		if err := json.Unmarshal(raw, &enabled); err != nil {
			return true, fmt.Errorf("invalid desktop preferences: %w", err)
		}
	}
	return enabled, nil
}

func (p FilePreferences) SaveUpdateCheck(enabled bool) error {
	return p.save("checkForUpdates", enabled)
}

// save replaces one key atomically. An unreadable file is replaced, as it was
// before preferences were merged, so choosing a collection can repair it.
func (p FilePreferences) save(key string, value any) error {
	values, err := p.load()
	if err != nil {
		values = map[string]json.RawMessage{}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	values[key] = raw
	data, err := json.Marshal(values)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.Directory, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(p.Directory, ".desktop-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(p.Directory, "desktop.json"))
}
```

- [ ] **Step 4: Add the update service**

`internal/desktop/updates.go`:

```go
package desktop

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"

	"recall/internal/update"
	"recall/internal/version"
)

// Updater is the part of *update.Updater the desktop uses.
type Updater interface {
	Check(context.Context) (update.Check, error)
	Fetch(context.Context, update.Check, func(stage string, done, total int64)) (string, error)
}

// UpdatePreferences is implemented by FilePreferences.
type UpdatePreferences interface {
	LoadUpdateCheck() (bool, error)
	SaveUpdateCheck(bool) error
}

type AppInfo struct {
	Version         string `json:"version"`
	Development     bool   `json:"development"`
	CheckForUpdates bool   `json:"checkForUpdates"`
}

// UpdateReply.State is "current", "available", "unsupported" (development
// build), "disabled" (launch check turned off) or "failed" (see Problem).
type UpdateReply struct {
	State      string   `json:"state"`
	Current    string   `json:"current"`
	Latest     string   `json:"latest"`
	ReleaseURL string   `json:"releaseURL"`
	Problem    *Problem `json:"problem"`
}

// UpgradeProgress is emitted as "desktop:upgrade-progress"; Stage is
// "downloading" or "verifying".
type UpgradeProgress struct {
	Stage string `json:"stage"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
}

type updateState struct {
	updater Updater
	current string
	handoff func(pkg string) error
	running atomic.Bool
}

// SetUpdater enables update checks. handoff receives a verified installer
// package and must open it as the app quits (see desktop/host.go).
func (s *Service) SetUpdater(u Updater, current string, handoff func(string) error) {
	s.upgrades.Store(&updateState{updater: u, current: current, handoff: handoff})
}

func (s *Service) AppInfo() AppInfo {
	info := AppInfo{Version: "dev", Development: true, CheckForUpdates: true}
	if p, ok := s.preferences.(UpdatePreferences); ok {
		info.CheckForUpdates, _ = p.LoadUpdateCheck() // unreadable: keep default
	}
	if u := s.upgrades.Load(); u != nil {
		_, valid := version.Parse(u.current)
		info.Version, info.Development = u.current, !valid
	}
	return info
}

func (s *Service) SetCheckForUpdates(enabled bool) OperationReply {
	p, ok := s.preferences.(UpdatePreferences)
	if !ok {
		return OperationReply{Problem: &Problem{Code: "runtime_unavailable", Message: "Preferences are unavailable"}}
	}
	if err := p.SaveUpdateCheck(enabled); err != nil {
		return OperationReply{Problem: &Problem{Code: "internal", Message: "Could not save the update preference"}}
	}
	return OperationReply{}
}

// CheckForUpdate asks GitHub for the latest release. A launch (non-manual)
// check makes no request when the user turned launch checks off.
func (s *Service) CheckForUpdate(manual bool) UpdateReply {
	info := s.AppInfo()
	reply := UpdateReply{Current: info.Version}
	u := s.upgrades.Load()
	switch {
	case u == nil || info.Development:
		reply.State = string(update.Unsupported)
	case !manual && !info.CheckForUpdates:
		reply.State = "disabled"
	default:
		check, err := u.updater.Check(context.Background())
		if err != nil {
			reply.State, reply.Problem = "failed", problem(err)
			break
		}
		reply.State, reply.Latest = string(check.State), check.Latest
		if check.Release != nil {
			reply.ReleaseURL = check.Release.URL
		}
	}
	return reply
}

// StartUpgrade downloads and verifies the latest release, then hands it off
// so the app quits (saving drafts) and opens it in Installer.
func (s *Service) StartUpgrade() OperationReply {
	u := s.upgrades.Load()
	if u == nil || s.AppInfo().Development {
		return OperationReply{Problem: &Problem{Code: "upgrade_unsupported", Message: "Updates are only available in release installs."}}
	}
	if !u.running.CompareAndSwap(false, true) {
		return OperationReply{Problem: &Problem{Code: "conflict", Message: "An upgrade is already in progress"}}
	}
	defer u.running.Store(false)
	ctx := context.Background()
	check, err := u.updater.Check(ctx)
	if err != nil {
		return OperationReply{Problem: problem(err)}
	}
	if check.State != update.Available {
		return OperationReply{Problem: &Problem{Code: "conflict", Message: "Recall " + check.Current + " is already up to date"}}
	}
	pkg, err := u.updater.Fetch(ctx, check, func(stage string, done, total int64) {
		s.emit("desktop:upgrade-progress", UpgradeProgress{Stage: stage, Done: done, Total: total})
	})
	if err != nil {
		return OperationReply{Problem: problem(err)}
	}
	if u.handoff == nil || u.handoff(pkg) != nil {
		os.RemoveAll(filepath.Dir(pkg))
		return OperationReply{Problem: &Problem{Code: "internal", Message: "Could not quit Recall to install the upgrade"}}
	}
	return OperationReply{}
}
```

- [ ] **Step 5: Add the service field and bindings**

In `internal/desktop/service.go`, add `"sync/atomic"` to the imports and add this field at the end of the `Service` struct (after `setupState  setupState`):

```go
	upgrades    atomic.Pointer[updateState]
```

In `internal/desktop/bindings.go`, append:

```go
func (a *API) AppInfo() AppInfo { return a.service.AppInfo() }
func (a *API) SetCheckForUpdates(enabled bool) OperationReply {
	return a.service.SetCheckForUpdates(enabled)
}
func (a *API) CheckForUpdate(manual bool) UpdateReply { return a.service.CheckForUpdate(manual) }
func (a *API) StartUpgrade() OperationReply           { return a.service.StartUpgrade() }
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test -race ./internal/desktop/ && go vet ./internal/desktop/ && gofmt -l internal/desktop`
Expected: `ok  recall/internal/desktop` (including the existing `TestRootPreferences`); no vet or gofmt output.

- [ ] **Step 7: Commit**

```bash
git add internal/desktop
git commit -m "Add desktop update checks, upgrade start and a launch-check preference

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Quit, then open Installer (desktop host)

**Files:**
- Modify: `desktop/host.go`, `desktop/main.go`
- Test: `desktop/host_test.go`

**Interfaces:**
- Consumes: `update.ForApp`, `(*Updater).Open` (Tasks 2-3); `(*Service).SetUpdater` (Task 6); `version.Version`.
- Produces: `Host.install func(string) error`; `func (*Host) InstallOnQuit(pkg string) error`; event `desktop:upgrade-cancelled` with payload `upgradeCancelled{Problem dto.Problem "problem"}`; messages `"Could not open Installer for the upgrade"` and `"Upgrade cancelled: unsaved changes could not be saved."`.

- [ ] **Step 1: Write the failing tests**

Append to `desktop/host_test.go` (add imports `"errors"`, `"slices"`):

```go
func TestUpgradeOpensInstallerBeforeClosingAndQuitting(t *testing.T) {
	var order []string
	var token string
	h := newHost(func(_ string, payload any) {
		if r, ok := payload.(quitRequest); ok {
			token = r.Token
		}
	}, func() { order = append(order, "quit") }, func(string) {})
	h.close = func() error { order = append(order, "close"); return nil }
	h.install = func(pkg string) error { order = append(order, "install "+pkg); return nil }
	if h.InstallOnQuit("/cache/a.pkg") == nil {
		t.Fatal("started an upgrade quit before the renderer was ready")
	}
	h.Ready()
	if err := h.InstallOnQuit("/cache/a.pkg"); err != nil || token == "" {
		t.Fatal(err, token)
	}
	if r := h.CompleteQuit(token); r.Problem != nil {
		t.Fatal(r.Problem)
	}
	if !slices.Equal(order, []string{"install /cache/a.pkg", "close", "quit"}) {
		t.Fatal(order)
	}
}

func TestInstallerFailureKeepsRecallRunning(t *testing.T) {
	var token string
	var cancelled []string
	h := newHost(func(_ string, payload any) {
		switch p := payload.(type) {
		case quitRequest:
			token = p.Token
		case upgradeCancelled:
			cancelled = append(cancelled, p.Problem.Message)
		}
	}, func() { t.Fatal("quit after Installer failed") }, func(string) {})
	h.close = func() error { t.Fatal("closed the collection after Installer failed"); return nil }
	h.install = func(string) error { return errors.New("open failed") }
	h.Ready()
	if err := h.InstallOnQuit("/cache/a.pkg"); err != nil {
		t.Fatal(err)
	}
	if r := h.CompleteQuit(token); r.Problem == nil || r.Problem.Message != "Could not open Installer for the upgrade" {
		t.Fatal(r.Problem)
	}
	h.CancelQuit(token) // the renderer cancels after a failed CompleteQuit
	if !slices.Equal(cancelled, []string{"Could not open Installer for the upgrade"}) {
		t.Fatal(cancelled)
	}
}

func TestCancelledQuitAbandonsTheUpgrade(t *testing.T) {
	var tokens, cancelled []string
	quits, installs := 0, 0
	h := newHost(func(_ string, payload any) {
		switch p := payload.(type) {
		case quitRequest:
			tokens = append(tokens, p.Token)
		case upgradeCancelled:
			cancelled = append(cancelled, p.Problem.Message)
		}
	}, func() { quits++ }, func(string) {})
	h.install = func(string) error { installs++; return nil }
	h.Ready()
	h.InstallOnQuit("/cache/a.pkg")
	h.CancelQuit(tokens[0]) // a draft could not be saved
	if !slices.Equal(cancelled, []string{"Upgrade cancelled: unsaved changes could not be saved."}) {
		t.Fatal(cancelled)
	}
	// A later ordinary quit must not open the abandoned package.
	h.BeforeClose(context.Background())
	if r := h.CompleteQuit(tokens[1]); r.Problem != nil || installs != 0 || quits != 1 {
		t.Fatal(r.Problem, installs, quits)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `(cd desktop && go test -run 'Upgrade|Installer' ./...)`
Expected: FAIL — build errors `h.install undefined`, `h.InstallOnQuit undefined`, `undefined: upgradeCancelled`.

- [ ] **Step 3: Implement the handoff in the host**

In `desktop/host.go`:

1. Add `"errors"` to the imports.
2. After `type quitRequest struct{...}` add:

```go
type upgradeCancelled struct {
	Problem dto.Problem `json:"problem"`
}

const (
	installFailed    = "Could not open Installer for the upgrade"
	upgradeAbandoned = "Upgrade cancelled: unsaved changes could not be saved."
)
```

3. Add fields to `Host` (after `copy func(string) error`):

```go
	install        func(string) error // opens a verified package in Installer
	pending        string             // package to open when the quit completes
	installProblem string
```

4. Add after `BeforeClose`:

```go
// InstallOnQuit records a verified installer package and starts the normal
// draft-protected quit; CompleteQuit opens the package just before exiting.
func (h *Host) InstallOnQuit(pkg string) error {
	h.mu.Lock()
	if !h.ready || h.allowed || h.completing || h.install == nil {
		h.mu.Unlock()
		return errors.New("the app cannot quit to install the upgrade now")
	}
	h.pending = pkg
	h.mu.Unlock()
	h.BeforeClose(context.Background())
	return nil
}
```

5. In `CompleteQuit`, replace

```go
	h.completing = true
	h.mu.Unlock()
	if h.close != nil {
```

with

```go
	h.completing = true
	pending := h.pending
	h.mu.Unlock()
	// Open Installer before closing anything, so a failure leaves Recall running.
	if pending != "" {
		if err := h.install(pending); err != nil {
			h.mu.Lock()
			h.completing = false
			h.installProblem = installFailed
			h.mu.Unlock()
			return dto.OperationReply{Problem: &dto.Problem{Code: "runtime_unavailable", Message: installFailed}}
		}
		h.mu.Lock()
		h.pending = ""
		h.mu.Unlock()
	}
	if h.close != nil {
```

6. Replace `CancelQuit` with:

```go
func (h *Host) CancelQuit(token string) dto.OperationReply {
	h.mu.Lock()
	cancelled := ""
	if token == h.token && !h.allowed && !h.completing {
		h.token = ""
		if h.pending != "" {
			cancelled = h.installProblem
			if cancelled == "" {
				cancelled = upgradeAbandoned
			}
			h.pending, h.installProblem = "", ""
		}
	}
	h.mu.Unlock()
	if cancelled != "" {
		h.emit("desktop:upgrade-cancelled", upgradeCancelled{Problem: dto.Problem{Code: "conflict", Message: cancelled}})
	}
	return dto.OperationReply{}
}
```

- [ ] **Step 4: Wire it in `main.go`**

In `desktop/main.go`, add imports `"recall/internal/update"` and `"recall/internal/version"`, and after the `host.choose = func() ... }` block add:

```go
	updater := update.ForApp(version.Version)
	host.install = func(pkg string) error { return updater.Open(context.Background(), pkg) }
	service.SetUpdater(updater, version.Version, host.InstallOnQuit)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `(cd desktop && go test -race ./... && go vet ./... && gofmt -l .)`
Expected: `ok  recall/desktop` (including the existing `TestBeforeCloseRequiresHandshake`); no vet or gofmt output.

- [ ] **Step 6: Commit**

```bash
git add desktop/host.go desktop/host_test.go desktop/main.go
git commit -m "Open verified upgrades in Installer as the desktop app quits

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Desktop UI — pop-up, version footer and ⚙ Settings

**Files:**
- Modify: `desktop/frontend/src/api/types.ts`, `src/api/bridge.ts`, `src/api/fakeBridge.ts`, `src/App.tsx`, `src/app.css`
- Create: `desktop/frontend/src/updates/useUpdates.ts`, `src/updates/UpgradePrompt.tsx`, `src/updates/VersionFooter.tsx`, `src/updates/SettingsDialog.tsx`
- Test: `desktop/frontend/src/updates/updates.test.tsx`

**Interfaces:**
- Consumes: bindings `AppInfo`, `SetCheckForUpdates`, `CheckForUpdate`, `StartUpgrade` (Task 6); events `desktop:upgrade-progress` `{stage, done, total}` (Task 6) and `desktop:upgrade-cancelled` `{problem}` (Task 7); existing `Modal` (`src/tree/Modal.tsx`) and `host.openExternal`.
- Produces: TS types `AppInfo`, `UpdateReply`, `UpgradeProgress`; `DesktopAPI.appInfo/setCheckForUpdates/checkForUpdate/startUpgrade`; `useUpdates(api, host): Updates`; type `UpgradePhase`.

All commands below run in `desktop/frontend` (run `npm ci` first if `node_modules` is missing).

- [ ] **Step 1: Write the failing tests**

`desktop/frontend/src/updates/updates.test.tsx`:

```tsx
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { expect, it } from "vitest";
import { App } from "../App";
import { fakeBridge } from "../api/fakeBridge";
import type { HostAPI, UpdateReply } from "../api/types";

const available: UpdateReply = {
  state: "available",
  current: "0.2.0",
  latest: "0.3.0",
  releaseURL: "https://github.com/moogie-labs/recall/releases/tag/v0.3.0",
  problem: null,
};

function eventHost(overrides: Partial<HostAPI> = {}) {
  const handlers = new Map<string, (payload: any) => void>();
  const host: HostAPI = {
    ready: async () => {},
    completeQuit: async () => ({ problem: null }),
    openExternal: async () => ({ problem: null }),
    on: (name, callback) => {
      handlers.set(name, callback);
      return () => handlers.delete(name);
    },
    ...overrides,
  };
  const emit = (name: string, payload: unknown) =>
    act(() => handlers.get(name)!(payload));
  return { host, emit };
}

it("offers an available upgrade on launch and keeps it in the footer after Not now", async () => {
  const api = fakeBridge();
  const checks: boolean[] = [];
  api.checkForUpdate = async (manual) => {
    checks.push(manual);
    return available;
  };
  const { host } = eventHost();
  render(<App api={api} host={host} />);
  expect(
    await screen.findByText("Recall 0.3.0 is available. You have 0.2.0."),
  ).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Not now" }));
  await waitFor(() =>
    expect(
      screen.queryByText("Recall 0.3.0 is available. You have 0.2.0."),
    ).not.toBeInTheDocument(),
  );
  expect(screen.getByText("Recall 0.2.0")).toBeInTheDocument();
  expect(
    screen.getByRole("button", { name: "Upgrade available" }),
  ).toBeInTheDocument();
  expect(checks).toEqual([false]);
});

it("opens release notes from the upgrade prompt", async () => {
  const api = fakeBridge();
  api.checkForUpdate = async () => available;
  const opened: string[] = [];
  const { host } = eventHost({
    openExternal: async (url) => {
      opened.push(url);
      return { problem: null };
    },
  });
  render(<App api={api} host={host} />);
  fireEvent.click(await screen.findByRole("button", { name: "Release notes" }));
  expect(opened).toEqual([available.releaseURL]);
});

it("shows upgrade progress until Installer opens", async () => {
  const api = fakeBridge();
  api.checkForUpdate = async () => available;
  let finish!: (reply: { problem: null }) => void;
  let starts = 0;
  api.startUpgrade = () => {
    starts++;
    return new Promise((resolve) => {
      finish = resolve;
    });
  };
  const { host, emit } = eventHost();
  render(<App api={api} host={host} />);
  fireEvent.click(await screen.findByRole("button", { name: "Upgrade" }));
  expect(starts).toBe(1);
  emit("desktop:upgrade-progress", { stage: "downloading", done: 50, total: 100 });
  expect(screen.getByText("Downloading Recall 0.3.0… 50%")).toBeInTheDocument();
  emit("desktop:upgrade-progress", { stage: "verifying", done: 100, total: 100 });
  expect(screen.getByText("Verifying…")).toBeInTheDocument();
  await act(async () => finish({ problem: null }));
  expect(screen.getByText("Opening Installer…")).toBeInTheDocument();
});

it("reports a failed or cancelled upgrade and offers a retry", async () => {
  const api = fakeBridge();
  api.checkForUpdate = async () => available;
  let starts = 0;
  api.startUpgrade = async () => {
    starts++;
    return {
      problem: {
        code: "verification_failed",
        message: "Gatekeeper rejected the downloaded installer",
        path: "",
      },
    };
  };
  const { host, emit } = eventHost();
  render(<App api={api} host={host} />);
  fireEvent.click(await screen.findByRole("button", { name: "Not now" }));
  fireEvent.click(screen.getByRole("button", { name: "Upgrade available" }));
  expect(
    await screen.findByText("Gatekeeper rejected the downloaded installer"),
  ).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Retry upgrade" }));
  await waitFor(() => expect(starts).toBe(2));
  emit("desktop:upgrade-cancelled", {
    problem: {
      code: "conflict",
      message: "Upgrade cancelled: unsaved changes could not be saved.",
      path: "",
    },
  });
  expect(
    screen.getByText("Upgrade cancelled: unsaved changes could not be saved."),
  ).toBeInTheDocument();
});

it("skips the launch check when it is off or in a development build", async () => {
  for (const info of [
    { version: "0.2.0", development: false, checkForUpdates: false },
    { version: "dev", development: true, checkForUpdates: true },
  ]) {
    const api = fakeBridge();
    let checks = 0;
    api.appInfo = async () => info;
    api.checkForUpdate = async () => {
      checks++;
      return available;
    };
    const { host } = eventHost();
    const { unmount } = render(<App api={api} host={host} />);
    expect(await screen.findByText("Recall " + info.version)).toBeInTheDocument();
    expect(checks).toBe(0);
    unmount();
  }
});

it("shows the version in Settings, saves the launch-check toggle and checks on demand", async () => {
  const api = fakeBridge();
  const saved: boolean[] = [],
    checks: boolean[] = [];
  let reply: UpdateReply = { ...available, state: "current", latest: "0.2.0" };
  api.setCheckForUpdates = async (enabled) => {
    saved.push(enabled);
    return { problem: null };
  };
  api.checkForUpdate = async (manual) => {
    checks.push(manual);
    return reply;
  };
  let starts = 0;
  api.startUpgrade = async () => {
    starts++;
    return { problem: null };
  };
  const { host } = eventHost();
  render(<App api={api} host={host} />);
  await screen.findByText("Recall 0.2.0");
  fireEvent.click(screen.getByRole("button", { name: "Settings" }));
  expect(screen.getByText("Version: Recall 0.2.0")).toBeInTheDocument();
  const toggle = screen.getByRole("checkbox", {
    name: "Check for updates on launch",
  });
  expect(toggle).toBeChecked();
  fireEvent.click(toggle);
  await waitFor(() => expect(toggle).not.toBeChecked());
  expect(saved).toEqual([false]);
  fireEvent.click(screen.getByRole("button", { name: "Check now" }));
  expect(await screen.findByText("Recall 0.2.0 is up to date.")).toBeInTheDocument();
  reply = available;
  fireEvent.click(screen.getByRole("button", { name: "Check now" }));
  expect(await screen.findByText("Recall 0.3.0 is available.")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Upgrade" }));
  await waitFor(() => expect(starts).toBe(1));
  expect(checks).toEqual([false, true, true]);
});

it("disables update controls in development builds", async () => {
  const api = fakeBridge();
  api.appInfo = async () => ({
    version: "dev",
    development: true,
    checkForUpdates: true,
  });
  const { host } = eventHost();
  render(<App api={api} host={host} />);
  await screen.findByText("Recall dev");
  fireEvent.click(screen.getByRole("button", { name: "Settings" }));
  expect(
    screen.getByRole("checkbox", { name: "Check for updates on launch" }),
  ).toBeDisabled();
  expect(screen.getByRole("button", { name: "Check now" })).toBeDisabled();
  expect(
    screen.getByText("Updates are only available in release installs."),
  ).toBeInTheDocument();
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `npx vitest run src/updates`
Expected: FAIL — TypeScript/transform errors such as `Property 'checkForUpdate' does not exist on type 'DesktopAPI'` or test failures finding `Recall 0.3.0 is available…`.

- [ ] **Step 3: Extend the API types, bridge and fake**

In `src/api/types.ts`, add to `interface DesktopAPI`:

```ts
  appInfo(): Promise<AppInfo>;
  setCheckForUpdates(enabled: boolean): Promise<OperationReply>;
  checkForUpdate(manual: boolean): Promise<UpdateReply>;
  startUpgrade(): Promise<OperationReply>;
```

and append:

```ts
export interface AppInfo {
  version: string;
  development: boolean;
  checkForUpdates: boolean;
}
export interface UpdateReply {
  state: "current" | "available" | "unsupported" | "disabled" | "failed";
  current: string;
  latest: string;
  releaseURL: string;
  problem: Problem | null;
}
export interface UpgradeProgress {
  stage: "downloading" | "verifying";
  done: number;
  total: number;
}
```

In `src/api/bridge.ts`, add to `api` (after `retrySetup`):

```ts
  appInfo: () => backend().AppInfo(),
  setCheckForUpdates: (enabled) => backend().SetCheckForUpdates(enabled),
  checkForUpdate: (manual) => backend().CheckForUpdate(manual),
  startUpgrade: () => backend().StartUpgrade(),
```

In `src/api/fakeBridge.ts`, add to the returned object (after `retrySetup`):

```ts
    appInfo: async () => ({
      version: "0.2.0",
      development: false,
      checkForUpdates: true,
    }),
    setCheckForUpdates: async () => ({ problem: null }),
    checkForUpdate: async () => ({
      state: "current",
      current: "0.2.0",
      latest: "0.2.0",
      releaseURL: "",
      problem: null,
    }),
    startUpgrade: async () => ({ problem: null }),
```

- [ ] **Step 4: Add the update hook**

`src/updates/useUpdates.ts`:

```ts
import { useCallback, useEffect, useState } from "react";
import type {
  AppInfo,
  DesktopAPI,
  HostAPI,
  UpdateReply,
  UpgradeProgress,
} from "../api/types";

export type UpgradePhase =
  | { kind: "idle" }
  | { kind: "downloading"; done: number; total: number }
  | { kind: "verifying" }
  | { kind: "quitting" }
  | { kind: "failed"; message: string };

export interface Updates {
  info: AppInfo | null;
  update: UpdateReply | null;
  prompting: boolean;
  phase: UpgradePhase;
  dismiss(): void;
  start(): void;
  checkNow(): Promise<UpdateReply>;
  setCheckOnLaunch(enabled: boolean): Promise<void>;
}

// Checks GitHub once at launch (unless turned off) and tracks an upgrade from
// download to the quit that opens Installer.
export function useUpdates(api: DesktopAPI, host: HostAPI): Updates {
  const [info, setInfo] = useState<AppInfo | null>(null),
    [update, setUpdate] = useState<UpdateReply | null>(null),
    [prompting, setPrompting] = useState(false),
    [phase, setPhase] = useState<UpgradePhase>({ kind: "idle" });
  useEffect(() => {
    let active = true;
    void api
      .appInfo()
      .then(async (current) => {
        if (!active) return;
        setInfo(current);
        if (current.development || !current.checkForUpdates) return;
        const reply = await api.checkForUpdate(false);
        // Launch-check failures stay silent; Settings › Check now shows them.
        if (!active || reply.state !== "available") return;
        setUpdate(reply);
        setPrompting(true);
      })
      .catch(() => {});
    const offProgress = host.on(
      "desktop:upgrade-progress",
      (p: UpgradeProgress) =>
        setPhase(
          p.stage === "verifying"
            ? { kind: "verifying" }
            : { kind: "downloading", done: p.done, total: p.total },
        ),
    );
    const offCancelled = host.on(
      "desktop:upgrade-cancelled",
      (p: { problem?: { message: string } }) =>
        setPhase({
          kind: "failed",
          message: p?.problem?.message ?? "Upgrade cancelled.",
        }),
    );
    return () => {
      active = false;
      offProgress();
      offCancelled();
    };
  }, [api, host]);
  const start = useCallback(() => {
    setPrompting(false);
    setPhase({ kind: "downloading", done: 0, total: 0 });
    void api.startUpgrade().then(
      (r) =>
        setPhase(
          r.problem
            ? { kind: "failed", message: r.problem.message }
            : { kind: "quitting" },
        ),
      (e: Error) => setPhase({ kind: "failed", message: e.message }),
    );
  }, [api]);
  const checkNow = useCallback(async () => {
    const reply = await api.checkForUpdate(true);
    if (reply.state === "available" || reply.state === "current")
      setUpdate(reply);
    return reply;
  }, [api]);
  const setCheckOnLaunch = useCallback(
    async (enabled: boolean) => {
      const r = await api.setCheckForUpdates(enabled);
      if (r.problem) throw new Error(r.problem.message);
      setInfo((current) => current && { ...current, checkForUpdates: enabled });
    },
    [api],
  );
  const dismiss = useCallback(() => setPrompting(false), []);
  return { info, update, prompting, phase, dismiss, start, checkNow, setCheckOnLaunch };
}
```

- [ ] **Step 5: Add the three components**

`src/updates/UpgradePrompt.tsx`:

```tsx
import type { UpdateReply } from "../api/types";
import { Modal } from "../tree/Modal";
export function UpgradePrompt({
  update,
  onUpgrade,
  onDismiss,
  onReleaseNotes,
}: {
  update: UpdateReply;
  onUpgrade: () => void;
  onDismiss: () => void;
  onReleaseNotes: () => void;
}) {
  return (
    <Modal title="Upgrade available" onCancel={onDismiss}>
      <p>{`Recall ${update.latest} is available. You have ${update.current}.`}</p>
      <p>
        <button className="link-button" onClick={onReleaseNotes}>
          Release notes
        </button>
      </p>
      <div className="dialog-buttons">
        <button onClick={onDismiss}>Not now</button>
        <button className="primary" autoFocus onClick={onUpgrade}>
          Upgrade
        </button>
      </div>
    </Modal>
  );
}
```

`src/updates/VersionFooter.tsx`:

```tsx
import type { AppInfo, UpdateReply } from "../api/types";
import type { UpgradePhase } from "./useUpdates";
const percent = (done: number, total: number) =>
  total > 0 ? ` ${Math.floor((done / total) * 100)}%` : "";
export function VersionFooter({
  info,
  update,
  phase,
  onUpgrade,
}: {
  info: AppInfo | null;
  update: UpdateReply | null;
  phase: UpgradePhase;
  onUpgrade: () => void;
}) {
  if (!info) return null;
  let status = null;
  if (phase.kind === "downloading")
    status = (
      <span role="status">{`Downloading Recall ${update?.latest ?? ""}…${percent(phase.done, phase.total)}`}</span>
    );
  else if (phase.kind === "verifying")
    status = <span role="status">Verifying…</span>;
  else if (phase.kind === "quitting")
    status = <span role="status">Opening Installer…</span>;
  else if (phase.kind === "failed")
    status = (
      <span role="alert">
        {phase.message}{" "}
        <button className="link-button" onClick={onUpgrade}>
          Retry upgrade
        </button>
      </span>
    );
  return (
    <div className="version-footer">
      <span>{`Recall ${info.version}`}</span>
      {update?.state === "available" && phase.kind === "idle" && (
        <button className="upgrade-available" onClick={onUpgrade}>
          Upgrade available
        </button>
      )}
      {status}
    </div>
  );
}
```

`src/updates/SettingsDialog.tsx`:

```tsx
import { useState } from "react";
import type { AppInfo, UpdateReply } from "../api/types";
import { Modal } from "../tree/Modal";
export function SettingsDialog({
  info,
  onClose,
  onCheck,
  onToggle,
  onUpgrade,
}: {
  info: AppInfo;
  onClose: () => void;
  onCheck: () => Promise<UpdateReply>;
  onToggle: (enabled: boolean) => Promise<void>;
  onUpgrade: () => void;
}) {
  const [result, setResult] = useState<UpdateReply | null>(null),
    [checking, setChecking] = useState(false),
    [error, setError] = useState("");
  const check = () => {
    setChecking(true);
    setError("");
    void onCheck()
      .then(setResult, (e: Error) => setError(e.message))
      .finally(() => setChecking(false));
  };
  let message = "";
  if (result?.problem) message = result.problem.message;
  else if (result?.state === "current")
    message = `Recall ${result.current} is up to date.`;
  else if (result?.state === "available")
    message = `Recall ${result.latest} is available.`;
  return (
    <Modal title="Settings" onCancel={onClose}>
      <p>{`Version: Recall ${info.version}`}</p>
      <label className="setting-toggle">
        <input
          type="checkbox"
          checked={info.checkForUpdates}
          disabled={info.development}
          onChange={(e) => {
            setError("");
            void onToggle(e.target.checked).catch((err: Error) =>
              setError(err.message),
            );
          }}
        />
        Check for updates on launch
      </label>
      {info.development && (
        <p>Updates are only available in release installs.</p>
      )}
      {message && <p role="status">{message}</p>}
      {error && <p role="alert">{error}</p>}
      <div className="dialog-buttons">
        <button disabled={info.development || checking} onClick={check}>
          {checking ? "Checking…" : "Check now"}
        </button>
        {result?.state === "available" && (
          <button
            className="primary"
            onClick={() => {
              onClose();
              onUpgrade();
            }}
          >
            Upgrade
          </button>
        )}
        <button onClick={onClose}>Done</button>
      </div>
    </Modal>
  );
}
```

- [ ] **Step 6: Integrate into `App.tsx`**

1. Add imports:

```tsx
import { useUpdates } from "./updates/useUpdates";
import { UpgradePrompt } from "./updates/UpgradePrompt";
import { VersionFooter } from "./updates/VersionFooter";
import { SettingsDialog } from "./updates/SettingsDialog";
```

2. After `const state = useSyncExternalStore(...)`, add:

```tsx
  const updates = useUpdates(api, host);
  const [settings, setSettings] = useState(false);
```

3. In the header, after the `Collection…` button, add:

```tsx
        <button
          className="icon"
          aria-label="Settings"
          disabled={!updates.info}
          onClick={() => setSettings(true)}
        >
          <svg
            width="17"
            height="17"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.5"
            aria-hidden="true"
          >
            <circle cx="12" cy="12" r="3" />
            <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1-2.83 0l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-2 2 2 2 0 0 1-2-2v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83 0 2 2 0 0 1 0-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1-2-2 2 2 0 0 1 2-2h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 2-2 2 2 0 0 1 2 2v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 2 2 2 2 0 0 1-2 2h-.09a1.65 1.65 0 0 0-1.51 1z" />
          </svg>
        </button>
```

4. After `<p className="tree-note">Plain Markdown. Yours to keep.</p>` add:

```tsx
            <VersionFooter
              info={updates.info}
              update={updates.update}
              phase={updates.phase}
              onUpgrade={updates.start}
            />
```

5. Before the final closing `</div>` of the component (after the `deleting && <DeleteDialog …/>` block), add:

```tsx
      {updates.prompting && updates.update && (
        <UpgradePrompt
          update={updates.update}
          onUpgrade={updates.start}
          onDismiss={updates.dismiss}
          onReleaseNotes={() => {
            void host.openExternal(updates.update!.releaseURL);
          }}
        />
      )}
      {settings && updates.info && (
        <SettingsDialog
          info={updates.info}
          onClose={() => setSettings(false)}
          onCheck={updates.checkNow}
          onToggle={updates.setCheckOnLaunch}
          onUpgrade={updates.start}
        />
      )}
```

- [ ] **Step 7: Add styles**

Append to `src/app.css`:

```css
.version-footer {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 10px;
  padding: 0 10px 18px;
  font-size: 11px;
  color: #999b91;
}
.version-footer .upgrade-available {
  border: 1px solid #476047;
  background: #eef3ea;
  color: #476047;
  border-radius: 10px;
  padding: 2px 8px;
  font-size: 11px;
}
.version-footer [role="alert"] {
  color: #884c36;
}
.link-button {
  border: 0;
  background: none;
  padding: 0;
  color: #476047;
  text-decoration: underline;
  font: inherit;
  cursor: pointer;
}
dialog .setting-toggle {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 4px 0 16px;
}
dialog .setting-toggle input {
  display: inline-block;
  width: auto;
  margin: 0;
}
```

- [ ] **Step 8: Run tests, typecheck and format**

Run: `npx prettier --write src && npm run typecheck && npm test`
Expected: typecheck passes; all Vitest suites pass (the new `src/updates/updates.test.tsx` and the existing `App.test.tsx` etc.).

Run: `npm run test:e2e`
Expected: existing Playwright specs pass (the test fixture uses `fakeBridge`, whose launch check reports `current`).

- [ ] **Step 9: Commit**

```bash
git add desktop/frontend/src
git commit -m "Show the version, upgrade prompt and Settings in the desktop app

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: Documentation and full verification

**Files:**
- Modify: `README.md`, `docs/desktop.md`, `docs/verification.md`, `docs/superpowers/specs/2026-11-10-version-and-upgrade-design.md`

- [ ] **Step 1: Update the README**

1. In the opening paragraph, after `No accounts, cloud API, Ollama, launch agent, or generated answers.` add the sentence: `The desktop app's only routine network request is an optional launch check for new releases on GitHub.`
2. At the end of the `## Install from a GitHub release (recommended)` section (after `Release automation and maintainer setup are described in [releases](docs/releases.md).`), add:

````markdown
### Upgrading

Releases after 0.1.0 can upgrade themselves from GitHub Releases. When the
desktop app launches, it asks GitHub (one anonymous request) whether a newer
stable release exists and offers to upgrade. **Not now** leaves an **Upgrade
available** button beside the version at the bottom of the sidebar; the pop-up
returns on the next launch. Turn the launch check off under **⚙ Settings**,
which also shows the version and has **Check now**. From the terminal:

```sh
recall version
recall upgrade --check
recall upgrade
```

Either way, Recall downloads the installer and its checksum and requires a
matching checksum, a notarized Developer ID Installer signature from the same
developer team as your installed copy, and Gatekeeper's approval before opening
it in macOS Installer (which asks for administrator authorization as usual).
The desktop app quits first, saving drafts as it does on ⌘Q. Only copies
installed by the release installer can upgrade; Recall 0.1.0 predates this
feature, so install the next release manually once.
````

- [ ] **Step 2: Update `docs/desktop.md`**

After the `### First launch and semantic-search setup` subsection (before `## Browsing, editing, and deletion`), add:

```markdown
## Version, updates and settings

The bottom of the sidebar shows the running version (`Recall dev` for source
builds). The ⚙ **Settings** button in the title bar shows the version, a
**Check for updates on launch** toggle (on by default, stored as
`checkForUpdates` in `desktop.json` beside the remembered collection) and
**Check now**.

With the toggle on, each launch makes one anonymous request to the GitHub
Releases API. If a newer stable release exists, a pop-up offers **Upgrade**,
**Not now** and **Release notes**; it appears on every launch until you upgrade
or turn the check off. After **Not now**, the sidebar footer shows
**Upgrade available**. Launch-check failures (for example, offline) are silent;
**Check now** reports them.

Upgrading downloads the installer to `~/Library/Caches/recall/upgrade-*`,
showing progress in the footer, and verifies its checksum, its notarized
Developer ID Installer signature (same Apple team as the running app) and
Gatekeeper's install assessment. The app then quits through the normal ⌘Q
sequence, saving drafts; macOS Installer opens just before the collection closes.
If a draft cannot be saved, the upgrade is cancelled and Installer does not open.
Only `/Applications/Recall.app` installed by the release installer can upgrade
itself; development builds disable these controls.
```

- [ ] **Step 3: Align the spec with two implementation details**

In `docs/superpowers/specs/2026-11-10-version-and-upgrade-design.md`:
- In §4.3, replace `(percentage progress on stderr when it is a terminal)` with `(percentage progress on stderr in 10% steps, like \`recall init\`)`.
- In §4.4, replace `` `state` one of `current`, `available`,
  `unsupported`, `disabled` `` with `` `state` one of `current`, `available`,
  `unsupported`, `disabled`, or `failed` (with `problem`) ``.

- [ ] **Step 4: Run the complete credential-free verification**

Run from the repository root:

```bash
gofmt -l . desktop
go vet ./...
go test -race ./...
sh scripts/build-desktop.sh
(cd desktop && go vet ./... && go test -race ./...)
python3 scripts/verify-desktop.py build/Recall.app
(cd desktop/frontend && npm run typecheck && npm test && npm run test:e2e)
python3 -m unittest discover -s scripts -p 'test_release_macos.py'
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 .github/workflows/release.yml
```

Expected: no gofmt output; every command exits 0.

- [ ] **Step 5: Check against the real GitHub release**

```bash
RECALL_LIVE_UPDATE_TEAM=SS25FU43FL go test ./internal/update -run Live -v
go build -ldflags "-X recall/internal/version.Version=0.0.9" -o build/recall ./cmd/recall
./build/recall upgrade --check
./build/recall --json upgrade --check
./build/recall upgrade; echo "exit $?"
./build/recall version
go build -o build/recall-dev ./cmd/recall && ./build/recall-dev upgrade; echo "exit $?"
```

Expected:
- Live test passes (real download, real `pkgutil`/`spctl` verification of the latest release).
- `Recall 0.1.0 is available (installed: 0.0.9).` followed by the release-notes line (or the current latest release if newer).
- JSON with `"update_available":true`.
- `recall upgrade` exits 1 with `upgrade_unsupported` (`only the copy installed by the Recall installer at /usr/local/lib/recall/recall can upgrade itself…`) and downloads nothing.
- `recall 0.0.9`.
- The dev build exits 1: `this is a development build of Recall…`.

- [ ] **Step 6: Visual check of the desktop app (manual)**

```bash
sh scripts/build-desktop.sh -ldflags "-X recall/internal/version.Version=0.0.9"
open build/Recall.app
```

Expected: the **Upgrade available** pop-up for the latest release; **Release notes** opens the GitHub page; **Not now** leaves `Recall 0.0.9` + **Upgrade available** in the sidebar footer; ⚙ **Settings** shows the version and toggle; **Upgrade** fails inline with the "only the copy installed by the Recall installer at /Applications/Recall.app…" message (this build is not in `/Applications`), and the app keeps running. Rebuild without `-ldflags` afterwards (`sh scripts/build-desktop.sh`) so `build/Recall.app` is a normal dev build again.

- [ ] **Step 7: Record verification**

Append to `docs/verification.md` a section `## Version and upgrade (2026-11-10)` listing each command from Steps 4–6 with its actual outcome (pass/fail and any notable output), plus the remaining limit: *a full installed-release → newer-signed-release upgrade, including Installer completing and the new app launching, can only be verified once a release containing this feature is installed and a later release is published.*

- [ ] **Step 8: Commit**

```bash
git add README.md docs
git commit -m "Document version reporting and upgrades

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
