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
