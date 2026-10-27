package desktop

import (
	"path/filepath"
	"recall/internal/document"
	"recall/internal/model"
	"strings"
)

func view(d model.Document) *DocumentView {
	return &DocumentView{ID: d.ID, Path: d.Path, Name: strings.TrimSuffix(filepath.Base(d.Path), "."+string(d.Kind)+".md"), Title: d.Title, Kind: string(d.Kind), State: string(d.State), Body: d.Body, Revision: d.Revision}
}
func (s *Service) OpenDocument(session, id string) DocumentReply {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p := s.check(session); p != nil {
		return DocumentReply{Problem: p}
	}
	d, err := s.application.Read(s.ctx, model.Ref{ID: id})
	if err != nil {
		return DocumentReply{Problem: problem(err)}
	}
	return DocumentReply{Document: view(d)}
}
func (s *Service) SaveBody(in SaveBodyInput) SaveBodyReply {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := SaveBodyReply{Generation: in.Generation}
	if p := s.check(in.Session); p != nil {
		r.Problem = p
		return r
	}
	if in.ExpectedRevision == "" || in.Generation > 9007199254740991 {
		r.Problem = invalid("A source revision and safe draft generation are required")
		return r
	}
	current, err := s.application.Read(s.ctx, model.Ref{ID: in.ID})
	if err != nil {
		r.Problem = problem(err)
		return r
	}
	if current.Revision != in.ExpectedRevision {
		r.Problem = &Problem{Code: "conflict", Path: current.Path, Message: "Source changed outside this editor; copy the draft or reload"}
		return r
	}
	parsed, err := document.Parse(current.Path, []byte(current.Raw))
	if err != nil {
		r.Problem = problem(err)
		return r
	}
	parsed.Body = []byte(in.Body)
	raw, err := document.Encode(parsed)
	if err != nil {
		r.Problem = problem(err)
		return r
	}
	d, err := s.application.Update(s.ctx, model.Ref{ID: in.ID}, in.ExpectedRevision, string(raw))
	if err != nil {
		actual, readErr := s.application.ReadSource(s.ctx, current.Path)
		if readErr != nil || actual.Raw != string(raw) {
			r.Problem = problem(err)
			return r
		}
		d = actual
		r.IndexingWarning = problem(err)
	}
	r.Document = view(d)
	r.Saved = true
	return r
}
