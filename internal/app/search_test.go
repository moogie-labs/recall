package app

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"recall/internal/model"
	"recall/internal/search"
)

type appProvider struct {
	calls                      atomic.Int32
	started, release           chan struct{}
	queryStarted, queryRelease chan struct{}
}

func (p *appProvider) Tokenize(_ context.Context, s string) ([]int, error) {
	return make([]int, len(strings.Fields(s))+2), nil
}
func (p *appProvider) Embed(ctx context.Context, purpose string, texts []string) ([][]float32, error) {
	if purpose == "query" && p.queryStarted != nil {
		close(p.queryStarted)
		select {
		case <-p.queryRelease:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if purpose == "document" {
		p.calls.Add(int32(len(texts)))
		if p.started != nil {
			close(p.started)
			select {
			case <-p.release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	v := make([][]float32, len(texts))
	for i := range v {
		v[i] = make([]float32, 768)
		v[i][0] = 1
	}
	return v, nil
}
func installProvider(a *App, p *appProvider) {
	a.search = search.New(search.NewStoreSource(a.store), a.index, p)
}
func TestStateChangeDoesNotReembedBody(t *testing.T) {
	a, _ := newTestApp(t)
	defer a.Close()
	p := &appProvider{}
	installProvider(a, p)
	d := mustCreate(t, a, CreateInput{Kind: model.Task, Name: "work", Body: "original body"})
	if p.calls.Load() != 0 {
		t.Fatal("one-shot write ran inference")
	}
	if err := a.IndexPending(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := p.calls.Load()
	changed, err := a.SetState(t.Context(), model.Ref{ID: d.ID}, model.Completed, d.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Move(t.Context(), model.Ref{ID: d.ID}, "moved.task.md", changed.Revision); err != nil {
		t.Fatal(err)
	}
	if err = a.IndexPending(t.Context()); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != before {
		t.Fatal("metadata-only edit re-embedded")
	}
	hits, err := a.Search(t.Context(), model.SearchQuery{Text: "concept", Mode: "semantic"})
	if err != nil || len(hits) != 1 || hits[0].Path != "moved.task.md" || hits[0].State != model.Completed {
		t.Fatalf("%+v %v", hits, err)
	}
	line := 1 + strings.Count(changed.Raw[:len(changed.Raw)-len(changed.Body)], "\n")
	if hits[0].StartLine != line || hits[0].EndLine != line {
		t.Fatalf("metadata edit left stale source lines: %+v want %d", hits[0], line)
	}
	if report, err := a.Rebuild(t.Context()); err != nil || report.Added != 1 {
		t.Fatalf("%+v %v", report, err)
	}
	if err = a.IndexPending(t.Context()); err != nil || p.calls.Load() != before+1 {
		t.Fatalf("rebuild %v calls=%d", err, p.calls.Load())
	}
}
func TestStoreSourceDeletionDuringEmbeddingAndLiveCleanup(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	p := &appProvider{started: make(chan struct{}), release: make(chan struct{})}
	installProvider(a, p)
	d := mustCreate(t, a, CreateInput{Kind: model.Note, Name: "work", Body: "needle"})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.IndexPending(ctx) }()
	select {
	case <-p.started:
	case <-ctx.Done():
		t.Fatal("inference did not start")
	}
	// This mutation would deadlock if the engine held the source lock in Embed.
	if err := a.Delete(ctx, model.Ref{ID: d.ID}, d.Revision); err != nil {
		t.Fatal(err)
	}
	// Inspect live derived rows BEFORE invoking any auto-refreshing app query.
	db, err := sql.Open("sqlite", cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"documents", "document_fts", "chunks", "embeddings", "embedding_jobs"} {
		var n int
		if err = db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("live %s=%d %v", table, n, err)
		}
	}
	close(p.release)
	if err = <-done; err != nil && model.ErrorCode(err) != "indexing" {
		t.Fatal(err)
	}
	for _, table := range []string{"documents", "document_fts", "chunks", "embeddings", "embedding_jobs"} {
		var n int
		if err = db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("resurrected %s=%d %v", table, n, err)
		}
	}
}
func TestExternalBodyChangeCannotPublishOldEmbedding(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	p := &appProvider{started: make(chan struct{}), release: make(chan struct{})}
	installProvider(a, p)
	d := mustCreate(t, a, CreateInput{Kind: model.Note, Name: "work", Body: "old body"})
	done := make(chan error, 1)
	go func() { done <- a.IndexPending(t.Context()) }()
	<-p.started
	if err := os.WriteFile(filepath.Join(cfg.Content, d.Path), []byte(strings.Replace(d.Raw, "old body", "new body", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	close(p.release)
	if err := <-done; model.ErrorCode(err) != "indexing" {
		t.Fatalf("stale source publication: %v", err)
	}
}

func TestMoveDuringEmbeddingReusesVector(t *testing.T) {
	a, _ := newTestApp(t)
	defer a.Close()
	p := &appProvider{started: make(chan struct{}), release: make(chan struct{})}
	installProvider(a, p)
	d := mustCreate(t, a, CreateInput{Kind: model.Task, Name: "work", Body: "body"})
	done := make(chan error, 1)
	go func() { done <- a.IndexPending(t.Context()) }()
	<-p.started
	if _, err := a.Move(t.Context(), model.Ref{ID: d.ID}, "moved.task.md", d.Revision); err != nil {
		t.Fatal(err)
	}
	close(p.release)
	if err := <-done; err != nil {
		t.Fatalf("metadata-only move invalidated active input: %v", err)
	}
	if p.calls.Load() != 1 {
		t.Fatal("move re-embedded unchanged input")
	}
}

func TestSourceChangedDuringQueryInferenceIsNotCurrentHit(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	p := &appProvider{queryStarted: make(chan struct{}), queryRelease: make(chan struct{})}
	installProvider(a, p)
	d := mustCreate(t, a, CreateInput{Kind: model.Note, Name: "work", Body: "old body"})
	if err := a.IndexPending(t.Context()); err != nil {
		t.Fatal(err)
	}
	type result struct {
		hits []model.Hit
		err  error
	}
	done := make(chan result, 1)
	go func() {
		h, err := a.Search(t.Context(), model.SearchQuery{Text: "concept", Mode: "semantic"})
		done <- result{h, err}
	}()
	<-p.queryStarted
	if err := os.WriteFile(filepath.Join(cfg.Content, d.Path), []byte(strings.Replace(d.Raw, "old body", "new body", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	close(p.queryRelease)
	if got := <-done; model.ErrorCode(got.err) != "indexing" || got.hits != nil {
		t.Fatalf("stale current search result: %+v %v", got.hits, got.err)
	}
}

func TestBodyEditInvalidatesOldSemanticRowsUntilDrained(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	p := &appProvider{}
	installProvider(a, p)
	d := mustCreate(t, a, CreateInput{Kind: model.Note, Name: "work", Body: "before"})
	if err := a.IndexPending(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Update(t.Context(), model.Ref{ID: d.ID}, d.Revision, strings.Replace(d.Raw, "before", "after", 1)); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 1 {
		t.Fatal("source update waited for inference")
	}
	db, err := sql.Open("sqlite", cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tc := range []struct {
		table string
		want  int
	}{{"chunks", 0}, {"embeddings", 0}, {"embedding_jobs", 1}} {
		var n int
		if err = db.QueryRow("SELECT count(*) FROM " + tc.table).Scan(&n); err != nil || n != tc.want {
			t.Fatalf("%s=%d want %d: %v", tc.table, n, tc.want, err)
		}
	}
	h, err := a.Search(t.Context(), model.SearchQuery{Text: "after", Mode: "keyword"})
	if err != nil || len(h) != 1 || p.calls.Load() != 1 {
		t.Fatalf("pending keyword %+v %v", h, err)
	}
	h, err = a.Search(t.Context(), model.SearchQuery{Text: "concept", Mode: "semantic"})
	if err != nil || len(h) != 1 || h[0].Snippet != "after" || h[0].IndexState != "current" || p.calls.Load() != 2 {
		t.Fatalf("current semantic %+v %v calls=%d", h, err, p.calls.Load())
	}
}
