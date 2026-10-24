package mcpserver_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"recall/internal/cli"
	"recall/internal/model"
)

// Removing metadata projection/budget checks must fail these public tool tests.
func TestPreviewWireBudgetsTitleAndExactReferences(t *testing.T) {
	title := strings.Repeat("界", 100000)
	raw := "---\nid: exact-id\ntype: task\ntitle: " + title + "\n---\nneedle passage\n"
	a, _ := newTestAppWithSources(t, map[string]string{"exact.task.md": raw})
	s := connect(t, a, "")
	full := data[model.Document](t, call(t, s, "document_get", map[string]any{"id": "exact-id"}))
	if full.Title != title || !strings.Contains(full.Raw, title) {
		t.Fatal("full get lost title")
	}
	for _, name := range []string{"documents_list", "tasks_list", "search"} {
		t.Run(name, func(t *testing.T) {
			args := map[string]any{"limit": 1}
			if name == "search" {
				args["text"], args["mode"] = "needle", "keyword"
			}
			r := call(t, s, name, args)
			var row map[string]any
			switch name {
			case "documents_list":
				row = data[struct {
					Documents []map[string]any `json:"documents"`
				}](t, r).Documents[0]
			case "tasks_list":
				row = data[struct {
					Items []map[string]any `json:"items"`
				}](t, r).Items[0]
			case "search":
				row = data[struct {
					Hits []map[string]any `json:"hits"`
				}](t, r).Hits[0]
			}
			if row["title"] != strings.Repeat("界", 84)+"…" || row["title_truncated"] != true {
				t.Errorf("title preview not explicitly bounded: %d bytes, flag=%v", len(row["title"].(string)), row["title_truncated"])
			}
			if row["id"] != full.ID || row["path"] != full.Path || row["revision"] != full.Revision {
				t.Fatal("exact reference changed")
			}
			b, _ := json.Marshal(row)
			if len(b) > 4096 {
				t.Errorf("row=%d bytes", len(b))
			}
		})
	}
}

func TestPreviewWireBudgetsOversizedIdentityAndWholePage(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		count, idBytes, limit int
		wantError             bool
	}{
		{"identity", 1, 8192, 1, true}, {"encoded-identity", 1, 900, 1, true}, {"whole-page", 20, 1800, 20, true}, {"lower-limit", 20, 1800, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sources := map[string]string{}
			for n := 0; n < tc.count; n++ {
				id := fmt.Sprintf("%03d", n) + strings.Repeat("x", tc.idBytes)
				if tc.name == "encoded-identity" {
					id = strings.Repeat("<", tc.idBytes)
				}
				sources[fmt.Sprintf("%03d.task.md", n)] = "---\nid: '" + id + "'\ntype: task\ntitle: Same\nstate: planned\nstate_history:\n  - state: planned\n    at: 2026-10-18T00:00:00Z\n---\nneedle\n"
			}
			a, _ := newTestAppWithSources(t, sources)
			s := connect(t, a, "")
			for _, name := range []string{"documents_list", "tasks_list", "search"} {
				args := map[string]any{"limit": tc.limit}
				if name == "search" {
					args["text"], args["mode"] = "needle", "keyword"
				}
				r := call(t, s, name, args)
				if tc.wantError {
					if !r.IsError {
						t.Errorf("%s accepted oversized %s", name, tc.name)
						continue
					}
					errorCode(t, r, "output_too_large")
					b, _ := json.Marshal(r)
					if len(b) > 2048 || bytes.Contains(b, []byte(strings.Repeat("x", 100))) {
						t.Fatal("error echoed oversized identity")
					}
				} else {
					data[any](t, r)
				}
			}
			// Even an undisplayable ID is valid source and explicitly retrievable.
			d := data[model.Document](t, call(t, s, "document_get", map[string]any{"path": "000.task.md"}))
			if d.Raw != sources["000.task.md"] {
				t.Fatal("output bounds rewrote source")
			}
		})
	}
}

func TestStateFilterIsTaskOnlyAcrossAdaptersAndPreservesNote(t *testing.T) {
	noteRaw := "---\nid: note\ntype: note\nstate: completed\n---\nneedle\n"
	a, cfg := newTestAppWithSources(t, map[string]string{"note.note.md": noteRaw, "task.task.md": "---\nid: task\ntype: task\nstate: completed\n---\nneedle\n"})
	s := connect(t, a, "")
	hits := data[struct {
		Hits []model.Hit `json:"hits"`
	}](t, call(t, s, "search", map[string]any{"text": "needle", "mode": "keyword", "filter": map[string]any{"states": []string{"completed"}}}))
	if len(hits.Hits) != 1 || hits.Hits[0].ID != "task" {
		t.Errorf("MCP task-state includes notes: %+v", hits.Hits)
	}
	var out, logs bytes.Buffer
	code := cli.Run(t.Context(), []string{"--root", cfg.Root, "--json", "search", "needle", "--mode", "keyword", "--state", "completed"}, nil, &out, &logs)
	var result struct {
		Data []model.Hit `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if code != 0 || len(result.Data) != 1 || result.Data[0].ID != "task" {
		t.Errorf("CLI task-state includes notes: exit=%d hits=%+v", code, result.Data)
	}
	full := data[model.Document](t, call(t, s, "document_get", map[string]any{"id": "note"}))
	if full.Raw != noteRaw || full.State != model.Completed || full.Kind != model.Note {
		t.Fatal("valid note state was rejected/rewritten")
	}
}

func TestPreviewBudgetsPreserveFullTitleSortAndCursor(t *testing.T) {
	prefix := strings.Repeat("x", 300)
	a, _ := newTestAppWithSources(t, map[string]string{
		"a.note.md": "---\nid: a\ntype: note\ntitle: " + prefix + "z\n---\nbody\n",
		"z.note.md": "---\nid: z\ntype: note\ntitle: " + prefix + "a\n---\nbody\n",
	})
	s := connect(t, a, "")
	for offset, want := range []string{"z", "a"} {
		page := data[documentPage](t, call(t, s, "documents_list", map[string]any{"limit": 1, "offset": offset}))
		if len(page.Documents) != 1 || page.Documents[0].ID != want || len(page.Documents[0].Title) > 256 {
			t.Fatalf("preview changed source-title ordering: %+v", page)
		}
		if offset == 0 && (page.NextOffset == nil || *page.NextOffset != 1) || offset == 1 && page.NextOffset != nil {
			t.Fatal("preview changed cursor")
		}
	}
}

func TestPreviewBudgetsDiagnosticsAndFolders(t *testing.T) {
	sources := map[string]string{}
	for n := 0; n < 180; n++ {
		sources[fmt.Sprintf("%03d-", n)+strings.Repeat("f", 190)+"/bad.note.md"] = "---\ntitle: [\n---\n"
	}
	a, _ := newTestAppWithSources(t, sources)
	s := connect(t, a, "")
	for _, name := range []string{"documents_list", "folders_list"} {
		errorCode(t, call(t, s, name, map[string]any{"limit": 200}), "output_too_large")
		// Reducing the page does not skip diagnostics or change continuation.
		r := call(t, s, name, map[string]any{"limit": 1})
		data[any](t, r)
		b, _ := json.Marshal(r.StructuredContent)
		if len(b) > 32768 {
			t.Fatal("small auxiliary page exceeded budget")
		}
	}
	r := call(t, s, "document_get", map[string]any{"path": "../" + strings.Repeat("x", 10000)})
	errorCode(t, r, "output_too_large")
	b, _ := json.Marshal(r)
	if len(b) > 2048 {
		t.Fatal("unbounded MCP error")
	}
}
