package desktop

import (
	"context"
	"recall/internal/app"
	"recall/internal/config"
	"recall/internal/model"
	"testing"
)

type searchCapture struct {
	Application
	queries []model.SearchQuery
}

func (a *searchCapture) Search(ctx context.Context, q model.SearchQuery) ([]model.Hit, error) {
	a.queries = append(a.queries, q)
	if q.Mode == "hybrid" {
		return nil, &model.Error{Code: "runtime_unavailable", Message: "Inference unavailable"}
	}
	return a.Application.Search(ctx, q)
}
func TestSearchDelegatesModesAndErrors(t *testing.T) {
	s, a, cfg, _ := fixture(t)
	a.Create(t.Context(), app.CreateInput{Kind: model.Note, Name: "find", Body: "needle"})
	capture := &searchCapture{Application: a}
	s.open = func(context.Context, config.Config) (Application, error) { return capture, nil }
	s.mu.Lock()
	s.application = nil
	s.mu.Unlock()
	session := s.OpenCollection(cfg.Root).Session
	r := s.Search(SearchInput{Session: session, Query: "needle"})
	if r.Problem == nil || r.Problem.Code != "runtime_unavailable" || len(capture.queries) != 1 || capture.queries[0].Mode != "hybrid" {
		t.Fatalf("silent fallback %+v %+v", r, capture.queries)
	}
	r = s.Search(SearchInput{Session: session, Query: "needle", Mode: "keyword", Limit: 100})
	if r.Problem != nil || len(r.Hits) != 1 || capture.queries[1].Limit != 50 {
		t.Fatalf("keyword %+v", r)
	}
	if s.Search(SearchInput{Session: "stale", Query: "needle"}).Problem == nil {
		t.Fatal("stale search accepted")
	}
	if s.Search(SearchInput{Session: session, Query: "needle", Mode: "other"}).Problem == nil {
		t.Fatal("invalid mode accepted")
	}
}
