package index

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"recall/internal/embedding"
	"recall/internal/model"
)

func TestIndexPreviewWireBudgets(t *testing.T) {
	for _, tc := range []struct {
		name, id, path, revision string
		wantError                bool
	}{
		{"title", "exact", "a.note.md", "exact-revision", false},
		{"identity", strings.Repeat("x", 8192), "a.note.md", "rev", true},
		{"unrepresentable-path", "exact", "bad\xff.note.md", "rev", true},
		{"revision", "exact", "a.note.md", strings.Repeat("r", 8192), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTestStore(t)
			d := fixture(t, tc.id, tc.path, strings.Repeat("界", 1000), "needle")
			d.Revision = tc.revision
			apply(t, s, model.Snapshot{Documents: []model.Document{d}})
			h, err := s.Keyword(t.Context(), model.SearchQuery{Text: "needle"})
			check := func(h []model.Hit, err error) {
				t.Helper()
				if tc.wantError {
					if model.ErrorCode(err) != "output_too_large" {
						t.Errorf("oversized/unrepresentable reference accepted: %v", err)
					}
					return
				}
				if err != nil || len(h) != 1 {
					t.Fatalf("hits=%d err=%v", len(h), err)
				}
				b, _ := json.Marshal(h[0])
				var row map[string]any
				json.Unmarshal(b, &row)
				if row["title_truncated"] != true || len(h[0].Title) > 256 || h[0].ID != tc.id || h[0].Path != tc.path || h[0].Revision != tc.revision {
					t.Error("preview/reference contract missing")
				}
			}
			check(h, err)
			if err := s.EnsureSignature(t.Context(), "sig"); err != nil {
				t.Fatal(err)
			}
			j, err := s.ClaimJob(t.Context(), model.Filter{}, "sig", time.Now)
			if err != nil || j == nil {
				t.Fatal(err)
			}
			if j.Document.Title != d.Title || j.Document.ID != d.ID {
				t.Fatal("preview altered embedding input")
			}
			v := make([]float32, embedding.Dimensions)
			v[0] = 1
			ok, err := s.Publish(t.Context(), *j, []Chunk{{Text: "needle"}}, [][]float32{v}, time.Now)
			if err != nil || !ok {
				t.Fatal(err)
			}
			h = nil
			err = s.StreamVectors(t.Context(), model.Filter{}, "sig", func(hit model.Hit, _ []float32) error { h = append(h, hit); return nil })
			check(h, err)
		})
	}
}
