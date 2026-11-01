package desktop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"recall/internal/app"
	"recall/internal/bootstrap"
	"recall/internal/config"
	"recall/internal/model"
)

type recorder struct {
	mu     sync.Mutex
	events []SetupStatus
}

func (r *recorder) sink(name string, payload any) {
	if status, ok := payload.(SetupStatus); ok && name == "desktop:setup" {
		r.mu.Lock()
		r.events = append(r.events, status)
		r.mu.Unlock()
	}
}

func (r *recorder) states() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var states []string
	for _, e := range r.events {
		if len(states) == 0 || states[len(states)-1] != e.State {
			states = append(states, e.State)
		}
	}
	return states
}

func setupService(t *testing.T, setup *Setup) (*Service, *recorder, string) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir()) // Recall canonicalizes /var -> /private/var.
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("RECALL_HOME", "")
	s := NewService(func(ctx context.Context, cfg config.Config) (Application, error) { return app.Open(ctx, cfg) }, nil)
	events := &recorder{}
	s.SetEventSink(events.sink)
	s.SetSetup(setup)
	t.Cleanup(func() { s.Close() })
	return s, events, filepath.Join(home, "Documents", "recall")
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestFirstLaunchCreatesDefaultCollectionAndInstallsAssetsInBackground(t *testing.T) {
	release := make(chan struct{})
	var installed config.Config
	s, events, root := setupService(t, &Setup{
		Prepare: bootstrap.PrepareMetadata,
		Ready:   func(config.Config) bool { return false },
		Install: func(ctx context.Context, cfg config.Config, progress func(int64, int64)) error {
			installed = cfg
			progress(0, 200)
			progress(1, 200) // Below 1%: not republished.
			progress(100, 200)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			progress(200, 200)
			return nil
		},
	})
	r := s.OpenCollection("")
	if r.Problem != nil || r.Session == "" || r.Root != root {
		t.Fatalf("first launch: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(root, "config.json")); err != nil {
		t.Fatal("default collection not created:", err)
	}
	waitFor(t, func() bool { return s.SetupStatus(r.Session).State == "downloading" })
	if status := s.SetupStatus(r.Session); status.Done != 100 || status.Total != 200 {
		t.Fatalf("progress %+v", status)
	}
	// Notes and keyword search work immediately; hybrid explains why it waits.
	if c := s.Create(CreateInput{Session: r.Session, Kind: "note", Name: "first"}); c.Problem != nil {
		t.Fatal(c.Problem)
	}
	if h := s.Search(SearchInput{Session: r.Session, Query: "first", Mode: "keyword"}); h.Problem != nil {
		t.Fatal(h.Problem)
	}
	if h := s.Search(SearchInput{Session: r.Session, Query: "first", Mode: "hybrid"}); h.Problem == nil || h.Problem.Code != "runtime_unavailable" {
		t.Fatalf("hybrid search during setup: %+v", h)
	}
	close(release)
	waitFor(t, func() bool { return s.SetupStatus(r.Session).State == "ready" })
	if got := strings.Join(events.states(), ","); got != "preparing,downloading,ready" {
		t.Fatalf("setup events %s", got)
	}
	if installed.ModelID == "" || installed.Root != root {
		t.Fatalf("install received an unprepared configuration: %+v", installed)
	}
	if p := s.semanticUnavailable(r.Session); p != nil {
		t.Fatal("hybrid search still gated after setup:", p)
	}
}

func TestAutomaticSetupNeverCreatesChosenOccupiedOrShadowingCollections(t *testing.T) {
	prepared := 0
	setup := &Setup{
		Prepare: func(ctx context.Context, cfg config.Config) (config.Config, error) {
			prepared++
			return bootstrap.PrepareMetadata(ctx, cfg)
		},
		Ready:   func(config.Config) bool { return true },
		Install: func(context.Context, config.Config, func(int64, int64)) error { return nil },
	}
	t.Run("chosen root", func(t *testing.T) {
		s, _, _ := setupService(t, setup)
		missing := filepath.Join(t.TempDir(), "typo")
		if r := s.OpenCollection(missing); r.Problem == nil || r.Problem.Code != "not_initialized" {
			t.Fatalf("missing chosen root: %+v", r)
		}
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Fatal("chosen root was created")
		}
	})
	t.Run("occupied default", func(t *testing.T) {
		s, _, root := setupService(t, setup)
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("mine"), 0600); err != nil {
			t.Fatal(err)
		}
		if r := s.OpenCollection(""); r.Problem == nil || r.Problem.Code != "not_initialized" {
			t.Fatalf("occupied default: %+v", r)
		}
		if _, err := os.Stat(filepath.Join(root, "config.json")); !os.IsNotExist(err) {
			t.Fatal("occupied folder was turned into a collection")
		}
	})
	t.Run("legacy collection", func(t *testing.T) {
		s, _, root := setupService(t, setup)
		legacy := filepath.Join(os.Getenv("HOME"), ".local", "share", "recall")
		if err := config.Prepare(config.Config{Root: legacy}); err != nil {
			t.Fatal(err)
		}
		setup.Legacy = legacy
		defer func() { setup.Legacy = "" }()
		r := s.OpenCollection("")
		if r.Problem == nil || !strings.Contains(r.Problem.Message, legacy) {
			t.Fatalf("legacy collection: %+v", r)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatal("new default collection would hide the existing one")
		}
	})
	if prepared != 0 {
		t.Fatalf("Prepare called %d times", prepared)
	}
}

