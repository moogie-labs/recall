package search

import (
	"testing"

	"recall/internal/model"
)

func TestStateFilterIsTaskOnlyForAllRetrievalModes(t *testing.T) {
	for _, mode := range []string{"keyword", "semantic", "hybrid"} {
		t.Run(mode, func(t *testing.T) {
			e, source, _, _ := searchFixture(t,
				model.Document{ID: "note", Kind: model.Note, State: model.Completed, Body: "needle"},
				model.Document{ID: "task", Path: "task.task.md", Kind: model.Task, State: model.Completed, Body: "needle"},
			)
			for _, kinds := range [][]model.Kind{nil, {model.Note}, {model.Task}} {
				h, err := e.Search(t.Context(), model.SearchQuery{Text: "needle", Mode: mode, Filter: model.Filter{Kinds: kinds, States: []model.State{model.Completed}}})
				if err != nil {
					t.Fatal(err)
				}
				if len(kinds) > 0 && kinds[0] == model.Note {
					if len(h) != 0 {
						t.Errorf("note-only plus task-state returned %d hits", len(h))
					}
				} else if len(h) != 1 || h[0].ID != "task" {
					t.Errorf("state search includes note: %+v", h)
				}
			}
			if source.docs[0].State != model.Completed || source.docs[0].Kind != model.Note {
				t.Fatal("source note metadata rewritten")
			}
		})
	}
}
