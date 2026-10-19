package index

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

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

func TestKeywordPreviewBoundsLongPassages(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"punctuation", "needle" + strings.Repeat("!", 1_000_000) + " tail"},
		{"whitespace", "needle" + strings.Repeat(" \t\r\n", 25_000) + "tail"},
		{"unbroken-token", "needle " + strings.Repeat("x", 100_000)},
		{"metadata-match-unbroken-body", strings.Repeat("x", 100_000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestStore(t)
			d := fixture(t, "a", "a.note.md", "Needle", tc.body)
			apply(t, s, model.Snapshot{Documents: []model.Document{d}})
			hits, err := s.Keyword(t.Context(), model.SearchQuery{Text: "needle"})
			if err != nil || len(hits) != 1 {
				t.Fatalf("keyword result count = %d, err = %v", len(hits), err)
			}
			preview := hits[0].Snippet
			// Literal budget assertions exercise the public query result, not
			// just a clipping helper or the production constant's value.
			if len(preview) > 1024 {
				t.Fatalf("preview has %d bytes, want at most 1024", len(preview))
			}
			want := tc.body[:1021] + "…"
			if !utf8.ValidString(preview) || preview != want {
				t.Fatal("preview must retain its passage prefix and include an in-budget truncation marker")
			}
			var body, raw, revision string
			if err := s.db.QueryRow("SELECT body, raw, revision FROM documents WHERE id = ?", d.ID).Scan(&body, &raw, &revision); err != nil {
				t.Fatal(err)
			}
			if body != d.Body || raw != d.Raw || revision != d.Revision {
				t.Fatal("preview generation changed cached source content or revision")
			}
		})
	}
}

func TestKeywordPreviewUTF8Boundaries(t *testing.T) {
	// A clipped result reserves three of its 1024 bytes for the ellipsis.
	// Exercise every interior byte of two-, three-, and four-byte code points,
	// plus code points ending exactly at each relevant boundary.
	prefix := func(n int) string { return "needle " + strings.Repeat("!", n-7) }
	for _, tc := range []struct {
		name, body, want string
	}{
		{"short", "needle é界🙂", "needle é界🙂"},
		{"invalid-byte-preview-only", "needle \xff text", "needle � text"},
		{"exact-budget-ascii", prefix(1024), prefix(1024)},
		{"over-budget-ascii", prefix(1025), prefix(1021) + "…"},
		{"exact-budget-multibyte", prefix(1020) + "🙂", prefix(1020) + "🙂"},
		{"two-byte-split", prefix(1020) + "é tail", prefix(1020) + "…"},
		{"three-byte-split-1", prefix(1020) + "界 tail", prefix(1020) + "…"},
		{"three-byte-split-2", prefix(1019) + "界 tail", prefix(1019) + "…"},
		{"four-byte-split-1", prefix(1020) + "🙂 tail", prefix(1020) + "…"},
		{"four-byte-split-2", prefix(1019) + "🙂 tail", prefix(1019) + "…"},
		{"four-byte-split-3", prefix(1018) + "🙂 tail", prefix(1018) + "…"},
		{"complete-rune-retained", prefix(1017) + "🙂 tail", prefix(1017) + "🙂…"},
		{"invalid-byte-at-boundary", prefix(1019) + "\xff tail", prefix(1019) + "…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestStore(t)
			d := fixture(t, "a", "a.note.md", "Needle", tc.body)
			apply(t, s, model.Snapshot{Documents: []model.Document{d}})
			hits, err := s.Keyword(t.Context(), model.SearchQuery{Text: "needle"})
			if err != nil || len(hits) != 1 {
				t.Fatalf("keyword result count = %d, err = %v", len(hits), err)
			}
			got := hits[0].Snippet
			if len(got) > 1024 || !utf8.ValidString(got) || got != tc.want {
				t.Fatalf("preview: bytes = %d, valid UTF-8 = %v, matches expected boundary = %v", len(got), utf8.ValidString(got), got == tc.want)
			}
			var body, raw, revision string
			if err := s.db.QueryRow("SELECT body, raw, revision FROM documents WHERE id = ?", d.ID).Scan(&body, &raw, &revision); err != nil {
				t.Fatal(err)
			}
			if body != d.Body || raw != d.Raw || revision != d.Revision {
				t.Fatal("UTF-8 preview repair or clipping changed cached source content or revision")
			}
		})
	}
}
