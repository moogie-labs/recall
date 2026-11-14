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
	dir                string // when set, Fetch returns a package inside it
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
	if f.dir != "" {
		pkg := filepath.Join(f.dir, "upgrade-1", update.PackageName(c.Latest))
		if err := os.MkdirAll(filepath.Dir(pkg), 0o700); err != nil {
			return "", err
		}
		return pkg, os.WriteFile(pkg, []byte("pkg"), 0o600)
	}
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
			s, _ := updateService(t, tc.current, tc.u, func(string) error { t.Fatal("handed off a failed upgrade"); return nil })
			if r := s.StartUpgrade(); r.Problem == nil || r.Problem.Code != tc.code {
				t.Fatal(r.Problem)
			}
		})
	}
	tmp := t.TempDir()
	s, _ := updateService(t, "0.2.0", &fakeUpdater{check: availableCheck(), dir: tmp}, func(string) error { return errors.New("not ready") })
	if r := s.StartUpgrade(); r.Problem == nil || r.Problem.Message != "Could not quit Recall to install the upgrade" {
		t.Fatal(r.Problem)
	}
	if _, err := os.Stat(filepath.Join(tmp, "upgrade-1")); !os.IsNotExist(err) {
		t.Fatal("download directory was not removed after the handoff failed:", err)
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
