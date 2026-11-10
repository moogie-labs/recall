package document

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"recall/internal/model"
)

func TestCodecPreservesBody(t *testing.T) {
	raw := []byte("---\r\ntype: note\r\ncustom: [a, b]\r\n---\r\n# café\r\n---\r\n")
	p, err := Parse("ideas.md", raw)
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
			p, err := Parse("ideas.md", []byte(raw))
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

func TestParseProjectsDocument(t *testing.T) {
	raw := "---\nid: existing-id\ntitle: Write café guide\ncustom: anything\n---\nDescription\n---\n"
	p, err := Parse("folder/guide.md", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	d := p.Document
	if d.ID != "existing-id" || d.Title != "Write café guide" || d.Path != "folder/guide.md" {
		t.Fatalf("unexpected metadata: %+v", d)
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
		p, err := Parse("work.md", []byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if p.Document.ID != "" {
			t.Fatalf("missing fields were normalized: %+v", p.Document)
		}
	}
}

// Notes written before tasks were removed can carry these keys; they are now
// ordinary unknown frontmatter, whatever their values are.
func TestRetiredTaskKeysRoundTripByteForByte(t *testing.T) {
	for _, raw := range []string{
		"---\nid: legacy\ntype: note\ntitle: Old\nstate: completed\nstate_history:\n  - state: planned\n    at: 2026-10-17T10:00:00Z\n---\nbody\n",
		"---\r\nid: legacy # keep\r\ntype : 'task'\r\nstate: SECRET\r\nstate_history: {not: a sequence}\r\n---\r\nbody\r\n",
		"---\nid: legacy\nstate: null\nstate_history: null\n---\n",
	} {
		t.Run(fmt.Sprintf("%q", raw), func(t *testing.T) {
			p, err := Parse("old.md", []byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			if p.Document.ID != "legacy" {
				t.Fatalf("id = %q", p.Document.ID)
			}
			out, err := Encode(p)
			if err != nil || string(out) != raw {
				t.Fatalf("round-trip = %q, want %q (%v)", out, raw, err)
			}
			p.Body = []byte("edited")
			out, err = Encode(p)
			header := raw[:len(raw)-len(p.Document.Body)]
			if err != nil || string(out) != header+"edited" {
				t.Fatalf("body edit changed header: %q (%v)", out, err)
			}
		})
	}
}

func TestParseRejectsInvalidDocuments(t *testing.T) {
	tests := []struct {
		name, path, raw, code string
	}{
		{"not markdown", "ideas.txt", "---\ntype: note\n---\n", "invalid_document"},
		{"not markdown body", "ideas.txt", "plain body", "invalid_document"},
		{"empty name", ".md", "body", "invalid_document"},
		{"uppercase suffix", "ideas.MD", "body", "invalid_document"},
		{"malformed YAML", "ideas.md", "---\nprivate: [SECRET\n---\n", "invalid_document"},
		{"trailing YAML after end", "ideas.md", "---\ntype: note\n...\nSECRET\n---\n", "invalid_document"},
		{"missing delimiter", "ideas.md", "---\ntype: note\n", "invalid_document"},
		{"sequence metadata", "ideas.md", "---\n- note\n---\n", "invalid_document"},
		{"scalar metadata", "ideas.md", "---\nSECRET\n---\n", "invalid_document"},
		{"duplicate type", "ideas.md", "---\ntype: note\ntype: note\n---\n", "invalid_document"},
		{"duplicate unknown key", "ideas.md", "---\ncustom: one\ncustom: two\n---\n", "invalid_document"},
		{"duplicate aliased key", "ideas.md", "---\n&key custom: one\n*key: two\n---\n", "invalid_document"},
		{"nested duplicate key", "ideas.md", "---\ncustom: {x: one, x: two}\n---\n", "invalid_document"},
		{"invalid merge", "ideas.md", "---\n<<: SECRET\n---\n", "invalid_document"},
		{"id number", "ideas.md", "---\nid: 42\n---\n", "invalid_document"},
		{"title map", "ideas.md", "---\ntitle: {SECRET: value}\n---\n", "invalid_document"},
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
	p, err := Parse("ideas.md", []byte(raw))
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
	q, err := Parse("ideas.md", out)
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
	p, err := Parse("ideas.md", []byte("---\ntype: note\ncustom: old\n---\nold body\n"))
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
	q, err := Parse("ideas.md", out)
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
	p, err := Parse("work.md", []byte("description\n"))
	if err != nil {
		t.Fatal(err)
	}
	p.Document.ID = "new-id"
	out, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	q, err := Parse("work.md", out)
	if err != nil {
		t.Fatal(err)
	}
	if q.Document.ID != "new-id" || string(q.Body) != "description\n" {
		t.Fatalf("unexpected metadata after encode: %+v", q.Document)
	}
}

func TestEncodeRejectsInvalidMetadataChanges(t *testing.T) {
	p, err := Parse("ideas.md", []byte("---\ntype: note\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	setValue(p.Metadata, "id", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: "7"})
	if _, err := Encode(p); err == nil || model.ErrorCode(err) != "invalid_document" {
		t.Fatalf("Encode error = %v, want invalid_document", err)
	}
}

func TestEncodePreservesBodyOnlyHeaderFormatting(t *testing.T) {
	raw := "---\r\ntype : 'note' # keep exact layout\r\ncustom:    [  a, b  ]\r\n---\r\nold body"
	p, err := Parse("ideas.md", []byte(raw))
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
			p, err := Parse("ideas.md", []byte(tt.raw))
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
			q, err := Parse("ideas.md", out)
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
	p, err := Parse("work.md", []byte("---\nbase: &base {id: existing-id}\n<<: *base\nname: &name Aliased\ntitle: *name\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Document.ID != "existing-id" || p.Document.Title != "Aliased" {
		t.Fatalf("aliased metadata ignored: %+v", p.Document)
	}
}

func TestEncodeRetainsAnchorsWhenManagedFieldsChange(t *testing.T) {
	p, err := Parse("ideas.md", []byte("---\ntype: note\nid: &identity old-id\ncustom: *identity\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	p.Document.ID = "new-id"
	out, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	q, err := Parse("ideas.md", out)
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
	p, err := Parse("ideas.md", []byte("---\n# keep me\n---\nbody\n"))
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

func TestEncodeUpdatesAnAliasedMetadataKey(t *testing.T) {
	p, err := Parse("ideas.md", []byte("---\nkey: &key id\n*key: old-id\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	p.Document.ID = "new-id"
	out, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	q, err := Parse("ideas.md", out)
	if err != nil {
		t.Fatal(err)
	}
	if q.Document.ID != "new-id" {
		t.Fatalf("aliased key was not updated: %q", out)
	}
}

func TestEncodePreservesTimestampSemanticTypes(t *testing.T) {
	for _, tt := range []struct {
		name, scalar, tag, value string
	}{
		{"implicit timestamp", "2026-10-17T10:00:00Z", "!!timestamp", "2026-10-17T10:00:00Z"},
		{"explicit timestamp", "!!timestamp 2026-10-17T10:00:00Z", "!!timestamp", "2026-10-17T10:00:00Z"},
		{"explicit quoted timestamp", "!!timestamp '2026-10-17T10:00:00+02:00'", "!!timestamp", "2026-10-17T10:00:00+02:00"},
		{"implicit date", "2026-10-17", "!!timestamp", "2026-10-17"},
		{"quoted string", "'2026-10-17T10:00:00Z'", "!!str", "2026-10-17T10:00:00Z"},
		{"explicit string", "!!str 2026-10-17T10:00:00Z", "!!str", "2026-10-17T10:00:00Z"},
		{"custom tag", "!user-date 2026-10-17T10:00:00Z", "!user-date", "2026-10-17T10:00:00Z"},
	} {
		for _, placement := range []string{"block", "flow mapping", "flow sequence"} {
			t.Run(tt.name+"/"+placement, func(t *testing.T) {
				metadata := "custom:\r\n  at: " + tt.scalar + "\r\n"
				switch placement {
				case "flow mapping":
					metadata = "custom: {at: " + tt.scalar + "}\r\n"
				case "flow sequence":
					metadata = "custom: [" + tt.scalar + "]\r\n"
				}
				raw := "\xef\xbb\xbf---\r\n" + metadata + "---\r\ncafé\r\n---\r\n"
				p, err := Parse("old.md", []byte(raw))
				if err != nil {
					t.Fatal(err)
				}
				unchanged, err := Encode(p)
				if err != nil || string(unchanged) != raw {
					t.Fatalf("untouched source changed: %q %v", unchanged, err)
				}
				p.Document.ID = "fixed-id" // Force metadata serialization.
				out, err := Encode(p)
				if err != nil {
					t.Fatal(err)
				}
				q, err := Parse("old.md", out)
				if err != nil {
					t.Fatal(err)
				}
				custom := mappingValue(q.Metadata, "custom")
				var at *yaml.Node
				if custom.Kind == yaml.SequenceNode {
					at = custom.Content[0]
				} else {
					at = mappingValue(custom, "at")
				}
				if at.Tag != tt.tag || at.Value != tt.value {
					t.Fatalf("unrelated scalar semantics changed: tag=%q value=%q, want %q %q; source=%q", at.Tag, at.Value, tt.tag, tt.value, out)
				}
				if !q.BOM || q.Newline != "\r\n" || string(q.Body) != "café\r\n---\r\n" {
					t.Fatalf("source/body changed: %q", out)
				}
			})
		}
	}
}
