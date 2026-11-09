package document

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"recall/internal/model"
)

func TestAdoptionWritesOnlyTheID(t *testing.T) {
	p, err := Parse("work.md", []byte("description\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, changed, err := Normalize(p, func() string { return "fixed-id" })
	if err != nil || !changed || got.Document.ID != "fixed-id" || got.Document.Title != "work" || string(got.Body) != "description\n" {
		t.Fatalf("%+v changed=%v err=%v", got.Document, changed, err)
	}
	if want := "---\nid: fixed-id\n---\ndescription\n"; got.Document.Raw != want {
		t.Fatalf("adopted source = %q, want %q", got.Document.Raw, want)
	}
}

func TestNormalizeLeavesRetiredTaskKeysAlone(t *testing.T) {
	for _, raw := range []string{
		"---\nid: existing\ntype: note\nstate: completed\nstate_history: [{state: planned, at: 2026-10-17T10:00:00Z}]\n---\nbody\n",
		"---\nid: existing\ntype: task\nstate: not-a-state\nstate_history: nonsense\n---\nbody\n",
	} {
		p, err := Parse("work.md", []byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		got, changed, err := Normalize(p, func() string { t.Fatal("existing ID replaced"); return "" })
		if err != nil || changed || got.Document.Raw != raw {
			t.Fatalf("source rewritten: %q changed=%v err=%v", got.Document.Raw, changed, err)
		}
	}
}

func TestNormalizePreservesNoteSourceAndDoesNotMutateInput(t *testing.T) {
	raw := "\xef\xbb\xbf---\r\n# keep\r\ncustom: &items [café, 7, null]\r\ncopy: *items\r\n---\r\nbody\r\n---\r\n"
	p, err := Parse("old.md", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	got, changed, err := Normalize(p, func() string { return "fixed-id" })
	if err != nil || !changed {
		t.Fatalf("normalize note: %+v %v", got.Document, err)
	}
	if !strings.HasPrefix(got.Document.Raw, "\xef\xbb\xbf---\r\n") || string(got.Body) != "body\r\n---\r\n" || !strings.Contains(got.Document.Raw, "# keep") || !strings.Contains(got.Document.Raw, "*items") {
		t.Fatalf("source lost: %q", got.Document.Raw)
	}
	original, err := Encode(p)
	if err != nil || string(original) != raw {
		t.Fatalf("input mutated: %q %v", original, err)
	}
}

func TestNormalizeValidatesEditsBeforeAssigningID(t *testing.T) {
	p, err := Parse("work.md", []byte("---\ntitle: original\n---\nbody"))
	if err != nil {
		t.Fatal(err)
	}
	setValue(p.Metadata, "title", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: "7"})
	_, changed, err := Normalize(p, func() string { t.Fatal("assigned ID before validation"); return "fixed-id" })
	if model.ErrorCode(err) != "invalid_document" || changed {
		t.Fatalf("invalid title accepted: changed=%v err=%v", changed, err)
	}
}

func TestNormalizeUsesAuthoritativeBodyAndTypedEdits(t *testing.T) {
	p, err := Parse("work.md", []byte("---\nid: existing\ntitle: planned\n---\nold"))
	if err != nil {
		t.Fatal(err)
	}
	p.Body = []byte("latest\r\n---\r\n")
	p.Document.Body = "stale projection"
	p.Document.Title = "typed"
	setValue(p.Metadata, "title", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "direct"})
	got, _, err := Normalize(p, func() string { return "unused" })
	if err != nil || got.Document.Title != "typed" || string(got.Body) != "latest\r\n---\r\n" {
		t.Fatalf("authoritative edits lost: %+v %v", got.Document, err)
	}
}

func TestNormalizeRejectsEmptyGeneratedID(t *testing.T) {
	p, err := Parse("old.md", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, changed, err := Normalize(p, func() string { return "" })
	if model.ErrorCode(err) != "invalid_document" || changed {
		t.Fatalf("empty identity accepted: changed=%v err=%v", changed, err)
	}
}

func TestNormalizePreservesManagedAnchorReferences(t *testing.T) {
	for _, tt := range []struct {
		name, metadata, anchorKey, aliasKey, reference string
	}{
		{"unknown ID alias", "id: &identity ''\ncustom: *identity\n", "id", "custom", "identity"},
		{"title ID alias", "id: &identity ''\ntitle: *identity\n", "id", "title", "identity"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := "---\n" + tt.metadata + "---\ncafé\r\n---\r\n"
			p, err := Parse("work.md", []byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			got, changed, err := Normalize(p, func() string { return "fixed-id" })
			if err != nil || !changed {
				t.Fatalf("valid alias blocked adoption: changed=%v err=%v", changed, err)
			}
			alias, anchor := mappingValue(got.Metadata, tt.aliasKey), mappingValue(got.Metadata, tt.anchorKey)
			if alias == nil || anchor == nil || alias.Kind != yaml.AliasNode || alias.Value != tt.reference || alias.Alias != anchor || anchor.Anchor != tt.reference {
				t.Fatalf("alias reference detached or retargeted: alias=%+v anchor=%+v", alias, anchor)
			}
			if tt.anchorKey == "id" && anchor.Value != "fixed-id" {
				t.Fatalf("ID anchor was not adopted: %+v", anchor)
			}
			if string(got.Body) != "café\r\n---\r\n" {
				t.Fatalf("body changed: %q", got.Body)
			}
			original, err := Encode(p)
			if err != nil || string(original) != raw {
				t.Fatalf("input changed: %q %v", original, err)
			}
			again, changed, err := Normalize(got, func() string { t.Fatal("ID reassigned"); return "" })
			if err != nil || changed || again.Document.Raw != got.Document.Raw {
				t.Fatalf("repeat adoption changed source: changed=%v err=%v", changed, err)
			}
		})
	}
}

func TestPreservationComparisonChecksReferencesAndScalarSemantics(t *testing.T) {
	for _, tt := range []struct {
		name, before, after string
		want                bool
	}{
		{"follows anchor", "id: &id ''\ncustom: *id\n", "id: &id adopted\ncustom: *id\n", true},
		{"nested reference", "id: &id ''\ncustom: {ref: *id, value: keep}\n", "id: &id adopted\ncustom: {ref: *id, value: keep}\n", true},
		{"different reference", "a: &a same\nb: &b same\ncustom: *a\n", "a: &a same\nb: &b same\ncustom: *b\n", false},
		{"expanded reference", "id: &id same\ncustom: *id\n", "id: &id same\ncustom: same\n", false},
		{"changed scalar tag", "custom: !!timestamp '2026-10-17T10:00:00Z'\n", "custom: !!str '2026-10-17T10:00:00Z'\n", false},
		{"changed scalar value", "custom: keep\n", "custom: changed\n", false},
		{"nested scalar tag", "custom: {value: !!int 7}\n", "custom: {value: !!str '7'}\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before, err := Parse("old.md", []byte("---\n"+tt.before+"---\n"))
			if err != nil {
				t.Fatal(err)
			}
			after, err := Parse("old.md", []byte("---\n"+tt.after+"---\n"))
			if err != nil {
				t.Fatal(err)
			}
			if got := samePreservedNode(mappingValue(before.Metadata, "custom"), mappingValue(after.Metadata, "custom")); got != tt.want {
				t.Fatalf("preservation=%v, want %v", got, tt.want)
			}
		})
	}
}
