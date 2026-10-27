package desktop

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"recall/internal/app"
	"recall/internal/config"
	"recall/internal/model"
)

func fixture(t *testing.T) (*Service, *app.App, config.Config, string) {
	t.Helper()
	cfg, err := config.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(func(context.Context, config.Config) (Application, error) { return a, nil }, nil)
	r := s.OpenCollection(cfg.Root)
	if r.Problem != nil {
		t.Fatal(r.Problem)
	}
	t.Cleanup(func() { s.Close() })
	return s, a, cfg, r.Session
}

func TestBodySavePreservesHeaderAndID(t *testing.T) {
	for _, nl := range []string{"\n", "\r\n"} {
		t.Run(strings.ReplaceAll(nl, "\n", "LF"), func(t *testing.T) {
			s, a, cfg, session := fixture(t)
			raw := strings.ReplaceAll("---\nid: stable\ntype: task\ntitle: Title\nstate: planned\nstate_history: [{state: planned, at: 2026-10-20T00:00:00Z}]\n# keep me\ncustom: {x: yes}\n---\nold\n", "\n", nl)
			if err := os.WriteFile(filepath.Join(cfg.Content, "test.task.md"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			doc := s.OpenDocument(session, "stable")
			if doc.Problem != nil {
				t.Fatal(doc.Problem)
			}
			before, err := a.Read(t.Context(), model.Ref{ID: "stable"})
			if err != nil {
				t.Fatal(err)
			}
			for _, revision := range []string{"", "stale"} {
				r := s.SaveBody(SaveBodyInput{Session: session, ID: "stable", ExpectedRevision: revision, Body: "bad"})
				if r.Saved || r.Problem == nil {
					t.Fatal("invalid revision accepted")
				}
			}
			r := s.SaveBody(SaveBodyInput{Session: session, ID: "stable", ExpectedRevision: doc.Document.Revision, Body: "new 自由" + nl, Generation: 2})
			if !r.Saved || r.Problem != nil || r.Generation != 2 {
				t.Fatalf("save %+v", r)
			}
			disk, err := os.ReadFile(filepath.Join(cfg.Content, "test.task.md"))
			if err != nil {
				t.Fatal(err)
			}
			want := before.Raw[:len(before.Raw)-len(before.Body)] + "new 自由" + nl
			if string(disk) != want || r.Document.ID != "stable" || r.Document.Revision == doc.Document.Revision {
				t.Fatalf("metadata/source changed: %s", disk)
			}
		})
	}
}

type indexFailure struct {
	Application
	mode string
}

func (f *indexFailure) Update(ctx context.Context, ref model.Ref, revision, raw string) (model.Document, error) {
	if f.mode == "no-write" {
		return model.Document{}, errors.New("write failed")
	}
	d, err := f.Application.Update(ctx, ref, revision, raw)
	if err != nil {
		return d, err
	}
	return d, errors.New("index failed after source write")
}
func TestPostWriteIndexFailureAcknowledgesExactSource(t *testing.T) {
	for _, mode := range []string{"written", "no-write"} {
		t.Run(mode, func(t *testing.T) {
			s, a, cfg, _ := fixture(t)
			d, err := a.Create(t.Context(), app.CreateInput{Kind: model.Note, Name: "one", Body: "old"})
			if err != nil {
				t.Fatal(err)
			}
			// Reopen the same underlying app through an adapter that fails after source commit.
			s.open = func(context.Context, config.Config) (Application, error) { return &indexFailure{a, mode}, nil }
			s.mu.Lock()
			s.application = nil
			s.mu.Unlock()
			c := s.OpenCollection(cfg.Root)
			r := s.SaveBody(SaveBodyInput{Session: c.Session, ID: d.ID, ExpectedRevision: d.Revision, Body: "new"})
			if mode == "written" {
				if !r.Saved || r.IndexingWarning == nil || r.Problem != nil || r.Document.Body != "new" {
					t.Fatalf("written %+v", r)
				}
			} else if r.Saved || r.Problem == nil {
				t.Fatalf("unsaved %+v", r)
			}
		})
	}
}

func TestStaleSessionCannotWrite(t *testing.T) {
	s, a, cfg, old := fixture(t)
	d, err := a.Create(t.Context(), app.CreateInput{Kind: model.Note, Name: "one", Body: "old"})
	if err != nil {
		t.Fatal(err)
	}
	// Even a new session at the same root invalidates old renderer requests.
	s.mu.Lock()
	s.application = nil
	s.mu.Unlock()
	current := s.OpenCollection(cfg.Root)
	r := s.SaveBody(SaveBodyInput{Session: old, ID: d.ID, ExpectedRevision: d.Revision, Body: "bad"})
	if r.Saved || r.Problem == nil || r.Problem.Code != "conflict" {
		t.Fatal("stale session accepted")
	}
	if s.OpenDocument(current.Session, "unknown").Problem == nil {
		t.Fatal("unknown document accepted")
	}
	disk, _ := os.ReadFile(filepath.Join(cfg.Content, d.Path))
	if !strings.HasSuffix(string(disk), "old") {
		t.Fatal("stale source changed")
	}
}
func TestOpenDoesNotInitializeOrLoadModel(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	s := NewService(func(ctx context.Context, cfg config.Config) (Application, error) { return app.Open(ctx, cfg) }, nil)
	defer s.Close()
	r := s.OpenCollection(root)
	if r.Problem == nil || r.Problem.Code != "not_initialized" {
		t.Fatalf("open %+v", r)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("implicit initialization")
	}
}
func TestPersistentPostWriteIndexFailureAcknowledgesSourceAndRevision(t *testing.T) {
	s, a, cfg, session := fixture(t)
	d, err := a.Create(t.Context(), app.CreateInput{Kind: model.Note, Name: "persist", Body: "old"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(cfg.Root, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TRIGGER desktop_fail_projection BEFORE INSERT ON documents WHEN NEW.body = 'written despite index failure' BEGIN SELECT RAISE(FAIL, 'desktop injected projection failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	r := s.SaveBody(SaveBodyInput{Session: session, ID: d.ID, ExpectedRevision: d.Revision, Body: "written despite index failure", Generation: 1})
	disk, err := os.ReadFile(filepath.Join(cfg.Content, d.Path))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(disk), "written despite index failure") {
		t.Fatal("test did not reach the source commit")
	}
	if !r.Saved || r.Document == nil || r.Problem != nil || r.IndexingWarning == nil || r.Document.Revision == d.Revision {
		t.Fatalf("committed source was stranded as unsaved: %+v", r)
	}
	if _, err = db.Exec(`DROP TRIGGER desktop_fail_projection`); err != nil {
		t.Fatal(err)
	}
	retry := s.SaveBody(SaveBodyInput{Session: session, ID: d.ID, ExpectedRevision: r.Document.Revision, Body: "next edit", Generation: 2})
	if !retry.Saved || retry.Problem != nil {
		t.Fatalf("repair produced a false conflict: %+v", retry)
	}
}
