package search

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"recall/internal/model"
)

func TestRetrievalPreviewWireBudgets(t *testing.T) {
	for _, mode := range []string{"keyword", "semantic", "hybrid"} {
		for _, tc := range []struct {
			name           string
			count, idBytes int
			title          string
			wantError      bool
		}{
			{"title", 1, 10, strings.Repeat("界", 1000), false}, {"identity", 1, 8192, "Same", true}, {"page", 20, 1800, "Same", true},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				var docs []model.Document
				for n := 0; n < tc.count; n++ {
					docs = append(docs, model.Document{ID: fmt.Sprintf("%03d", n) + strings.Repeat("x", tc.idBytes), Path: fmt.Sprintf("%03d.note.md", n), Title: tc.title, Body: "needle"})
				}
				e, source, idx, _ := searchFixture(t, docs...)
				h, err := e.Search(t.Context(), model.SearchQuery{Text: "needle", Mode: mode, Limit: 20})
				if tc.wantError {
					if model.ErrorCode(err) != "output_too_large" || h != nil {
						t.Fatalf("oversized output accepted: hits=%d err=%v", len(h), err)
					}
					return
				}
				if err != nil || len(h) != 1 {
					t.Fatalf("hits=%d err=%v", len(h), err)
				}
				b, _ := json.Marshal(h[0])
				var row map[string]any
				json.Unmarshal(b, &row)
				if row["title"] != strings.Repeat("界", 84)+"…" || row["title_truncated"] != true || len(b) > 4096 {
					t.Fatalf("unbounded/unmarked hit title: bytes=%d flag=%v", len(b), row["title_truncated"])
				}
				if h[0].ID != source.docs[0].ID || h[0].Path != source.docs[0].Path || h[0].Revision != source.docs[0].Revision || source.docs[0].Title != tc.title {
					t.Fatal("output projection changed authority/reference")
				}
				if mode != "keyword" {
					current, err := idx.SourceCurrent(t.Context(), source.docs[0])
					if err != nil || !current {
						t.Fatal("projection changed source comparison", err)
					}
				}
			})
		}
	}
}

func TestRetrievalPreviewRowBudgetIncludesFinalScore(t *testing.T) {
	// A fused score has more encoded digits than the vector stream's initial 0.
	// Exercise identities close to the row boundary, not just wildly huge ones.
	for size := 3800; size < 3900; size++ {
		e, _, _, _ := searchFixture(t, model.Document{ID: strings.Repeat("x", size), Path: "a.note.md", Title: "Same", Body: "needle"})
		h, err := e.Search(t.Context(), model.SearchQuery{Text: "needle", Mode: "semantic", Limit: 1})
		if err != nil {
			if model.ErrorCode(err) != "output_too_large" {
				t.Fatal(err)
			}
			continue
		}
		b, _ := json.Marshal(h[0])
		if len(b) > 4096 {
			t.Fatalf("final scored row escaped budget: id=%d row=%d", size, len(b))
		}
	}
}

func TestHybridBudgetMeasuresReturnedPageNotHiddenEvidence(t *testing.T) {
	var docs []model.Document
	for n := 0; n < 20; n++ {
		docs = append(docs, model.Document{ID: fmt.Sprintf("%03d", n) + strings.Repeat("x", 1800), Path: fmt.Sprintf("%03d.note.md", n), Title: "Same", Body: "needle"})
	}
	e, _, _, _ := searchFixture(t, docs...)
	for _, mode := range []string{"keyword", "semantic", "hybrid"} {
		h, err := e.Search(t.Context(), model.SearchQuery{Text: "needle", Mode: mode, Limit: 1})
		if err != nil || len(h) != 1 || h[0].ID != docs[0].ID {
			t.Errorf("%s: a representable one-hit page failed: hits=%d err=%v", mode, len(h), err)
		}
	}
}
