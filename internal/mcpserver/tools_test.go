package mcpserver_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"recall/internal/cli"
	"recall/internal/model"
)

type documentPage struct {
	Documents   []model.DocumentSummary `json:"documents"`
	Diagnostics []model.Diagnostic      `json:"diagnostics"`
	NextOffset  *int                    `json:"next_offset"`
}

func TestDocumentsListInvalidOnlyDiagnosticsAreDiscoverableAndPaged(t *testing.T) {
	for _, version := range []string{"", "2025-06-18"} {
		t.Run(version, func(t *testing.T) {
			sources := map[string]string{
				"a-malformed.note.md": "---\ntitle: [unterminated\n---\nprivate-invalid-body\n",
				"b-conflict.note.md":  "---\nid: [conflict\n---\nprivate-invalid-body\n",
				"c-duplicate.note.md": "---\nid: shared\ntype: note\n---\nprivate-invalid-body\n",
				"d-duplicate.note.md": "---\nid: shared\n---\nprivate-invalid-body\n",
			}
			a, cfg := newTestAppWithSources(t, sources)
			s := connect(t, a, version)
			want := []struct{ path, code string }{{"a-malformed.note.md", "invalid_document"}, {"b-conflict.note.md", "invalid_document"}, {"c-duplicate.note.md", "duplicate_id"}, {"d-duplicate.note.md", "duplicate_id"}}
			for offset := 0; offset < 4; offset += 2 {
				r := call(t, s, "documents_list", map[string]any{"limit": 2, "offset": offset, "desc": true})
				page := data[documentPage](t, r)
				if len(page.Documents) != 0 || len(page.Diagnostics) != 2 {
					t.Fatalf("offset %d: invalid sources are hidden or unbounded: %+v", offset, page)
				}
				for n, diagnostic := range page.Diagnostics {
					if diagnostic.Path != want[offset+n].path || diagnostic.Code != want[offset+n].code || diagnostic.Message == "" {
						t.Fatalf("offset %d diagnostic %d: %+v", offset, n, diagnostic)
					}
				}
				if offset == 0 && (page.NextOffset == nil || *page.NextOffset != 2) || offset == 2 && page.NextOffset != nil {
					t.Fatalf("diagnostic cursor: %+v", page)
				}
				b, _ := json.Marshal(r)
				if bytes.Contains(b, []byte("private-invalid-body")) {
					t.Fatal("diagnostics leaked raw source")
				}
			}
			page := data[documentPage](t, call(t, s, "documents_list", map[string]any{"limit": 2, "offset": 4}))
			if page.Documents == nil || page.Diagnostics == nil || len(page.Documents)+len(page.Diagnostics) != 0 || page.NextOffset != nil {
				t.Fatalf("exhausted diagnostic cursor: %+v", page)
			}
			// Preserve the actual App messages, not just codes, and let clients act
			// on a discovered path even though it is not a valid-document summary.
			snapshot, err := a.List(t.Context(), "", false)
			if err != nil {
				t.Fatal(err)
			}
			all := data[documentPage](t, call(t, s, "documents_list", map[string]any{}))
			if !reflect.DeepEqual(all.Diagnostics, snapshot.Diagnostics) {
				t.Fatalf("diagnostics changed: %+v want %+v", all.Diagnostics, snapshot.Diagnostics)
			}
			// CLI keeps its existing diagnostic visibility independently of MCP's
			// new shared pagination budget, in both JSON and human output.
			for _, jsonMode := range []bool{false, true} {
				args := []string{"--root", cfg.Root, "document", "list", "--limit", "1"}
				if jsonMode {
					args = append(args, "--json")
				}
				var out, logs bytes.Buffer
				if code := cli.Run(t.Context(), args, strings.NewReader(""), &out, &logs); code != 0 {
					t.Fatalf("CLI diagnostics: exit %d: %s %s", code, &out, &logs)
				}
				if jsonMode {
					var envelope struct {
						Data documentPage `json:"data"`
					}
					if err := json.Unmarshal(out.Bytes(), &envelope); err != nil || !reflect.DeepEqual(envelope.Data.Diagnostics, snapshot.Diagnostics) {
						t.Fatalf("CLI JSON diagnostics: %s %v", &out, err)
					}
				} else {
					for _, diagnostic := range snapshot.Diagnostics {
						if !strings.Contains(logs.String(), diagnostic.Path+": "+diagnostic.Code+": "+diagnostic.Message) {
							t.Fatalf("CLI human diagnostic missing: %+v: %s", diagnostic, &logs)
						}
					}
				}
			}
			for _, diagnostic := range all.Diagnostics {
				errorCode(t, call(t, s, "document_get", map[string]any{"path": diagnostic.Path}), diagnostic.Code)
			}
			if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".cache", "recall")); !os.IsNotExist(err) {
				t.Fatalf("invalid-source listing started inference: %v", err)
			}
		})
	}
}

