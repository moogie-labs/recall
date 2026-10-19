package document

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"recall/internal/model"
)

func TestCodecPreservesBody(t *testing.T) {
	raw := []byte("---\r\ntype: note\r\ncustom: [a, b]\r\n---\r\n# café\r\n---\r\n")
	p, err := Parse("ideas.note.md", raw)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, raw) {
		t.Fatalf("round-trip: %q, want %q", out, raw)
	}
}

func TestCodecRoundTrip(t *testing.T) {
	for _, raw := range []string{
		"",
		"# No required heading\n\n---\nordinary body\n",
		"\xef\xbb\xbfplain café\r\n---\r\n",
		"---\ntype: note # keep this\ncustom: {nested: [a, 7, null]}\n---\n",
		"\xef\xbb\xbf---\r\n# custom comment\r\ntype: note\r\ncustom: &items [a, b]\r\ncopy: *items\r\n---\r\n# café\r\n---\r\n",
		"---\n---",
		"---\ntype: note\n---\nbody without trailing newline",
	} {
		t.Run(fmt.Sprintf("%q", raw), func(t *testing.T) {
			p, err := Parse("ideas.note.md", []byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			out, err := Encode(p)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != raw {
				t.Fatalf("round-trip = %q, want %q", out, raw)
			}
		})
	}
}

func TestParseProjectsTypedDocument(t *testing.T) {
	raw := "---\ntype: task\nid: existing-id\ntitle: Write café guide\nstate: in_progress\nstate_history:\n  - state: planned\n    at: 2026-10-17T12:00:00+02:00\n  - state: in_progress\n    at: 2026-10-18T09:00:00Z\ncustom: anything\n---\nDescription\n---\n"
	p, err := Parse("folder/guide.task.md", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	d := p.Document
	if d.Kind != model.Task || d.ID != "existing-id" || d.Title != "Write café guide" || d.Path != "folder/guide.task.md" || d.State != model.InProgress {
		t.Fatalf("unexpected typed metadata: %+v", d)
	}
	if len(d.History) != 2 || d.History[0].State != model.Planned || !d.History[0].At.Equal(time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("history = %+v", d.History)
	}
	if d.Body != "Description\n---\n" || string(p.Body) != d.Body || d.Raw != raw {
		t.Fatal("source/body projection differs")
	}
	if want := fmt.Sprintf("%x", sha256.Sum256([]byte(raw))); d.Revision != want {
		t.Fatalf("revision = %q, want %q", d.Revision, want)
	}
}

func TestParseAllowsMissingManagedFields(t *testing.T) {
	for _, raw := range []string{"", "free-form body", "---\ncustom: value\n---\n"} {
		p, err := Parse("work.task.md", []byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if p.Document.Kind != model.Task || p.Document.ID != "" || p.Document.State != "" || len(p.Document.History) != 0 {
			t.Fatalf("missing fields were normalized: %+v", p.Document)
		}
	}
}

func TestParseRejectsInvalidDocuments(t *testing.T) {
	tests := []struct {
		name, path, raw, code string
	}{
		{"untyped filename", "ideas.md", "---\ntype: note\n---\n", "invalid_document"},
		{"untyped body", "ideas.md", "plain body", "invalid_document"},
		{"empty name", ".note.md", "body", "invalid_document"},
		{"uppercase suffix", "ideas.NOTE.md", "body", "invalid_document"},
		{"type mismatch", "ideas.note.md", "---\ntype: task\n---\n", "type_mismatch"},
		{"reverse mismatch", "work.task.md", "---\ntype: note\n---\n", "type_mismatch"},
		{"unknown type", "ideas.note.md", "---\ntype: memo\n---\n", "invalid_document"},
		{"type array", "ideas.note.md", "---\ntype: [note]\n---\n", "invalid_document"},
		{"type null", "ideas.note.md", "---\ntype: null\n---\n", "invalid_document"},
		{"malformed YAML", "ideas.note.md", "---\nprivate: [SECRET\n---\n", "invalid_document"},
		{"trailing YAML after end", "ideas.note.md", "---\ntype: note\n...\nSECRET\n---\n", "invalid_document"},
		{"missing delimiter", "ideas.note.md", "---\ntype: note\n", "invalid_document"},
		{"sequence metadata", "ideas.note.md", "---\n- note\n---\n", "invalid_document"},
		{"scalar metadata", "ideas.note.md", "---\nSECRET\n---\n", "invalid_document"},
		{"duplicate type", "ideas.note.md", "---\ntype: note\ntype: note\n---\n", "invalid_document"},
		{"duplicate unknown key", "ideas.note.md", "---\ncustom: one\ncustom: two\n---\n", "invalid_document"},
		{"duplicate aliased key", "ideas.note.md", "---\n&key custom: one\n*key: two\n---\n", "invalid_document"},
		{"nested duplicate key", "ideas.note.md", "---\ncustom: {x: one, x: two}\n---\n", "invalid_document"},
		{"merged type mismatch", "ideas.note.md", "---\nbase: &base {type: task}\n<<: *base\n---\n", "type_mismatch"},
		{"invalid merge", "ideas.note.md", "---\n<<: SECRET\n---\n", "invalid_document"},
		{"id number", "ideas.note.md", "---\nid: 42\n---\n", "invalid_document"},
		{"title map", "ideas.note.md", "---\ntitle: {SECRET: value}\n---\n", "invalid_document"},
		{"invalid state", "work.task.md", "---\nstate: SECRET\n---\n", "invalid_document"},
		{"history map", "work.task.md", "---\nstate_history: {}\n---\n", "invalid_document"},
		{"history null", "work.task.md", "---\nstate_history: null\n---\n", "invalid_document"},
		{"history missing state", "work.task.md", "---\nstate_history: [{at: 2026-10-18T09:00:00Z}]\n---\n", "invalid_document"},
		{"history missing timestamp", "work.task.md", "---\nstate_history: [{state: planned}]\n---\n", "invalid_document"},
		{"history date only", "work.task.md", "---\nstate_history: [{state: planned, at: 2026-10-18}]\n---\n", "invalid_document"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.path, []byte(tt.raw))
			if err == nil || model.ErrorCode(err) != tt.code {
				t.Fatalf("Parse error = %v, want %s", err, tt.code)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("error exposes document content: %v", err)
			}
		})
	}
}

func TestEncodePreservesUnknownMetadataAndCommentsWhenEdited(t *testing.T) {
	raw := "\xef\xbb\xbf---\r\n# user comment\r\ntype: note # type comment\r\ncustom: {nested: [a, 7, null]} # custom comment\r\n---\r\n# café\r\n---\r\n"
	p, err := Parse("ideas.note.md", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !p.BOM || p.Newline != "\r\n" {
		t.Fatalf("source style = BOM %v, newline %q", p.BOM, p.Newline)
	}
	p.Document.ID = "assigned-id"
	out, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out, []byte("\xef\xbb\xbf---\r\n")) || !bytes.HasSuffix(out, []byte("---\r\n# café\r\n---\r\n")) {
		t.Fatalf("BOM or body changed: %q", out)
	}
	for _, comment := range []string{"# user comment", "# type comment", "# custom comment"} {
		if !bytes.Contains(out, []byte(comment)) {
			t.Fatalf("lost %q: %q", comment, out)
		}
	}
	q, err := Parse("ideas.note.md", out)
	if err != nil {
		t.Fatal(err)
	}
	if q.Document.ID != "assigned-id" {
		t.Fatalf("updated id = %q", q.Document.ID)
	}
	var fields map[string]any
	if err := q.Metadata.Decode(&fields); err != nil {
		t.Fatal(err)
	}
	custom, ok := fields["custom"].(map[string]any)
	if !ok {
		t.Fatalf("custom metadata = %#v", fields["custom"])
	}
	values, ok := custom["nested"].([]any)
	if !ok || len(values) != 3 || values[0] != "a" || values[1] != 7 || values[2] != nil {
		t.Fatalf("unknown values changed: %#v", custom)
	}
}

func TestEncodeUsesMetadataAndBodyEdits(t *testing.T) {
	p, err := Parse("ideas.note.md", []byte("---\ntype: note\ncustom: old\n---\nold body\n"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(p.Metadata.Content); i += 2 {
		if p.Metadata.Content[i].Value == "custom" {
			p.Metadata.Content[i+1].Value = "new"
		}
	}
	p.Body = []byte("new body\n---\n")
	out, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	q, err := Parse("ideas.note.md", out)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := q.Metadata.Decode(&fields); err != nil {
		t.Fatal(err)
	}
	if fields["custom"] != "new" || string(q.Body) != "new body\n---\n" {
		t.Fatalf("edits were lost: %q", out)
	}
}

func TestEncodeAddsFrontmatterWithoutNormalizingMissingFields(t *testing.T) {
	p, err := Parse("work.task.md", []byte("description\n"))
	if err != nil {
		t.Fatal(err)
	}
	p.Document.ID = "new-id"
	out, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	q, err := Parse("work.task.md", out)
	if err != nil {
		t.Fatal(err)
	}
	if q.Document.ID != "new-id" || q.Document.State != "" || len(q.Document.History) != 0 || string(q.Body) != "description\n" {
		t.Fatalf("unexpected metadata after encode: %+v", q.Document)
	}
}

func TestEncodeRejectsInvalidMetadataChanges(t *testing.T) {
	p, err := Parse("ideas.note.md", []byte("---\ntype: note\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	p.Document.Kind = model.Task
	if _, err := Encode(p); err == nil || model.ErrorCode(err) != "type_mismatch" {
		t.Fatalf("Encode error = %v, want type_mismatch", err)
	}
}

func TestEncodePreservesBodyOnlyHeaderFormatting(t *testing.T) {
	raw := "---\r\ntype : 'note' # keep exact layout\r\ncustom:    [  a, b  ]\r\n---\r\nold body"
	p, err := Parse("ideas.note.md", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	p.Body = []byte("new body\n---\n")
	out, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSuffix(raw, "old body") + string(p.Body); string(out) != want {
		t.Fatalf("body-only edit reformatted header: %q", out)
	}
}

func TestEncodeAddsBodyAfterEOFClosingDelimiter(t *testing.T) {
	for _, tt := range []struct {
		name, raw, want string
	}{
		{"LF", "---\ntype: note\n---", "---\ntype: note\n---\nnew body\n"},
		{"CRLF", "---\r\ntype: note\r\n---", "---\r\ntype: note\r\n---\r\nnew body\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Parse("ideas.note.md", []byte(tt.raw))
			if err != nil {
				t.Fatal(err)
			}
			unchanged, err := Encode(p)
			if err != nil {
				t.Fatal(err)
			}
			if string(unchanged) != tt.raw {
				t.Fatalf("unchanged source = %q, want %q", unchanged, tt.raw)
			}
			p.Body = []byte("new body\n")
			out, err := Encode(p)
			if err != nil {
				t.Fatal(err)
			}
			q, err := Parse("ideas.note.md", out)
			if err != nil {
				t.Fatalf("encoded document cannot be reparsed: %v; source = %q", err, out)
			}
			if string(out) != tt.want || !bytes.Equal(q.Body, p.Body) {
				t.Fatalf("encoded source = %q, want %q; reparsed body = %q", out, tt.want, q.Body)
			}
		})
	}
}

func TestParseReadsAliasedManagedFields(t *testing.T) {
	p, err := Parse("work.task.md", []byte("---\nbase: &base {type: task, id: existing-id}\n<<: *base\nstatus: &status planned\nstate: *status\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Document.ID != "existing-id" || p.Document.State != model.Planned {
		t.Fatalf("aliased metadata ignored: %+v", p.Document)
	}
}

func TestEncodeRetainsAnchorsWhenManagedFieldsChange(t *testing.T) {
	p, err := Parse("ideas.note.md", []byte("---\ntype: note\nid: &identity old-id\ncustom: *identity\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	p.Document.ID = "new-id"
	out, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	q, err := Parse("ideas.note.md", out)
	if err != nil {
		t.Fatalf("encoded invalid aliases: %v", err)
	}
	var fields map[string]any
	if err := q.Metadata.Decode(&fields); err != nil {
		t.Fatal(err)
	}
	if fields["custom"] != "new-id" {
		t.Fatalf("alias no longer references the id: %#v", fields)
	}
}

func TestEncodePreservesCommentOnlyFrontmatter(t *testing.T) {
	p, err := Parse("ideas.note.md", []byte("---\n# keep me\n---\nbody\n"))
	if err != nil {
		t.Fatal(err)
	}
	p.Document.ID = "assigned-id"
	out, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte("# keep me")) {
		t.Fatalf("comment lost: %q", out)
	}
}

func TestEncodeTaskMetadataPreservesHistoryExtras(t *testing.T) {
	p, err := Parse("work.task.md", []byte("---\ntype: task\nstate: planned\nstate_history:\n  - state: planned\n    at: 2026-10-17T10:00:00Z\n    custom: keep # event comment\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	p.Document.State = model.Completed
	p.Document.History = append(p.Document.History, model.Event{State: model.Completed, At: time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)})
	out, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	q, err := Parse("work.task.md", out)
	if err != nil {
		t.Fatal(err)
	}
	if q.Document.State != model.Completed || len(q.Document.History) != 2 || q.Document.History[1].State != model.Completed || !bytes.Contains(out, []byte("custom: keep # event comment")) {
		t.Fatalf("history update lost metadata: %q", out)
	}
}

func TestEncodeUpdatesAnAliasedMetadataKey(t *testing.T) {
	p, err := Parse("ideas.note.md", []byte("---\nkey: &key id\n*key: old-id\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	p.Document.ID = "new-id"
	out, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	q, err := Parse("ideas.note.md", out)
	if err != nil {
		t.Fatal(err)
	}
	if q.Document.ID != "new-id" {
		t.Fatalf("aliased key was not updated: %q", out)
	}
}

func TestEncodePreservesAliasedHistoryExtras(t *testing.T) {
	p, err := Parse("work.task.md", []byte("---\ntype: task\ninitial: &initial\n  state: planned\n  at: 2026-10-17T10:00:00Z\n  custom: keep\nstate: planned\nstate_history: [*initial]\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	p.Document.History = append(p.Document.History, model.Event{State: model.Completed, At: time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)})
	out, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	q, err := Parse("work.task.md", out)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := q.Metadata.Decode(&fields); err != nil {
		t.Fatal(err)
	}
	history, ok := fields["state_history"].([]any)
	if !ok || len(history) != 2 {
		t.Fatalf("history = %#v", fields["state_history"])
	}
	first, ok := history[0].(map[string]any)
	if !ok || first["custom"] != "keep" {
		t.Fatalf("history extras lost: %#v", history[0])
	}
}
