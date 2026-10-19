package document

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"recall/internal/model"
)

var observedAt = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

func TestTaskAdoptionDefaults(t *testing.T) {
	p, err := Parse("work.task.md", []byte("description\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, changed, err := Normalize(p, observedAt, func() string { return "fixed-id" })
	if err != nil || !changed || got.Document.State != model.Planned || len(got.Document.History) != 1 {
		t.Fatalf("%+v changed=%v err=%v", got.Document, changed, err)
	}
	if string(got.Body) != "description\n" || got.Document.ID != "fixed-id" || got.Document.Title != "work" || !got.Document.History[0].At.Equal(observedAt) {
		t.Fatalf("adoption lost defaults or body: %+v", got.Document)
	}
	fields, err := mappingFields(got.Metadata)
	if err != nil || fieldValue(fields, "type") == nil || fieldValue(fields, "type").Value != "task" {
		t.Fatalf("type not persisted: %+v %v", fields, err)
	}
	if fieldValue(fields, "title") != nil {
		t.Fatal("optional fallback title was written")
	}
}

func TestNormalizePreservesExistingTaskInformation(t *testing.T) {
	for _, tt := range []struct {
		name, metadata string
		state          model.State
		count          int
		lastAt         time.Time
	}{
		{"explicit state", "state: completed\n", model.Completed, 1, observedAt},
		{"history supplies state", "state_history: [{state: stalled, at: 2026-10-17T10:00:00+02:00}]\n", model.Stalled, 1, time.Date(2026, 10, 17, 8, 0, 0, 0, time.UTC)},
		{"observed transition", "state: in_progress\nstate_history: [{state: planned, at: 2026-10-17T10:00:00Z, custom: keep}]\n", model.InProgress, 2, observedAt},
		{"matching final event", "state: stalled\nstate_history: [{state: stalled, at: 2026-10-17T10:00:00Z}]\n", model.Stalled, 1, time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)},
		{"empty history", "state: completed\nstate_history: []\n", model.Completed, 1, observedAt},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := "---\nid: existing\n" + tt.metadata + "custom: [one, two]\n---\ncafé\n---\n"
			p, err := Parse("work.task.md", []byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			got, changed, err := Normalize(p, observedAt, func() string { t.Fatal("existing ID replaced"); return "" })
			if err != nil || !changed || got.Document.ID != "existing" || got.Document.State != tt.state || len(got.Document.History) != tt.count {
				t.Fatalf("normalize: %+v changed=%v err=%v", got.Document, changed, err)
			}
			if !got.Document.History[tt.count-1].At.Equal(tt.lastAt) || string(got.Body) != "café\n---\n" {
				t.Fatalf("history/body changed: %+v", got.Document)
			}
			out, err := Encode(got)
			if err != nil || !bytes.Contains(out, []byte("custom:")) {
				t.Fatalf("unknown fields lost: %q %v", out, err)
			}
			again, changed, err := Normalize(got, observedAt.Add(time.Hour), func() string { t.Fatal("stable ID replaced"); return "" })
			if err != nil || changed || again.Document.Raw != string(out) || len(again.Document.History) != tt.count {
				t.Fatalf("repeated normalization changed history: %+v changed=%v err=%v", again.Document, changed, err)
			}
			if tt.name == "observed transition" && !bytes.Contains(out, []byte("custom: keep")) {
				t.Fatalf("existing event metadata lost: %q", out)
			}
		})
	}
}

func TestNormalizePreservesNoteSourceAndDoesNotMutateInput(t *testing.T) {
	raw := "\xef\xbb\xbf---\r\n# keep\r\ncustom: &items [café, 7, null]\r\ncopy: *items\r\n---\r\nbody\r\n---\r\n"
	p, err := Parse("old.note.md", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	got, changed, err := Normalize(p, observedAt, func() string { return "fixed-id" })
	if err != nil || !changed || got.Document.Kind != model.Note || got.Document.State != "" || len(got.Document.History) != 0 {
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
	p, err := Parse("work.task.md", []byte("---\nstate_history: []\n---\nbody"))
	if err != nil {
		t.Fatal(err)
	}
	setValue(p.Metadata, "state_history", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "SECRET"})
	_, changed, err := Normalize(p, observedAt, func() string { t.Fatal("assigned ID before validation"); return "fixed-id" })
	if model.ErrorCode(err) != "invalid_document" || changed || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("invalid history accepted or exposed: changed=%v err=%v", changed, err)
	}
}

