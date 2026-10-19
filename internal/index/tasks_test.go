package index

import (
	"fmt"
	"reflect"
	"testing"

	"recall/internal/model"
	"recall/internal/tasks"
)

func TestTaskFolderScopesAreLiteral(t *testing.T) {
	s := openTestStore(t)
	paths := []string{"root.task.md", "a/direct.task.md", "a/sub/deep.task.md", "ab/other.task.md", "a%_/literal.task.md", "axx/not-literal.task.md", "quote'folder/literal.task.md"}
	var docs []model.Document
	for i, p := range paths {
		docs = append(docs, fixture(t, fmt.Sprint(i), p, "Same", "body"))
	}
	apply(t, s, model.Snapshot{Documents: docs})
	for _, tc := range []struct {
		name, folder string
		recursive    bool
		want         []string
	}{
		{"global", "", false, []string{"0", "1", "2", "3", "4", "5", "6"}},
		{"global-recursive", "", true, []string{"0", "1", "2", "3", "4", "5", "6"}},
		{"explicit-root", ".", false, []string{"0"}},
		{"root-subtree", ".", true, []string{"0", "1", "2", "3", "4", "5", "6"}},
		{"direct", "a", false, []string{"1"}},
		{"subtree-boundary", "a/", true, []string{"1", "2"}},
		{"wildcards", "a%_", true, []string{"4"}},
		{"quote", "quote'folder", false, []string{"6"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := taskIDs(t, s, model.TaskQuery{Filter: model.Filter{Folder: tc.folder, Recursive: tc.recursive}})
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestTaskCurrentDatesVersusHistoricalTransitions(t *testing.T) {
	s := openTestStore(t)
	d := fixture(t, "reopened", "reopened.task.md", "Reopened", "body",
		model.Event{State: model.Planned, At: instant("2026-01-01T00:00:00Z")},
		model.Event{State: model.Completed, At: instant("2026-02-01T00:00:00.000000001Z")},
		model.Event{State: model.InProgress, At: instant("2026-03-01T00:00:00Z")})
	apply(t, s, model.Snapshot{Documents: []model.Document{d, fixture(t, "note", "note.note.md", "Note", "body")}})
	from, until, err := tasks.DateRange("2026-02-01", "2026-02-01", instant("2026-01-01T00:00:00Z").Location())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		filter model.Filter
		want   []string
	}{
		{"not-currently-completed", model.Filter{States: []model.State{model.Completed}}, []string{}},
		{"currently-reopened", model.Filter{States: []model.State{model.InProgress}}, []string{"reopened"}},
		{"historical-completion", model.Filter{DateField: "transition", EventState: model.Completed, From: from, Until: until}, []string{"reopened"}},
		{"historical-with-current-filter", model.Filter{States: []model.State{model.Completed}, DateField: "transition", EventState: model.Completed, From: from, Until: until}, []string{}},
		{"current-entry-not-history", model.Filter{DateField: "state_changed", From: from, Until: until}, []string{}},
		{"created-not-history", model.Filter{DateField: "created", From: from, Until: until}, []string{}},
		{"note-kind-does-not-list-tasks", model.Filter{Kinds: []model.Kind{model.Note}}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := taskIDs(t, s, model.TaskQuery{Filter: tc.filter}); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	// Source-edited history replaces the cache; it is not merged with hidden history.
	d = fixture(t, "reopened", d.Path, d.Title, d.Body, model.Event{State: model.InProgress, At: instant("2026-03-01T00:00:00Z")})
	apply(t, s, model.Snapshot{Documents: []model.Document{d}})
	if got := taskIDs(t, s, model.TaskQuery{Filter: model.Filter{DateField: "transition", EventState: model.Completed}}); len(got) != 0 {
		t.Fatalf("hidden history restored: %v", got)
	}
}

func TestTaskDateBoundsUseUTCAndHalfOpenNanoseconds(t *testing.T) {
	s := openTestStore(t)
	from := instant("2026-02-01T01:00:00+01:00")
	until := instant("2026-02-01T00:00:00.000000002Z")
	var docs []model.Document
	for i, at := range []string{"2026-01-31T23:59:59.999999999Z", "2026-02-01T00:00:00Z", "2026-02-01T00:00:00.000000001Z", "2026-02-01T00:00:00.000000002Z"} {
		docs = append(docs, fixture(t, fmt.Sprint(i), fmt.Sprint(i)+".task.md", "Same", "body", model.Event{State: model.Completed, At: instant(at)}))
	}
	apply(t, s, model.Snapshot{Documents: docs})
	for _, field := range []model.DateField{"created", "state_changed", "transition"} {
		t.Run(string(field), func(t *testing.T) {
			f := model.Filter{DateField: field, From: &from, Until: &until}
			if field == "transition" {
				f.EventState = model.Completed
			}
			if got := taskIDs(t, s, model.TaskQuery{Filter: f}); !reflect.DeepEqual(got, []string{"1", "2"}) {
				t.Fatalf("bounds: %v", got)
			}
		})
	}
}

func TestTaskSortAndStablePagination(t *testing.T) {
	s := openTestStore(t)
	var docs []model.Document
	for _, id := range []string{"c", "a", "b"} {
		docs = append(docs, fixture(t, id, id+".task.md", "Same", "body"))
	}
	apply(t, s, model.Snapshot{Documents: docs})
	for _, sort := range []string{"", "title", "created", "state_changed"} {
		for _, desc := range []bool{false, true} {
			q := model.TaskQuery{Sort: sort, Desc: desc, Limit: 2}
			p, err := s.Tasks(t.Context(), q)
			if err != nil || len(p.Items) != 2 || p.Items[0].ID != "a" || p.Items[1].ID != "b" || p.NextOffset == nil || *p.NextOffset != 2 {
				t.Fatalf("first %s/%v: %+v %v", sort, desc, p, err)
			}
			q.Offset = *p.NextOffset
			p, err = s.Tasks(t.Context(), q)
			if err != nil || len(p.Items) != 1 || p.Items[0].ID != "c" || p.NextOffset != nil {
				t.Fatalf("last: %+v %v", p, err)
			}
		}
	}
	// Distinct values catch an ignored sort column/direction, not just tie behavior.
	docs = []model.Document{fixture(t, "a", "a.task.md", "Z", "", model.Event{State: model.Planned, At: instant("2026-01-02T00:00:00Z")}), fixture(t, "b", "b.task.md", "A", "", model.Event{State: model.Planned, At: instant("2026-01-01T00:00:00Z")}, model.Event{State: model.Completed, At: instant("2026-01-03T00:00:00Z")})}
	apply(t, s, model.Snapshot{Documents: docs})
	for _, tc := range []struct {
		sort string
		desc bool
		want []string
	}{{"title", false, []string{"b", "a"}}, {"created", false, []string{"b", "a"}}, {"state_changed", false, []string{"a", "b"}}, {"state_changed", true, []string{"b", "a"}}} {
		if got := taskIDs(t, s, model.TaskQuery{Sort: tc.sort, Desc: tc.desc}); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("sort %+v: %v", tc, got)
		}
	}
}

func TestQueryLimitsAndValidation(t *testing.T) {
	s := openTestStore(t)
	var docs []model.Document
	for i := range 205 {
		docs = append(docs, fixture(t, fmt.Sprintf("%03d", i), fmt.Sprintf("%03d.task.md", i), "Same", "limitword"))
	}
	apply(t, s, model.Snapshot{Documents: docs})
	for _, tc := range []struct{ limit, want int }{{0, 50}, {200, 200}} {
		p, err := s.Tasks(t.Context(), model.TaskQuery{Limit: tc.limit})
		if err != nil || len(p.Items) != tc.want || p.NextOffset == nil || *p.NextOffset != tc.want {
			t.Fatalf("limit: %+v %v", p, err)
		}
	}
	for _, q := range []model.TaskQuery{{Limit: -1}, {Limit: 201}, {Offset: -1}, {Sort: "title; DROP TABLE documents"}, {Filter: model.Filter{Folder: "../a"}}, {Filter: model.Filter{Folder: "/a"}}, {Filter: model.Filter{Kinds: []model.Kind{"other"}}}, {Filter: model.Filter{States: []model.State{"other"}}}, {Filter: model.Filter{DateField: "other"}}, {Filter: model.Filter{DateField: "transition"}}, {Filter: model.Filter{EventState: model.Completed}}, {Filter: model.Filter{From: ptr(instant("2026-01-01T00:00:00Z"))}}, {Filter: model.Filter{DateField: "created", From: ptr(instant("2026-02-01T00:00:00Z")), Until: ptr(instant("2026-01-01T00:00:00Z"))}}} {
		if _, err := s.Tasks(t.Context(), q); model.ErrorCode(err) != "invalid_argument" {
			t.Fatalf("query %+v: %v", q, err)
		}
	}
}

func ptr[T any](v T) *T { return &v }
