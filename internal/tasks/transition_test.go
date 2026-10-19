package tasks

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"recall/internal/document"
	"recall/internal/model"
)

var transitionAt = time.Date(2026, 10, 19, 12, 30, 45, 123456789, time.FixedZone("local", -4*60*60))

func TestTransitionAllStatePairs(t *testing.T) {
	states := []model.State{model.Planned, model.InProgress, model.Stalled, model.Completed}
	for _, from := range states {
		for _, to := range states {
			t.Run(string(from)+"/"+string(to), func(t *testing.T) {
				raw := fmt.Sprintf("---\nid: existing\ntype: task\nstate : '%s' # keep exact layout\nstate_history: [{state: %s, at: 2026-10-18T09:00:00Z}]\n---\nDescription\n---\n", from, from)
				p := parseTask(t, raw)
				before := append([]model.Event(nil), p.Document.History...)
				got, changed, err := Transition(p, to, transitionAt)
				if err != nil || changed != (from != to) {
					t.Fatalf("changed=%v err=%v", changed, err)
				}
				if got.Document.State != to || string(got.Body) != "Description\n---\n" {
					t.Fatalf("state/body: %+v", got.Document)
				}
				wantCount := 2
				if from == to {
					wantCount = 1
					if got.Document.Raw != raw || got.Document.Revision != p.Document.Revision {
						t.Fatal("same-state request changed source bytes or revision")
					}
				}
				if len(got.Document.History) != wantCount || !reflect.DeepEqual(got.Document.History[:1], before) {
					t.Fatalf("history=%+v", got.Document.History)
				}
				if changed {
					event := got.Document.History[1]
					if event.State != to || !event.At.Equal(transitionAt) || event.At.Location() != time.UTC {
						t.Fatalf("new event is not the requested UTC transition: %+v", event)
					}
					if !strings.Contains(got.Document.Raw, "2026-10-19T16:30:45.123456789Z") {
						t.Fatal("UTC timestamp was not persisted at full precision")
					}
				}
				out, err := document.Encode(got)
				if err != nil || string(out) != got.Document.Raw {
					t.Fatalf("returned metadata/source disagree: %q %v", out, err)
				}
				original, err := document.Encode(p)
				if err != nil || string(original) != raw || !reflect.DeepEqual(p.Document.History, before) {
					t.Fatalf("transition mutated its input: %q %v", original, err)
				}
			})
		}
	}
}

func TestTransitionReopenRetainsCompletionHistory(t *testing.T) {
	p := parseTask(t, "---\nid: existing\nstate: planned\nstate_history: [{state: planned, at: 2026-10-18T09:00:00Z}]\n---\n")
	for i, state := range []model.State{model.Completed, model.InProgress, model.Completed, model.Stalled, model.Planned} {
		var changed bool
		var err error
		p, changed, err = Transition(p, state, transitionAt.Add(time.Duration(i)*time.Minute))
		if err != nil || !changed {
			t.Fatalf("transition %d: changed=%v err=%v", i, changed, err)
		}
	}
	want := []model.State{model.Planned, model.Completed, model.InProgress, model.Completed, model.Stalled, model.Planned}
	if len(p.Document.History) != len(want) {
		t.Fatalf("history=%+v", p.Document.History)
	}
	for i, state := range want {
		if p.Document.History[i].State != state {
			t.Fatalf("history[%d]=%+v, want %s", i, p.Document.History[i], state)
		}
		if i > 0 && !p.Document.History[i].At.Equal(transitionAt.Add(time.Duration(i-1)*time.Minute)) {
			t.Fatalf("history[%d] timestamp changed", i)
		}
	}
}

func TestTransitionRejectsUnknownRequestedStates(t *testing.T) {
	for _, state := range []model.State{"", "Completed", "in progress", "deleted", "SECRET"} {
		t.Run(string(state), func(t *testing.T) {
			p := parseTask(t, "---\nstate: planned\n---\nbody")
			got, changed, err := Transition(p, state, transitionAt)
			if model.ErrorCode(err) != "invalid_argument" || changed {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
			if strings.Contains(err.Error(), "SECRET") || got.Document.Raw != p.Document.Raw {
				t.Fatal("invalid request exposed content or changed source")
			}
		})
	}
}

func TestTransitionValidatesSourceBeforeNoOp(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*testing.T, *document.Parsed)
	}{
		{"invalid typed state", func(t *testing.T, p *document.Parsed) { p.Document.State = "SECRET" }},
		{"invalid metadata state", func(t *testing.T, p *document.Parsed) { metadataValue(t, p.Metadata, "state").Value = "SECRET" }},
		{"invalid history", func(t *testing.T, p *document.Parsed) {
			*metadataValue(t, p.Metadata, "state_history") = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "SECRET"}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := parseTask(t, "---\nstate: planned\nstate_history: []\n---\nbody")
			tt.edit(t, &p)
			_, changed, err := Transition(p, model.Planned, transitionAt)
			if model.ErrorCode(err) != "invalid_document" || changed {
				t.Fatalf("invalid source accepted: changed=%v err=%v", changed, err)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatal("source content exposed in error")
			}
		})
	}
}