func TestFailedAssetSetupIsReportedAndRetryable(t *testing.T) {
	attempts := 0
	s, events, _ := setupService(t, &Setup{
		Prepare: bootstrap.PrepareMetadata,
		Ready:   func(config.Config) bool { return false },
		Install: func(context.Context, config.Config, func(int64, int64)) error {
			attempts++
			if attempts == 1 {
				return &model.Error{Code: "runtime_unavailable", Message: "asset download failed"}
			}
			return nil
		},
	})
	r := s.OpenCollection("")
	if r.Problem != nil {
		t.Fatal(r.Problem)
	}
	waitFor(t, func() bool { return s.SetupStatus(r.Session).State == "failed" })
	if p := s.SetupStatus(r.Session).Problem; p == nil || p.Message != "asset download failed" {
		t.Fatalf("failure not reported: %+v", p)
	}
	if h := s.Search(SearchInput{Session: r.Session, Query: "x", Mode: "hybrid"}); h.Problem == nil || !strings.Contains(h.Problem.Message, "setup failed") {
		t.Fatalf("hybrid search after failure: %+v", h)
	}
	if reply := s.RetrySetup("stale-session"); reply.Problem == nil {
		t.Fatal("retry accepted a stale session")
	}
	if reply := s.RetrySetup(r.Session); reply.Problem != nil {
		t.Fatal(reply.Problem)
	}
	waitFor(t, func() bool { return s.SetupStatus(r.Session).State == "ready" })
	if attempts != 2 || !strings.HasSuffix(strings.Join(events.states(), ","), "failed,preparing,ready") {
		t.Fatalf("attempts=%d events=%v", attempts, events.states())
	}
}

func TestInstalledAssetsSkipSetupAndUnconfiguredServiceNeverCreates(t *testing.T) {
	s, events, root := setupService(t, &Setup{
		Prepare: bootstrap.PrepareMetadata,
		Ready:   func(config.Config) bool { return true },
		Install: func(context.Context, config.Config, func(int64, int64)) error {
			return errors.New("install must not run when assets are present")
		},
	})
	r := s.OpenCollection("")
	if r.Problem != nil || s.SetupStatus(r.Session).State != "ready" || len(events.states()) != 0 {
		t.Fatalf("%+v %+v %v", r, s.SetupStatus(r.Session), events.states())
	}
	s.Close()
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	plain, _, _ := setupService(t, nil)
	if r := plain.OpenCollection(""); r.Problem == nil || r.Problem.Code != "not_initialized" {
		t.Fatalf("service without setup created a collection: %+v", r)
	}
}
