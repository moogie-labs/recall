package document

import (
	"bytes"
	"time"

	"go.yaml.in/yaml/v3"

	"recall/internal/model"
)

// Normalize fills missing managed metadata and records an observed task-state
// change. It validates all present fields before assigning an ID and never
// changes the body or mutates the caller's metadata/history nodes. changed
// reports normalization changes, not edits already present in p.
func Normalize(p Parsed, now time.Time, newID func() string) (Parsed, bool, error) {
	// Honor the codec's edit precedence and authoritative Body, then reproject
	// direct metadata edits. Re-parsing also gives us a private node tree.
	raw, err := Encode(p)
	if err != nil {
		return p, false, err
	}
	q, err := Parse(p.Document.Path, raw)
	if err != nil {
		return p, false, err
	}
	fields, err := mappingFields(q.Metadata)
	if err != nil {
		return p, false, invalid(q.Document.Path, "frontmatter has invalid mapping keys or merges")
	}
	if fieldValue(fields, "type") == nil {
		setValue(q.Metadata, "type", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: string(q.Document.Kind)})
	}
	if q.Document.ID == "" {
		if newID == nil {
			return p, false, invalid(q.Document.Path, "an ID generator is required for adoption")
		}
		q.Document.ID = newID()
		if q.Document.ID == "" {
			return p, false, invalid(q.Document.Path, "generated ID must not be empty")
		}
	}
	if q.Document.Kind == model.Task {
		d := &q.Document
		if d.State == "" {
			if len(d.History) != 0 {
				d.State = d.History[len(d.History)-1].State
			} else {
				d.State = model.Planned
			}
		}
		if len(d.History) == 0 || d.History[len(d.History)-1].State != d.State {
			d.History = append(d.History, model.Event{State: d.State, At: now.UTC()})
		}
	}
	out, err := Encode(q)
	if err != nil {
		return p, false, err
	}
	q, err = Parse(q.Document.Path, out)
	if err != nil {
		return p, false, err
	}
	// Encode intentionally retains anchor relationships when managed fields
	// change. Automatic normalization must not thereby change existing values
	// that reference those anchors. Refuse the write instead of changing the
	// codec's explicit-edit contract or silently rewriting user metadata.
	after, err := mappingFields(q.Metadata)
	if err != nil {
		return p, false, invalid(q.Document.Path, "frontmatter has invalid mapping keys or merges")
	}
	for name, before := range fields {
		switch name {
		case "id", "type", "state", "state_history":
			continue
		}
		value, ok := after[name]
		if !ok || !sameKey(&before, &value, make(map[[2]*yaml.Node]bool)) {
			return p, false, invalid(q.Document.Path, "normalization would change existing YAML metadata; edit managed metadata explicitly")
		}
	}
	if history := fieldValue(fields, "state_history"); history != nil {
		latest := fieldValue(after, "state_history")
		for i, entry := range history.Content {
			if latest == nil || i >= len(latest.Content) || !sameHistoryEntry(entry, latest.Content[i]) {
				return p, false, invalid(q.Document.Path, "normalization would change existing YAML history; edit managed metadata explicitly")
			}
		}
	}
	return q, !bytes.Equal(raw, out), nil
}

func sameHistoryEntry(a, b *yaml.Node) bool {
	before, err := mappingFields(resolveAlias(a))
	if err != nil {
		return false
	}
	after, err := mappingFields(resolveAlias(b))
	if err != nil || len(before) != len(after) {
		return false
	}
	for name := range before {
		x, y := fieldValue(before, name), fieldValue(after, name)
		if y == nil {
			return false
		}
		// Both accepted timestamp tags represent the same managed RFC3339 value.
		if name == "at" && x.Value == y.Value {
			continue
		}
		if !sameKey(x, y, make(map[[2]*yaml.Node]bool)) {
			return false
		}
	}
	return true
}
