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
		// Edge channel builds (release.yml) stay put until a stable release passes them.
		{"0.3.1-edge.147", "v0.3.0", Current},
		{"0.3.1-edge.147", "v0.3.1", Available},
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
		"edge channel":      latest(fakeRelease{Tag: "edge", Prerelease: true}),
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
