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
