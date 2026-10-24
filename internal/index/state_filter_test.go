package index

import (
	"reflect"
	"testing"
	"time"

	"recall/internal/embedding"
	"recall/internal/model"
)

func TestStateFilterIsTaskOnlyForKeywordAndVectors(t *testing.T) {
	s := openTestStore(t)
	note := fixture(t, "note", "note.note.md", "Same", "needle")
	note.State = model.Completed
	task := fixture(t, "task", "task.task.md", "Same", "needle", model.Event{State: model.Completed, At: time.Now()})
	apply(t, s, model.Snapshot{Documents: []model.Document{note, task}})
	if err := s.EnsureSignature(t.Context(), "sig"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		j, err := s.ClaimJob(t.Context(), model.Filter{}, "sig", time.Now)
		if err != nil || j == nil {
			t.Fatal(err)
		}
		v := make([]float32, embedding.Dimensions)
		v[0] = 1
		ok, err := s.Publish(t.Context(), *j, []Chunk{{Text: "needle"}}, [][]float32{v}, time.Now)
		if err != nil || !ok {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name   string
		filter model.Filter
		want   []string
	}{
		{"unfiltered", model.Filter{}, []string{"note", "task"}},
		{"state", model.Filter{States: []model.State{model.Completed}}, []string{"task"}},
		{"note-and-state", model.Filter{Kinds: []model.Kind{model.Note}, States: []model.State{model.Completed}}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits, err := s.Keyword(t.Context(), model.SearchQuery{Text: "needle", Filter: tc.filter})
			if err != nil {
				t.Fatal(err)
			}
			ids := []string{}
			for _, h := range hits {
				ids = append(ids, h.ID)
			}
			if !reflect.DeepEqual(ids, tc.want) {
				t.Errorf("keyword ids=%v want=%v", ids, tc.want)
			}
			ids = []string{}
			err = s.StreamVectors(t.Context(), tc.filter, "sig", func(h model.Hit, _ []float32) error { ids = append(ids, h.ID); return nil })
			if err != nil || !reflect.DeepEqual(ids, tc.want) {
				t.Errorf("vector ids=%v want=%v err=%v", ids, tc.want, err)
			}
		})
	}
	var state string
	if err := s.db.QueryRow("SELECT state FROM documents WHERE id='note'").Scan(&state); err != nil || state != "completed" {
		t.Fatal("note metadata lost", err)
	}
}