func TestDocumentsListMixedSourcesShareOnePaginationBudget(t *testing.T) {
	a, _ := newTestAppWithSources(t, map[string]string{
		"bad.note.md":    "---\ntitle: [unterminated\n---\nprivate-invalid-body\n",
		"good-a.note.md": "---\nid: b\ntype: note\ntitle: Same\n---\nprivate-valid-body\n",
		"good-b.note.md": "---\nid: a\ntype: note\ntitle: Same\n---\nprivate-valid-body\n",
		"good-z.note.md": "---\nid: z\ntype: note\ntitle: Zulu\n---\nprivate-valid-body\n",
	})
	s := connect(t, a, "")
	for _, tc := range []struct {
		offset      int
		desc        bool
		ids         []string
		diagnostics int
		next        int
	}{
		{0, false, []string{"a"}, 1, 2},
		{2, false, []string{"b", "z"}, 0, -1},
		{0, true, []string{"z"}, 1, 2},
		{1, true, []string{"z", "a"}, 0, 3},
		{3, true, []string{"b"}, 0, -1},
		{4, false, []string{}, 0, -1},
		{100, false, []string{}, 0, -1},
	} {
		r := call(t, s, "documents_list", map[string]any{"limit": 2, "offset": tc.offset, "desc": tc.desc})
		page := data[documentPage](t, r)
		ids := make([]string, len(page.Documents))
		for n, d := range page.Documents {
			ids[n] = d.ID
		}
		if !reflect.DeepEqual(ids, tc.ids) || len(page.Diagnostics) != tc.diagnostics || len(page.Documents)+len(page.Diagnostics) > 2 {
			t.Fatalf("offset %d desc %v: shared page budget/order: %+v", tc.offset, tc.desc, page)
		}
		if tc.next < 0 && page.NextOffset != nil || tc.next >= 0 && (page.NextOffset == nil || *page.NextOffset != tc.next) {
			t.Fatalf("cursor: %+v", page)
		}
		b, _ := json.Marshal(r)
		if bytes.Contains(b, []byte("private-valid-body")) || bytes.Contains(b, []byte("private-invalid-body")) {
			t.Fatal("mixed listing leaked source")
		}
	}
}

func TestDocumentsListDiagnosticFolderScope(t *testing.T) {
	a, _ := newTestAppWithSources(t, map[string]string{
		"bad.note.md":            "---\ntitle: [unterminated\n---\n",
		"work/bad.note.md":       "---\ntitle: [unterminated\n---\n",
		"work/sub/bad.note.md":   "---\ntitle: [unterminated\n---\n",
		"outside/bad.note.md":    "---\ntitle: [unterminated\n---\n",
		"work/valid.note.md":     "---\nid: first\ntype: note\n---\nfirst body\n",
		"work/sub/valid.note.md": "---\nid: second\n---\nsecond body\n",
	})
	s := connect(t, a, "")
	for _, tc := range []struct {
		folder    string
		recursive bool
		paths     []string
		ids       []string
	}{
		{"work", false, []string{"work/bad.note.md"}, []string{"first"}},
		{"work", true, []string{"work/bad.note.md", "work/sub/bad.note.md"}, []string{"first", "second"}},
		{".", false, []string{"bad.note.md"}, []string{}},
		{"", false, []string{"bad.note.md", "outside/bad.note.md", "work/bad.note.md", "work/sub/bad.note.md"}, []string{"first", "second"}},
	} {
		page := data[documentPage](t, call(t, s, "documents_list", map[string]any{"folder": tc.folder, "recursive": tc.recursive}))
		paths := make([]string, len(page.Diagnostics))
		for n, d := range page.Diagnostics {
			paths[n] = d.Path
		}
		ids := make([]string, len(page.Documents))
		for n, d := range page.Documents {
			ids[n] = d.ID
		}
		if !reflect.DeepEqual(paths, tc.paths) || !reflect.DeepEqual(ids, tc.ids) || page.NextOffset != nil {
			t.Fatalf("folder %q recursive %v: %+v", tc.folder, tc.recursive, page)
		}
	}
	// Scope filtering happens before the shared cursor is applied.
	page := data[documentPage](t, call(t, s, "documents_list", map[string]any{"folder": "work", "recursive": true, "limit": 2}))
	if len(page.Diagnostics) != 2 || len(page.Documents) != 0 || page.NextOffset == nil || *page.NextOffset != 2 {
		t.Fatalf("filtered diagnostic page: %+v", page)
	}
	page = data[documentPage](t, call(t, s, "documents_list", map[string]any{"folder": "work", "recursive": true, "limit": 2, "offset": 2}))
	if len(page.Diagnostics) != 0 || len(page.Documents) != 2 || page.NextOffset != nil {
		t.Fatalf("filtered summary page: %+v", page)
	}
}

