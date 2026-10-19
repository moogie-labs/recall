package index

import (
	"reflect"
	"strings"
	"testing"

	"recall/internal/model"
)

func TestKeywordFTS5SearchesSourceFields(t *testing.T) {
	s := openTestStore(t)
	d := fixture(t, "a", "commands/unique-path.note.md", "Café titleterm", "first\nidentifier_thing C++ foo-bar \"quoted\"\nlast")
	task := fixture(t, "b", "b.task.md", "Task", "body", model.Event{State: model.InProgress, At: instant("2026-01-01T00:00:00Z")})
	apply(t, s, model.Snapshot{Documents: []model.Document{d, task}})
	for _, text := range []string{"café", "titleterm", "unique-path", "identifier_thing", "C++", "foo-bar", "\"quoted\""} {
		hits, err := s.Keyword(t.Context(), model.SearchQuery{Text: text})
		if err != nil || len(hits) != 1 || hits[0].ID != "a" {
			t.Fatalf("query %q: %+v %v", text, hits, err)
		}
	}
	hits, err := s.Keyword(t.Context(), model.SearchQuery{Text: "identifier_thing"})
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits: %+v %v", hits, err)
	}
	h := hits[0]
	if h.Path != d.Path || h.Title != d.Title || h.Kind != model.Note || h.IndexState != "current" || !strings.Contains(h.Snippet, "identifier_thing") || h.Score <= 0 {
		t.Fatalf("hit metadata: %+v", h)
	}
	for _, text := range []string{"task in_progress", "note"} {
		hits, err := s.Keyword(t.Context(), model.SearchQuery{Text: text})
		if err != nil || len(hits) != 1 {
			t.Fatalf("metadata %q: %+v %v", text, hits, err)
		}
	}
}

func TestKeywordTreatsQueryAsLiteralTerms(t *testing.T) {
	s := openTestStore(t)
	apply(t, s, model.Snapshot{Documents: []model.Document{fixture(t, "literal", "literal.note.md", "Literal", "alpha OR beta state:completed"), fixture(t, "other", "other.note.md", "Other", "alpha gamma")}})
	for _, tc := range []struct {
		text string
		want []string
	}{{"alpha OR beta", []string{"literal"}}, {"state:completed", []string{"literal"}}, {"alpha NOT gamma", []string{}}, {"\" OR 1=1 --", []string{}}, {"*", []string{}}, {"", []string{}}, {"   ", []string{}}} {
		hits, err := s.Keyword(t.Context(), model.SearchQuery{Text: tc.text})
		if err != nil {
			t.Fatalf("query %q: %v", tc.text, err)
		}
		got := []string{}
		for _, h := range hits {
			got = append(got, h.ID)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("literal %q: got %v want %v", tc.text, got, tc.want)
		}
	}
}

func TestKeywordSharesFolderStateAndDateFilters(t *testing.T) {
	s := openTestStore(t)
	apply(t, s, model.Snapshot{Documents: []model.Document{
		fixture(t, "root", "root.note.md", "Same", "needle"),
		fixture(t, "a", "a%_/a.task.md", "Same", "needle", model.Event{State: model.Completed, At: instant("2026-01-01T00:00:00Z")}, model.Event{State: model.Planned, At: instant("2026-02-01T00:00:00Z")}),
		fixture(t, "b", "axx/b.task.md", "Same", "needle"),
	}})
	for _, tc := range []struct {
		f    model.Filter
		want []string
	}{{model.Filter{Folder: ".", Recursive: false}, []string{"root"}}, {model.Filter{Folder: "a%_", Recursive: true}, []string{"a"}}, {model.Filter{States: []model.State{model.Completed}}, []string{}}, {model.Filter{DateField: "transition", EventState: model.Completed}, []string{"a"}}, {model.Filter{Kinds: []model.Kind{model.Note}}, []string{"root"}}} {
		hits, err := s.Keyword(t.Context(), model.SearchQuery{Text: "needle", Filter: tc.f})
		if err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, h := range hits {
			got = append(got, h.ID)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("filter %+v: %v want %v", tc.f, got, tc.want)
		}
	}
}

func TestKeywordLimitsTiesAndStaleText(t *testing.T) {
	s := openTestStore(t)
	var docs []model.Document
	for i := range 105 {
		id := strings.Repeat("x", i+1)
		docs = append(docs, fixture(t, id, id+".note.md", "Same", "limitword"))
	}
	apply(t, s, model.Snapshot{Documents: docs})
	for _, tc := range []struct{ limit, want int }{{0, 20}, {100, 100}} {
		hits, err := s.Keyword(t.Context(), model.SearchQuery{Text: "limitword", Limit: tc.limit})
		if err != nil || len(hits) != tc.want {
			t.Fatalf("limit %d: %d %v", tc.limit, len(hits), err)
		}
		for i, h := range hits {
			if h.ID != strings.Repeat("x", i+1) {
				t.Fatalf("unstable tie at %d: %s", i, h.ID)
			}
		}
	}
	for _, q := range []model.SearchQuery{{Limit: -1}, {Limit: 101}, {Mode: "semantic"}, {Filter: model.Filter{Folder: "../escape"}}} {
		if _, err := s.Keyword(t.Context(), q); model.ErrorCode(err) != "invalid_argument" {
			t.Fatalf("invalid %+v: %v", q, err)
		}
	}
	changed := fixture(t, docs[0].ID, docs[0].Path, "Same", "replacementword")
	apply(t, s, model.Snapshot{Documents: []model.Document{changed}})
	hits, err := s.Keyword(t.Context(), model.SearchQuery{Text: "limitword"})
	if err != nil || len(hits) != 0 {
		t.Fatalf("stale keyword: %+v %v", hits, err)
	}
}
