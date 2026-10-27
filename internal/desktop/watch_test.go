package desktop

import (
	"context"
	"os"
	"path/filepath"
	"recall/internal/config"
	"sync/atomic"
	"testing"
	"time"
)

type watchedApp struct {
	Application
	active  atomic.Int32
	maximum atomic.Int32
}

func (a *watchedApp) IndexPending(ctx context.Context) error {
	n := a.active.Add(1)
	defer a.active.Add(-1)
	for {
		old := a.maximum.Load()
		if n <= old || a.maximum.CompareAndSwap(old, n) {
			break
		}
	}
	<-ctx.Done()
	return ctx.Err()
}
func TestWatchLifecycle(t *testing.T) {
	s, a, cfg, _ := fixture(t)
	wrapped := &watchedApp{Application: a}
	s.open = func(context.Context, config.Config) (Application, error) { return wrapped, nil }
	s.mu.Lock()
	s.application = nil
	s.mu.Unlock()
	events := make(chan CollectionEvent, 20)
	s.SetEventSink(func(name string, payload any) {
		if name == "desktop:collection-changed" {
			select {
			case events <- payload.(CollectionEvent):
			default:
			}
		}
	})
	session := s.OpenCollection(cfg.Root).Session
	select {
	case e := <-events:
		if e.Session != session {
			t.Fatal("wrong session")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no initial refresh")
	}
	os.Mkdir(filepath.Join(cfg.Content, "external"), 0700)
	select {
	case <-events:
	case <-time.After(3 * time.Second):
		t.Fatal("external change not observed")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if wrapped.active.Load() != 0 || wrapped.maximum.Load() > 1 {
		t.Fatal("index task leaked/overlapped")
	}
	if s.Refresh(session).Problem == nil {
		t.Fatal("closed session still active")
	}
}