func call(t *testing.T, s *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	r, err := s.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(name, err)
	}
	return r
}

func data[T any](t *testing.T, r *mcp.CallToolResult) T {
	t.Helper()
	if r.IsError {
		t.Fatalf("tool error: %+v", r)
	}
	b, err := json.Marshal(r.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(b, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(r.Content) != 1 {
		t.Fatalf("missing text fallback: %+v", r)
	}
	text, ok := r.Content[0].(*mcp.TextContent)
	if !ok || !json.Valid([]byte(text.Text)) {
		t.Fatalf("invalid fallback: %+v", r.Content)
	}
	var fallback, structured any
	json.Unmarshal([]byte(text.Text), &fallback)
	json.Unmarshal(b, &structured)
	if !reflect.DeepEqual(fallback, structured) {
		t.Fatal("legacy fallback differs from structured output")
	}
	return envelope.Data
}

func errorCode(t *testing.T, r *mcp.CallToolResult, want string) {
	t.Helper()
	b, _ := json.Marshal(r.StructuredContent)
	var envelope struct {
		Error *model.Error `json:"error"`
	}
	json.Unmarshal(b, &envelope)
	if !r.IsError || envelope.Error == nil || envelope.Error.Code != want {
		t.Fatalf("want %s: %+v structured=%s", want, r, b)
	}
}

func TestFullCRUDFoldersAndStaleWritesWithoutApproval(t *testing.T) {
	s := connect(t, newTestApp(t), "") // No elicitation handler or approval capability.
	data[any](t, call(t, s, "folder_create", map[string]any{"path": "projects"}))
	data[any](t, call(t, s, "folder_create", map[string]any{"path": "projects/sub"}))
	note := data[model.Document](t, call(t, s, "document_create", map[string]any{"folder": "projects", "name": "guide", "body": "free café\r\n"}))
	other := data[model.Document](t, call(t, s, "document_create", map[string]any{"folder": "projects/sub", "name": "work", "body": "do work"}))
	if note.Body != "free café\r\n" {
		t.Fatalf("%+v", note)
	}
	updated := data[model.Document](t, call(t, s, "document_update", map[string]any{"id": note.ID, "if_revision": note.Revision, "raw": strings.Replace(note.Raw, "free café", "new café", 1)}))
	if updated.Body != "new café\r\n" || updated.Revision == note.Revision {
		t.Fatal(updated)
	}
	errorCode(t, call(t, s, "document_update", map[string]any{"id": note.ID, "if_revision": note.Revision, "raw": note.Raw}), "conflict")
	errorCode(t, call(t, s, "document_move", map[string]any{"id": note.ID, "to": "stale.note.md", "if_revision": note.Revision}), "conflict")
	errorCode(t, call(t, s, "document_delete", map[string]any{"id": note.ID, "if_revision": note.Revision}), "conflict")
	data[model.Document](t, call(t, s, "document_move", map[string]any{"id": note.ID, "to": "projects/sub/moved.note.md", "if_revision": updated.Revision}))
	data[any](t, call(t, s, "folder_move", map[string]any{"from": "projects", "to": "archive"}))
	got := data[model.Document](t, call(t, s, "document_get", map[string]any{"id": note.ID}))
	if got.Path != "archive/sub/moved.note.md" || got.Raw != updated.Raw {
		t.Fatal(got)
	}
	folders := data[struct {
		Folders []string `json:"folders"`
	}](t, call(t, s, "folders_list", map[string]any{}))
	if !reflect.DeepEqual(folders.Folders, []string{".", "archive", "archive/sub"}) {
		t.Fatal(folders)
	}
	errorCode(t, call(t, s, "folder_delete", map[string]any{"path": "archive"}), "folder_not_empty")
	data[any](t, call(t, s, "document_delete", map[string]any{"id": other.ID, "if_revision": other.Revision}))
	errorCode(t, call(t, s, "document_get", map[string]any{"id": other.ID}), "not_found")
	data[any](t, call(t, s, "folder_delete", map[string]any{"path": "archive", "recursive": true}))
	errorCode(t, call(t, s, "document_get", map[string]any{"id": note.ID}), "not_found")
}

func TestListsAreBoundedMetadataAndExplicitGetRetainsLargeBody(t *testing.T) {
	a := newTestApp(t)
	s := connect(t, a, "2025-06-18")
	body := strings.Repeat("distinctive-heavy-payload-", 10000)
	for _, name := range []string{"first", "second"} {
		d := data[model.Document](t, call(t, s, "document_create", map[string]any{"name": name, "body": body}))
		get := data[model.Document](t, call(t, s, "document_get", map[string]any{"path": d.Path}))
		if get.Body != body || !strings.Contains(get.Raw, body) {
			t.Fatal("explicit retrieval lost body")
		}
	}
	for _, name := range []string{"documents_list"} {
		r := call(t, s, name, map[string]any{"limit": 1})
		b, _ := json.Marshal(r)
		for _, forbidden := range []string{"distinctive-heavy-payload", `\"body\"`, `\"raw\"`} {
			if bytes.Contains(b, []byte(forbidden)) {
				t.Fatalf("%s leaks %s", name, forbidden)
			}
		}
		if len(b) > 6000 {
			t.Fatalf("unbounded list: %d", len(b))
		}
		data[any](t, r)
	}
	page := data[struct {
		Documents []model.DocumentSummary `json:"documents"`
		Next      *int                    `json:"next_offset"`
	}](t, call(t, s, "documents_list", map[string]any{"limit": 1}))
	if len(page.Documents) != 1 || page.Next == nil || *page.Next != 1 {
		t.Fatal(page)
	}
	page = data[struct {
		Documents []model.DocumentSummary `json:"documents"`
		Next      *int                    `json:"next_offset"`
	}](t, call(t, s, "documents_list", map[string]any{"limit": 1, "offset": 1}))
	if len(page.Documents) != 1 || page.Next != nil {
		t.Fatal(page)
	}
	// Core App contracts are deliberately still full documents.
	snapshot, err := a.List(t.Context(), "", false)
	if err != nil || len(snapshot.Documents) != 2 || snapshot.Documents[0].Body != body {
		t.Fatalf("core list changed: %v", err)
	}
}

func TestSharedQueriesFiltersPaginationAndKeywordStayLazy(t *testing.T) {
	s := connect(t, newTestApp(t), "")
	data[any](t, call(t, s, "folder_create", map[string]any{"path": "work"}))
	for _, name := range []string{"Zulu", "Alpha", "Bravo"} {
		data[model.Document](t, call(t, s, "document_create", map[string]any{"folder": "work", "name": name, "body": "retrievalneedle"}))
	}
	page := data[documentPage](t, call(t, s, "documents_list", map[string]any{"folder": "work", "recursive": true, "limit": 2}))
	if len(page.Documents) != 2 || page.Documents[0].Title != "Alpha" || page.Documents[1].Title != "Bravo" || page.NextOffset == nil || *page.NextOffset != 2 {
		t.Fatal(page)
	}
	page = data[documentPage](t, call(t, s, "documents_list", map[string]any{"limit": 2, "offset": 2}))
	if len(page.Documents) != 1 || page.Documents[0].Title != "Zulu" || page.NextOffset != nil {
		t.Fatal(page)
	}
	page = data[documentPage](t, call(t, s, "documents_list", map[string]any{"folder": ".", "recursive": false}))
	if len(page.Documents) != 0 {
		t.Fatal(page)
	}
	page = data[documentPage](t, call(t, s, "documents_list", map[string]any{"desc": true, "limit": 2}))
	if len(page.Documents) != 2 || page.Documents[0].Title != "Zulu" || page.Documents[1].Title != "Bravo" {
		t.Fatal(page)
	}
	hits := data[struct {
		Hits []model.Hit `json:"hits"`
	}](t, call(t, s, "search", map[string]any{"text": "retrievalneedle", "mode": "keyword", "limit": 1, "filter": map[string]any{"folder": "work"}}))
	if len(hits.Hits) != 1 {
		t.Fatal(hits)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".cache", "recall")); !os.IsNotExist(err) {
		t.Fatalf("metadata/keyword spawned worker: %v", err)
	}
	for _, tc := range []struct {
		name string
		args any
		code string
	}{
		{"documents_list", map[string]any{"limit": 201}, "invalid_argument"},
		{"documents_list", map[string]any{"offset": -1}, "invalid_argument"},
		{"search", map[string]any{"text": "x", "mode": "keyword", "limit": 101}, "invalid_argument"},
		{"document_get", map[string]any{}, "invalid_argument"},
		{"document_get", map[string]any{"id": "abc", "path": "x.note.md"}, "invalid_argument"},
		{"document_get", map[string]any{"path": "../escape.note.md"}, "outside_root"},
		{"folder_delete", map[string]any{"path": ".", "recursive": true}, "outside_root"},
	} {
		errorCode(t, call(t, s, tc.name, tc.args), tc.code)
	}
}