func TestNormalizeUsesAuthoritativeBodyAndTypedEdits(t *testing.T) {
	p, err := Parse("work.task.md", []byte("---\nid: existing\nstate: planned\n---\nold"))
	if err != nil {
		t.Fatal(err)
	}
	p.Body = []byte("latest\r\n---\r\n")
	p.Document.Body = "stale projection"
	p.Document.State = model.Completed
	setValue(p.Metadata, "state", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "stalled"})
	got, _, err := Normalize(p, observedAt, func() string { return "unused" })
	if err != nil || got.Document.State != model.Completed || len(got.Document.History) != 1 || got.Document.History[0].State != model.Completed || string(got.Body) != "latest\r\n---\r\n" {
		t.Fatalf("authoritative edits lost: %+v %v", got.Document, err)
	}
}

func TestNormalizeRejectsEmptyGeneratedID(t *testing.T) {
	p, err := Parse("old.note.md", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, changed, err := Normalize(p, observedAt, func() string { return "" })
	if model.ErrorCode(err) != "invalid_document" || changed {
		t.Fatalf("empty identity accepted: changed=%v err=%v", changed, err)
	}
}

func TestNormalizePreservesManagedAnchorReferences(t *testing.T) {
	for _, tt := range []struct {
		name, metadata, anchorKey, aliasKey, reference string
		inHistory                                      bool
	}{
		{"unknown ID alias", "id: &identity ''\ncustom: *identity\n", "id", "custom", "identity", false},
		{"title ID alias", "id: &identity ''\ntitle: *identity\n", "id", "title", "identity", false},
		{"event ID alias", "id: &identity ''\nstate: planned\nstate_history: [{state: planned, at: 2026-10-17T10:00:00Z, custom: *identity}]\n", "id", "custom", "identity", true},
		{"history alias", "id: existing\nstate: completed\nstate_history: &events [{state: planned, at: 2026-10-17T10:00:00Z}]\ncustom: *events\n", "state_history", "custom", "events", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := "---\n" + tt.metadata + "---\ncafé\r\n---\r\n"
			p, err := Parse("work.task.md", []byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			got, changed, err := Normalize(p, observedAt, func() string { return "fixed-id" })
			if err != nil || !changed {
				t.Fatalf("valid alias blocked adoption: changed=%v err=%v", changed, err)
			}
			parent := got.Metadata
			if tt.inHistory {
				parent = mappingValue(parent, "state_history").Content[0]
			}
			alias, anchor := mappingValue(parent, tt.aliasKey), mappingValue(got.Metadata, tt.anchorKey)
			if alias == nil || anchor == nil || alias.Kind != yaml.AliasNode || alias.Value != tt.reference || alias.Alias != anchor || anchor.Anchor != tt.reference {
				t.Fatalf("alias reference detached or retargeted: alias=%+v anchor=%+v", alias, anchor)
			}
			if tt.anchorKey == "id" && anchor.Value != "fixed-id" {
				t.Fatalf("ID anchor was not adopted: %+v", anchor)
			}
			if tt.anchorKey == "state_history" && len(anchor.Content) != 2 {
				t.Fatalf("history alias did not follow appended event: %+v", anchor)
			}
			if string(got.Body) != "café\r\n---\r\n" {
				t.Fatalf("body changed: %q", got.Body)
			}
			original, err := Encode(p)
			if err != nil || string(original) != raw {
				t.Fatalf("input changed: %q %v", original, err)
			}
			again, changed, err := Normalize(got, observedAt.Add(time.Hour), func() string { t.Fatal("ID reassigned"); return "" })
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
			before, err := Parse("old.note.md", []byte("---\n"+tt.before+"---\n"))
			if err != nil {
				t.Fatal(err)
			}
			after, err := Parse("old.note.md", []byte("---\n"+tt.after+"---\n"))
			if err != nil {
				t.Fatal(err)
			}
			if got := samePreservedNode(mappingValue(before.Metadata, "custom"), mappingValue(after.Metadata, "custom")); got != tt.want {
				t.Fatalf("preservation=%v, want %v", got, tt.want)
			}
		})
	}
}
