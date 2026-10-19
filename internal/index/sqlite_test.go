package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"recall/internal/document"
	"recall/internal/model"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func fixture(t *testing.T, id, path, title, body string, events ...model.Event) model.Document {
	t.Helper()
	kind, err := document.KindFromPath(path)
	if err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf("---\nid: %q\ntype: %s\ntitle: %q\ncustom: retained\n", id, kind, title)
	if kind == model.Task {
		if len(events) == 0 {
			events = []model.Event{{State: model.Planned, At: instant("2026-01-01T00:00:00Z")}}
		}
		raw += fmt.Sprintf("state: %s\nstate_history:\n", events[len(events)-1].State)
		for _, event := range events {
			raw += fmt.Sprintf("  - state: %s\n    at: %s\n", event.State, event.At.Format(time.RFC3339Nano))
		}
	}
	p, err := document.Parse(path, []byte(raw+"---\n"+body))
	if err != nil {
		t.Fatal(err)
	}
	return p.Document
}

func instant(s string) time.Time {
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return v
}

func apply(t *testing.T, s *Store, snapshot model.Snapshot) model.SyncReport {
	t.Helper()
	r, err := s.Apply(t.Context(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func taskIDs(t *testing.T, s *Store, q model.TaskQuery) []string {
	t.Helper()
	p, err := s.Tasks(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, d := range p.Items {
		ids = append(ids, d.ID)
	}
	return ids
}

func TestEmptyIndexTaskPage(t *testing.T) {
	s := openTestStore(t)
	got, err := s.Tasks(t.Context(), model.TaskQuery{Limit: 50})
	if err != nil || len(got.Items) != 0 || got.NextOffset != nil {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestApplyRebuildRoundTripAndIdempotence(t *testing.T) {
	s := openTestStore(t)
	d := fixture(t, "task", "a/task.task.md", "Unicode α", "First\r\nSecond\n", model.Event{State: model.Planned, At: instant("2026-01-01T00:00:00.123456789Z")}, model.Event{State: model.Completed, At: instant("2026-02-01T00:00:00Z")})
	n := fixture(t, "note", "note.note.md", "Note", "rebuild keyword")
	snapshot := model.Snapshot{Documents: []model.Document{d, n}, Diagnostics: []model.Diagnostic{{Code: "invalid_document", Path: "bad.task.md", Message: "invalid metadata"}}}
	first := apply(t, s, snapshot)
	if first.Added != 2 || first.Updated != 0 || first.Removed != 0 || !reflect.DeepEqual(first.Diagnostics, snapshot.Diagnostics) {
		t.Fatalf("report: %+v", first)
	}
	second := apply(t, s, snapshot)
	if second.Added != 0 || second.Updated != 0 || second.Removed != 0 {
		t.Fatalf("repeat report: %+v", second)
	}
	p, err := s.Tasks(t.Context(), model.TaskQuery{})
	if err != nil || len(p.Items) != 1 || !reflect.DeepEqual(p.Items[0], d) {
		t.Fatalf("round trip: %+v, %v", p, err)
	}
	rebuilt := openTestStore(t)
	apply(t, rebuilt, snapshot)
	other, err := rebuilt.Tasks(t.Context(), model.TaskQuery{})
	if err != nil || !reflect.DeepEqual(p, other) {
		t.Fatalf("rebuild differs: %+v %v", other, err)
	}
	hits, err := s.Keyword(t.Context(), model.SearchQuery{Text: "rebuild"})
	otherHits, otherErr := rebuilt.Keyword(t.Context(), model.SearchQuery{Text: "rebuild"})
	if err != nil || otherErr != nil || len(hits) != 1 || !reflect.DeepEqual(hits, otherHits) {
		t.Fatalf("FTS rebuild: %+v %+v %v %v", hits, otherHits, err, otherErr)
	}
}

func TestApplyRemovesInvalidDuplicateAndAbsentDocuments(t *testing.T) {
	s := openTestStore(t)
	a := fixture(t, "a", "a.task.md", "A", "vanishing")
	b := fixture(t, "b", "b.task.md", "B", "vanishing")
	c := fixture(t, "c", "c.task.md", "C", "vanishing")
	apply(t, s, model.Snapshot{Documents: []model.Document{a, b, c}})
	// Scanner omits invalid and duplicate documents; a missing source is absent too.
	diagnostics := []model.Diagnostic{{Code: "invalid_document", Path: a.Path}, {Code: "duplicate_id", Path: b.Path}, {Code: "duplicate_id", Path: "copy.task.md"}}
	r := apply(t, s, model.Snapshot{Diagnostics: diagnostics})
	if r.Removed != 3 || !reflect.DeepEqual(r.Diagnostics, diagnostics) {
		t.Fatalf("removal report: %+v", r)
	}
	if got := taskIDs(t, s, model.TaskQuery{}); len(got) != 0 {
		t.Fatalf("stale tasks: %v", got)
	}
	hits, err := s.Keyword(t.Context(), model.SearchQuery{Text: "vanishing"})
	if err != nil || len(hits) != 0 {
		t.Fatalf("stale FTS: %+v %v", hits, err)
	}
	for _, table := range []string{"documents", "state_events", "document_fts"} {
		var count int
		if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("stale %s: %d %v", table, count, err)
		}
	}
	apply(t, s, model.Snapshot{})
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM diagnostics").Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale diagnostics: %d %v", count, err)
	}
}

func TestApplyMovesReplacementAndSeparateFingerprints(t *testing.T) {
	s := openTestStore(t)
	a := fixture(t, "a", "a.task.md", "Same", "content")
	b := fixture(t, "b", "b.task.md", "Other", "other")
	apply(t, s, model.Snapshot{Documents: []model.Document{a, b}})
	var revision, before string
	if err := s.db.QueryRow("SELECT revision, semantic_fingerprint FROM documents WHERE id = ?", "a").Scan(&revision, &before); err != nil || revision != a.Revision || before == "" {
		t.Fatalf("fingerprint: %s %s %v", revision, before, err)
	}
	a.Path, b.Path = b.Path, a.Path
	a.Raw = strings.Replace(a.Raw, "custom: retained", "custom: changed", 1)
	p, err := document.Parse(a.Path, []byte(a.Raw))
	if err != nil {
		t.Fatal(err)
	}
	a = p.Document
	r := apply(t, s, model.Snapshot{Documents: []model.Document{a, b}})
	if r.Updated != 2 {
		t.Fatalf("move report: %+v", r)
	}
	var after string
	if err := s.db.QueryRow("SELECT revision, semantic_fingerprint FROM documents WHERE id = ?", "a").Scan(&revision, &after); err != nil || revision != a.Revision || before != after {
		t.Fatalf("metadata/move rehashed semantic input: %s %s %v", before, after, err)
	}
	a = fixture(t, "a", a.Path, "Same", "changed content")
	apply(t, s, model.Snapshot{Documents: []model.Document{a, b}})
	if err := s.db.QueryRow("SELECT semantic_fingerprint FROM documents WHERE id = ?", "a").Scan(&after); err != nil || before == after {
		t.Fatalf("body change not fingerprinted: %s %v", after, err)
	}
	replacement := fixture(t, "new", b.Path, "Replacement", "replacement")
	r = apply(t, s, model.Snapshot{Documents: []model.Document{a, replacement}})
	if r.Added != 1 || r.Removed != 1 {
		t.Fatalf("replacement report: %+v", r)
	}
}

func TestSemanticFingerprintPreservesInputBytesAndBoundaries(t *testing.T) {
	s := openTestStore(t)
	var previous string
	for _, input := range []struct{ title, body string }{
		{"Same", "content\x80"}, {"Same", "content\x81"},
		{"ab", "c"}, {"a", "bc"}, {"changed title", "bc"},
	} {
		d := fixture(t, "a", "a.note.md", input.title, input.body)
		apply(t, s, model.Snapshot{Documents: []model.Document{d}})
		var fingerprint string
		if err := s.db.QueryRow("SELECT semantic_fingerprint FROM documents WHERE id = ?", "a").Scan(&fingerprint); err != nil {
			t.Fatal(err)
		}
		if fingerprint == previous {
			t.Fatal("distinct semantic input bytes have the same fingerprint")
		}
		previous = fingerprint
	}
}

func TestTwoConnectionsAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	d := fixture(t, "a", "a.task.md", "A", "concurrent")
	snapshot := model.Snapshot{Documents: []model.Document{d}}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, s := range []*Store{a, b} {
		wg.Go(func() {
			for range 15 {
				if _, err := s.Apply(t.Context(), snapshot); err != nil {
					errs <- err
					return
				}
				if _, err := s.Tasks(t.Context(), model.TaskQuery{}); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := taskIDs(t, c, model.TaskQuery{}); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("reopen: %v", got)
	}
}

func TestSQLiteConfigurationAndAtomicFailure(t *testing.T) {
	s := openTestStore(t)
	for pragma, want := range map[string]string{"journal_mode": "wal", "foreign_keys": "1", "busy_timeout": "5000"} {
		var got string
		if err := s.db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: %s %v", pragma, got, err)
		}
	}
	d := fixture(t, "a", "a.task.md", "A", "kept")
	apply(t, s, model.Snapshot{Documents: []model.Document{d}})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Apply(ctx, model.Snapshot{}); err == nil {
		t.Fatal("cancelled apply succeeded")
	}
	if got := taskIDs(t, s, model.TaskQuery{}); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("partial apply: %v", got)
	}
	// Fail after document/history/FTS updates, proving all tables roll back
	// together and driver errors cannot expose document content.
	if _, err := s.db.Exec(`CREATE TRIGGER fail_diagnostic BEFORE INSERT ON diagnostics BEGIN SELECT RAISE(ABORT, 'private source content'); END`); err != nil {
		t.Fatal(err)
	}
	newDoc := fixture(t, "b", "b.task.md", "B", "replacement")
	_, err := s.Apply(t.Context(), model.Snapshot{Documents: []model.Document{newDoc}, Diagnostics: []model.Diagnostic{{Code: "invalid_document", Path: "bad.task.md"}}})
	if model.ErrorCode(err) != "index_unavailable" || strings.Contains(err.Error(), "private source content") {
		t.Fatalf("failure: %v", err)
	}
	p, err := s.Tasks(t.Context(), model.TaskQuery{})
	if err != nil || len(p.Items) != 1 || !reflect.DeepEqual(p.Items[0], d) {
		t.Fatalf("partially published metadata/history: %+v %v", p, err)
	}
	hits, err := s.Keyword(t.Context(), model.SearchQuery{Text: "kept"})
	if err != nil || len(hits) != 1 || hits[0].ID != "a" {
		t.Fatalf("partially published FTS: %+v %v", hits, err)
	}
}

func TestDeleteDatabaseAndRebuildSamePath(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "space # % ?.sqlite")
	s, err := Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	d := fixture(t, "a", "a.task.md", "A", "rebuildword")
	snapshot := model.Snapshot{Documents: []model.Document{d}}
	apply(t, s, snapshot)
	before, err := s.Tasks(t.Context(), model.TaskQuery{})
	if err != nil {
		t.Fatal(err)
	}
	beforeHits, err := s.Keyword(t.Context(), model.SearchQuery{Text: "rebuildword"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filename); err != nil {
		t.Fatal(err)
	}
	s, err = Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	apply(t, s, snapshot)
	after, err := s.Tasks(t.Context(), model.TaskQuery{})
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("rebuild metadata/history: %+v %v", after, err)
	}
	afterHits, err := s.Keyword(t.Context(), model.SearchQuery{Text: "rebuildword"})
	if err != nil || !reflect.DeepEqual(beforeHits, afterHits) {
		t.Fatalf("rebuild keywords: %+v %v", afterHits, err)
	}
}

func TestWALReaderCannotSeeUncommittedMetadataOrKeywords(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "index.sqlite")
	a, err := Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	d := fixture(t, "a", "a.task.md", "A", "publishedword")
	apply(t, a, model.Snapshot{Documents: []model.Document{d}})
	conn, err := a.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	if _, err := conn.ExecContext(t.Context(), "UPDATE documents SET body = ? WHERE id = ?", "uncommittedword", d.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	p, err := b.Tasks(ctx, model.TaskQuery{})
	if err != nil || len(p.Items) != 1 || !reflect.DeepEqual(p.Items[0], d) {
		t.Fatalf("reader blocked or dirty metadata: %+v %v", p, err)
	}
	hits, err := b.Keyword(ctx, model.SearchQuery{Text: "publishedword"})
	if err != nil || len(hits) != 1 {
		t.Fatalf("reader blocked or dirty FTS: %+v %v", hits, err)
	}
}
