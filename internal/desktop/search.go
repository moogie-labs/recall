package desktop

import (
	"recall/internal/model"
	"strings"
)

func (s *Service) Search(in SearchInput) SearchReply {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := SearchReply{Hits: []SearchHit{}}
	if p := s.check(in.Session); p != nil {
		r.Problem = p
		return r
	}
	if in.Mode == "" {
		in.Mode = "hybrid"
	}
	if in.Mode != "hybrid" && in.Mode != "keyword" {
		r.Problem = invalid("Search mode must be hybrid or keyword")
		return r
	}
	if strings.TrimSpace(in.Query) == "" {
		return r
	}
	if in.Mode == "hybrid" {
		if p := s.semanticUnavailable(in.Session); p != nil {
			r.Problem = p
			return r
		}
	}
	if in.Limit <= 0 {
		in.Limit = 20
	}
	if in.Limit > 50 {
		in.Limit = 50
	}
	hits, err := s.application.Search(s.ctx, model.SearchQuery{Text: in.Query, Mode: in.Mode, Limit: in.Limit})
	if err != nil {
		r.Problem = problem(err)
		return r
	}
	for _, h := range hits {
		r.Hits = append(r.Hits, SearchHit{ID: h.ID, Path: h.Path, Title: h.Title, Snippet: h.Snippet, Kind: string(h.Kind), State: string(h.State)})
	}
	return r
}
