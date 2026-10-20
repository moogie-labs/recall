package app

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"recall/internal/document"
	"recall/internal/model"
)

func TestStateOperationsAndSourceHistoryReplacement(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	ctx := t.Context()
	d := mustCreate(t, a, CreateInput{Kind: model.Task, Name: "work", Body: "details"})
	unchanged, err := a.SetState(ctx, model.Ref{ID: d.ID}, model.Planned, d.Revision)
	if err != nil || unchanged.Raw != d.Raw || len(unchanged.History) != 1 {
		t.Fatalf("no-op: %+v %v", unchanged, err)
	}
	changed, err := a.SetState(ctx, model.Ref{ID: d.ID}, model.InProgress, "")
	if err != nil || len(changed.History) != 2 || changed.History[1].At.Location() != time.UTC {
		t.Fatalf("transition: %+v %v", changed, err)
	}
	_, err = a.SetState(ctx, model.Ref{ID: d.ID}, model.Completed, d.Revision)
	wantCode(t, err, "conflict")
	_, err = a.SetState(ctx, model.Ref{ID: d.ID}, "bad", "")
	wantCode(t, err, "invalid_argument")
	n := mustCreate(t, a, CreateInput{Kind: model.Note, Name: "note"})
	_, err = a.SetState(ctx, model.Ref{ID: n.ID}, model.Completed, "")
	wantCode(t, err, "type_mismatch")
	// A direct edit replaces history; cached events must not be merged back in.
	raw := "---\nid: " + d.ID + "\ntype: task\nstate: stalled\nstate_history:\n  - state: stalled\n    at: 2026-10-01T01:00:00Z\n---\nlatest"
	if err := os.WriteFile(filepath.Join(cfg.Content, d.Path), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	page, err := a.Tasks(ctx, model.TaskQuery{Filter: model.Filter{States: []model.State{model.Stalled}}})
	if err != nil || len(page.Items) != 1 || len(page.Items[0].History) != 1 || page.Items[0].Body != "latest" {
		t.Fatalf("task refresh: %+v %v", page, err)
	}
	_, err = a.Tasks(ctx, model.TaskQuery{Limit: 201})
	wantCode(t, err, "invalid_argument")
	_, err = a.Tasks(ctx, model.TaskQuery{Filter: model.Filter{Folder: "../escape"}})
	wantCode(t, err, "invalid_argument")
}

func TestStatePreservesSourceBodyAndMetadataReferences(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	a.now = func() time.Time { return time.Date(2026, 10, 19, 12, 30, 0, 0, time.FixedZone("east", 2*60*60)) }
	body := "自由\r\n---\r\nuntouched\x00\r\n"
	raw := "---\r\nid: &identity task-id\r\ncopy: *identity\r\ntype: task\r\nstate: &status planned\r\nstatus_copy: *status\r\nextra: {at: 2026-10-01T00:00:00Z, enabled: true, number: 42}\r\nstate_history:\r\n  - state: planned\r\n    at: 2026-10-01T00:00:00Z\r\n---\r\n" + body
	if err := os.WriteFile(filepath.Join(cfg.Content, "references.task.md"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	d, err := a.SetState(t.Context(), model.Ref{ID: "task-id"}, model.InProgress, "")
	if err != nil || d.Body != body || len(d.History) != 2 || !d.History[1].At.Equal(time.Date(2026, 10, 19, 10, 30, 0, 0, time.UTC)) {
		t.Fatalf("preservation/UTC: %+v %v", d, err)
	}
	p, err := document.Parse(d.Path, []byte(d.Raw))
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := p.Metadata.Decode(&metadata); err != nil {
		t.Fatal(err)
	}
	extra, ok := metadata["extra"].(map[string]any)
	if !ok || metadata["copy"] != "task-id" || metadata["status_copy"] != "in_progress" || extra["enabled"] != true || extra["number"] != 42 {
		t.Fatalf("metadata changed: %#v", metadata)
	}
	if _, ok := extra["at"].(time.Time); !ok {
		t.Fatalf("timestamp scalar semantics changed: %#v", extra)
	}
}

func TestTaskDeleteIsPermanent(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	ctx := t.Context()
	n := mustCreate(t, a, CreateInput{Kind: model.Note, Name: "keep", Body: "unchanged\r\n---\r\n"})
	d := mustCreate(t, a, CreateInput{Kind: model.Task, Name: "work", Body: "unique-deletion-term"})
	d, err := a.SetState(ctx, model.Ref{ID: d.ID}, model.Completed, "")
	if err != nil {
		t.Fatal(err)
	}
	installProvider(a, &appProvider{})
	if err := a.IndexPending(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(ctx, model.Ref{ID: d.ID}, d.Revision); err != nil {
		t.Fatal(err)
	}
	// Check live cleanup before any auto-refreshing app read can repair it.
	db, err := sql.Open("sqlite", cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"documents", "state_events", "document_fts", "chunks", "embeddings", "embedding_jobs"} {
		column := "document_id"
		if table == "documents" || table == "document_fts" {
			column = "id"
		}
		var count int
		if err := db.QueryRow("SELECT count(*) FROM "+table+" WHERE "+column+" = ?", d.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s remains: %d %v", table, count, err)
		}
	}
	hits, err := a.index.Keyword(ctx, model.SearchQuery{Text: "unique-deletion-term"})
	if err != nil || len(hits) != 0 {
		t.Fatalf("deleted FTS: %+v %v", hits, err)
	}
	_, err = a.Read(ctx, model.Ref{ID: d.ID})
	wantCode(t, err, "not_found")
	page, err := a.Tasks(ctx, model.TaskQuery{})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("deleted task indexed: %+v %v", page, err)
	}
	if err := filepath.WalkDir(cfg.Content, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != cfg.Content && (entry.IsDir() || entry.Name() != "keep.note.md") {
			t.Fatalf("unexpected deletion artifact: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(cfg.Content, n.Path))
	if err != nil || string(got) != n.Raw || strings.Contains(string(got), d.ID) {
		t.Fatalf("note changed: %v", err)
	}
}
