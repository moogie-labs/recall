package desktop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"recall/internal/config"
)

// Setup lets the desktop app prepare a collection itself: create the default
// collection on first launch, then install the semantic-search assets in the
// background while notes, tasks and keyword search are already usable.
type Setup struct {
	// Prepare creates the collection layout and pinned configuration only.
	Prepare func(context.Context, config.Config) (config.Config, error)
	// Ready reports whether the semantic assets already appear installed.
	Ready func(config.Config) bool
	// Install verifies/installs the runtime and downloads the model.
	Install func(context.Context, config.Config, func(done, total int64)) error
	// Legacy is a former default root; a collection there blocks automatic
	// creation so a fresh empty collection never hides existing notes.
	Legacy string
}

// SetupStatus describes semantic-search setup for one collection session.
// State is "preparing", "downloading", "ready" or "failed".
type SetupStatus struct {
	Session string   `json:"session"`
	State   string   `json:"state"`
	Done    int64    `json:"done"`
	Total   int64    `json:"total"`
	Problem *Problem `json:"problem"`
}

type setupState struct {
	mu     sync.Mutex
	status SetupStatus
}

// SetSetup enables automatic setup. Without it the service never creates a
// collection or installs assets (the previous, CLI-initialized behavior).
func (s *Service) SetSetup(setup *Setup) {
	s.lifecycle.Lock()
	s.setup = setup
	s.lifecycle.Unlock()
}

// createDefault prepares the default collection when it is safe to do so: the
// root is missing or an empty directory, and no legacy collection exists.
func (s *Service) createDefault(ctx context.Context, cfg config.Config) (config.Config, *Problem) {
	if s.setup.Legacy != "" {
		if _, err := os.Lstat(filepath.Join(s.setup.Legacy, "config.json")); err == nil {
			return cfg, &Problem{Code: "not_initialized", Path: s.setup.Legacy, Message: "Found an existing Recall collection at " + s.setup.Legacy + ". Use Collection… to open it."}
		}
	}
	if info, err := os.Lstat(cfg.Root); err == nil {
		entries, readErr := os.ReadDir(cfg.Root)
		if !info.IsDir() || readErr != nil || len(entries) != 0 {
			return cfg, &Problem{Code: "not_initialized", Path: cfg.Root, Message: cfg.Root + " already exists but is not a Recall collection; choose another with Collection…"}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return cfg, problem(err)
	}
	prepared, err := s.setup.Prepare(ctx, cfg)
	if err != nil {
		return cfg, problem(err)
	}
	return prepared, nil
}

func (s *Service) publishSetup(status SetupStatus) {
	s.setupState.mu.Lock()
	if s.setupState.status.Session != status.Session {
		s.setupState.mu.Unlock()
		return // A newer collection session owns the status.
	}
	s.setupState.status = status
	s.setupState.mu.Unlock()
	s.emit("desktop:setup", status)
}

// startSetup must run with the lifecycle lock held (it joins s.workers).
func (s *Service) startSetup(ctx context.Context, cfg config.Config, session string) {
	s.setupState.mu.Lock()
	s.setupState.status = SetupStatus{Session: session, State: "ready"}
	s.setupState.mu.Unlock()
	if s.setup == nil || s.setup.Install == nil || (s.setup.Ready != nil && s.setup.Ready(cfg)) {
		return
	}
	s.publishSetup(SetupStatus{Session: session, State: "preparing"})
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		last := int64(-1)
		err := s.setup.Install(ctx, cfg, func(done, total int64) {
			if total <= 0 || done*100/total == last {
				return
			}
			last = done * 100 / total
			s.publishSetup(SetupStatus{Session: session, State: "downloading", Done: done, Total: total})
		})
		if ctx.Err() != nil {
			return // Collection switched or app quitting; setup is retryable.
		}
		if err != nil {
			s.publishSetup(SetupStatus{Session: session, State: "failed", Problem: problem(err)})
			return
		}
		s.publishSetup(SetupStatus{Session: session, State: "ready"})
	}()
}

func (s *Service) SetupStatus(session string) SetupStatus {
	s.mu.RLock()
	p := s.check(session)
	s.mu.RUnlock()
	if p != nil {
		return SetupStatus{Session: session, State: "failed", Problem: p}
	}
	s.setupState.mu.Lock()
	defer s.setupState.mu.Unlock()
	return s.setupState.status
}

// RetrySetup restarts a failed asset installation for the current session.
func (s *Service) RetrySetup(session string) OperationReply {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.RLock()
	p := s.check(session)
	ctx, cfg := s.ctx, s.cfg
	s.mu.RUnlock()
	if p != nil {
		return OperationReply{Problem: p}
	}
	if s.SetupStatus(session).State != "failed" {
		return OperationReply{}
	}
	s.startSetup(ctx, cfg, session)
	return OperationReply{}
}

// semanticUnavailable short-circuits hybrid search while assets are not ready,
// instead of starting an inference worker that cannot verify its assets.
func (s *Service) semanticUnavailable(session string) *Problem {
	s.setupState.mu.Lock()
	status := s.setupState.status
	s.setupState.mu.Unlock()
	if status.Session != session {
		return nil
	}
	switch status.State {
	case "preparing", "downloading":
		return &Problem{Code: "runtime_unavailable", Message: "Semantic search is still being set up. Search keywords meanwhile."}
	case "failed":
		return &Problem{Code: "runtime_unavailable", Message: "Semantic search setup failed. Search keywords instead, or retry setup."}
	}
	return nil
}
