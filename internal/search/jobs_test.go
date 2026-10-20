package search

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"recall/internal/model"
)

func waitStarted(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(5 * time.Second):
		t.Fatal("embedding did not start")
	}
}
func TestDeletedDuringEmbeddingCannotPublish(t *testing.T) {
	e, s, idx, p := searchFixture(t, model.Document{ID: "a", Title: "needle", Body: "needle"})
	p.started = make(chan struct{})
	p.release = make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- e.RunPending(t.Context()) }()
	waitStarted(t, p.started)
	if err := s.WithLock(t.Context(), func() error { s.docs = nil; _, err := idx.Apply(t.Context(), model.Snapshot{}); return err }); err != nil {
		t.Fatal(err)
	}
	close(p.release)
	if err := <-done; err != nil && model.ErrorCode(err) != "indexing" {
		t.Fatal(err)
	}
	n := 0
	if err := idx.StreamVectors(t.Context(), model.Filter{}, e.signature, func(_ model.Hit, _ []float32) error { n++; return nil }); err != nil || n != 0 {
		t.Fatalf("vectors resurrected %d %v", n, err)
	}
	if job, err := idx.ClaimJob(t.Context(), model.Filter{}, e.signature, time.Now); err != nil || job != nil {
		t.Fatalf("job resurrected %+v %v", job, err)
	}
}
func TestRebuildInvalidatesInFlightLease(t *testing.T) {
	e, s, idx, p := searchFixture(t, model.Document{ID: "a", Title: "needle", Body: "needle"})
	p.started = make(chan struct{})
	p.release = make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- e.RunPending(t.Context()) }()
	waitStarted(t, p.started)
	if err := s.WithLock(t.Context(), func() error {
		if err := idx.Reset(t.Context()); err != nil {
			return err
		}
		_, err := idx.Apply(t.Context(), model.Snapshot{Documents: s.docs})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	close(p.release)
	if err := <-done; model.ErrorCode(err) != "indexing" {
		t.Fatalf("old generation job must be rejected: %v", err)
	}
	n := 0
	if err := idx.StreamVectors(context.Background(), model.Filter{}, e.signature, func(model.Hit, []float32) error { n++; return nil }); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("old lease published into rebuilt generation")
	}
	if err := e.RunPending(t.Context()); err != nil {
		t.Fatal(err)
	}
	if p.calls != 2 {
		t.Fatalf("new generation must embed again, calls=%d", p.calls)
	}
}

func TestActiveWorkRenewsLease(t *testing.T) {
	e, _, idx, p := searchFixture(t, model.Document{ID: "a", Title: "needle", Body: "needle"})
	p.started = make(chan struct{})
	p.release = make(chan struct{})
	base := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	renewed := make(chan struct{})
	e.now = func() time.Time {
		n := calls.Add(1)
		if n == 3 {
			close(renewed)
		}
		if n == 1 {
			return base
		}
		return base.Add(50 * time.Second)
	}
	e.renewEvery = time.Millisecond
	done := make(chan error, 1)
	go func() { done <- e.RunPending(t.Context()) }()
	waitStarted(t, p.started)
	waitStarted(t, renewed)
	// The second renewal started, so the first renewal has committed. Without
	// renewal the original lease would be claimable after 60 seconds.
	if j, err := idx.ClaimJob(t.Context(), model.Filter{}, e.signature, func() time.Time { return base.Add(61 * time.Second) }); err != nil || j != nil {
		t.Fatalf("active lease reclaimed: %+v %v", j, err)
	}
	close(p.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRenewalCannotTurnSuccessfulPublicationIntoFailure(t *testing.T) {
	e, _, _, _ := searchFixture(t, model.Document{ID: "a", Title: "needle", Body: "needle"})
	e.renewEvery = time.Nanosecond
	if err := e.RunPending(t.Context()); err != nil {
		t.Fatalf("renewal raced successful publication: %v", err)
	}
}
