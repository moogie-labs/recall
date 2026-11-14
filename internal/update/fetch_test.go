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
		"moved copy": func(u *Updater, _ *fakeTools) {
			u.Location = func() (string, error) { return "/Users/me/Applications/Recall.app", nil }
		},
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