func TestTransitionRejectsMissingCurrentStateAndNotes(t *testing.T) {
	for _, tt := range []struct{ path, raw, code string }{
		{"work.task.md", "description", "invalid_document"},
		{"ideas.note.md", "---\nstate: planned\n---\nnote body", "type_mismatch"},
	} {
		t.Run(tt.path, func(t *testing.T) {
			p, err := document.Parse(tt.path, []byte(tt.raw))
			if err != nil {
				t.Fatal(err)
			}
			_, changed, err := Transition(p, model.Planned, transitionAt)
			if model.ErrorCode(err) != tt.code || changed {
				t.Fatalf("changed=%v err=%v, want %s", changed, err, tt.code)
			}
		})
	}
}

func TestTransitionPreservesMetadataHistoryAndSourceStyle(t *testing.T) {
	raw := "\xef\xbb\xbf---\r\nid: existing\r\ntype: task\r\ntitle: Café guide\r\nstate: planned # state comment\r\nstate_history:\r\n  - &initial {state: planned, at: 2026-10-18T10:00:00+02:00, audit: {when: 2026-10-18T08:00:00Z, text: !!str '2026-10-18T08:00:00Z'}} # event comment\r\ncustom: &details {when: 2026-10-18T08:00:00Z, text: !!str '2026-10-18T08:00:00Z', values: [café, 7, null]} # custom comment\r\ncopy: *details\r\nold_event: *initial\r\n---\r\nold description"
	p := parseTask(t, raw)
	p.Body = []byte("latest café\r\n---\r\nno final newline")
	p.Document.Body = "stale projection, not authoritative"
	beforeRaw, err := document.Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	before := decodedMetadata(t, p)
	got, changed, err := Transition(p, model.Completed, transitionAt)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !bytes.Equal(got.Body, p.Body) || got.Document.Body != string(p.Body) || !strings.HasPrefix(got.Document.Raw, "\xef\xbb\xbf---\r\n") {
		t.Fatalf("body or source style changed: %q", got.Document.Raw)
	}
	if got.Document.ID != "existing" || got.Document.Title != "Café guide" || got.Document.Kind != model.Task {
		t.Fatalf("unrelated managed fields changed: %+v", got.Document)
	}
	after := decodedMetadata(t, got)
	for _, key := range []string{"custom", "copy", "old_event"} {
		if !reflect.DeepEqual(before[key], after[key]) {
			t.Fatalf("%s semantics changed: %#v -> %#v", key, before[key], after[key])
		}
	}
	if !reflect.DeepEqual(before["state_history"].([]any)[0], after["state_history"].([]any)[0]) {
		t.Fatal("existing event metadata changed")
	}
	for _, comment := range []string{"# state comment", "# event comment", "# custom comment"} {
		if !strings.Contains(got.Document.Raw, comment) {
			t.Fatalf("lost %s", comment)
		}
	}
	if got.Document.Revision != fmt.Sprintf("%x", sha256.Sum256([]byte(got.Document.Raw))) {
		t.Fatal("returned revision does not match new source")
	}
	original, err := document.Encode(p)
	if err != nil || !bytes.Equal(original, beforeRaw) {
		t.Fatalf("input metadata/body mutated: %q %v", original, err)
	}
}

func TestTransitionPreservesManagedAnchorReferences(t *testing.T) {
	p := parseTask(t, "---\nstate: &status planned\nstate_history: &events [{state: planned, at: 2026-10-18T09:00:00Z}]\nstatus_copy: *status\nevents_copy: *events\n---\n")
	got, changed, err := Transition(p, model.Completed, transitionAt)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	for _, key := range []string{"state", "state_history"} {
		aliasKey := "status_copy"
		if key == "state_history" {
			aliasKey = "events_copy"
		}
		anchor, alias := metadataValue(t, got.Metadata, key), metadataValue(t, got.Metadata, aliasKey)
		if alias.Kind != yaml.AliasNode || alias.Alias != anchor || alias.Value != anchor.Anchor {
			t.Fatalf("%s reference detached: %+v", aliasKey, alias)
		}
	}
	values := decodedMetadata(t, got)
	if values["status_copy"] != "completed" || len(values["events_copy"].([]any)) != 2 {
		t.Fatal("aliases did not follow updated managed anchors")
	}
}

func TestTransitionHonorsMergedStateWithoutChangingDefaults(t *testing.T) {
	p := parseTask(t, "---\ndefaults: &defaults {state: planned, state_history: [{state: planned, at: 2026-10-18T09:00:00Z, custom: keep}]}\n<<: *defaults\n---\nbody")
	before := decodedMetadata(t, p)
	got, changed, err := Transition(p, model.Stalled, transitionAt)
	if err != nil || !changed || got.Document.State != model.Stalled || len(got.Document.History) != 2 {
		t.Fatalf("merged transition: %+v changed=%v err=%v", got.Document, changed, err)
	}
	if !reflect.DeepEqual(before["defaults"], decodedMetadata(t, got)["defaults"]) {
		t.Fatal("transition rewrote unrelated YAML merge defaults")
	}
}

func parseTask(t *testing.T, raw string) document.Parsed {
	t.Helper()
	p, err := document.Parse("work.task.md", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func metadataValue(t *testing.T, mapping *yaml.Node, key string) *yaml.Node {
	t.Helper()
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	t.Fatalf("missing metadata key %s", key)
	return nil
}

func decodedMetadata(t *testing.T, p document.Parsed) map[string]any {
	t.Helper()
	var values map[string]any
	if err := p.Metadata.Decode(&values); err != nil {
		t.Fatal(err)
	}
	return values
}
