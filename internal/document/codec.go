package document

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"recall/internal/model"
)

// Parsed retains the source representation alongside its typed projection.
// Metadata is a YAML mapping node. Body is authoritative for body edits;
// Document.Body, Raw, and Revision are read-only projections. Encode also applies
// changed typed metadata fields, leaving untouched fields in Metadata alone.
type Parsed struct {
	Document model.Document
	Metadata *yaml.Node
	Body     []byte
	Newline  string
	BOM      bool

	original *source
}

type source struct {
	raw, metadata, body []byte
	document            model.Document
	newline             string
	bom, frontmatter    bool
}

// Parse validates present managed fields but does not insert missing metadata.
func Parse(path string, raw []byte) (Parsed, error) {
	kind, err := KindFromPath(path)
	if err != nil {
		return Parsed{}, err
	}
	p := Parsed{Newline: "\n", BOM: bytes.HasPrefix(raw, []byte("\xef\xbb\xbf"))}
	text := raw
	if p.BOM {
		text = text[3:]
	}
	if i := bytes.IndexByte(text, '\n'); i > 0 && text[i-1] == '\r' {
		p.Newline = "\r\n"
	}
	header, body, frontmatter, err := splitFrontmatter(text)
	if err != nil {
		return Parsed{}, invalid(path, "frontmatter is missing its closing delimiter")
	}
	p.Metadata = emptyMapping()
	if frontmatter {
		var root yaml.Node
		decoder := yaml.NewDecoder(bytes.NewReader(header))
		err := decoder.Decode(&root)
		if err != nil && !errors.Is(err, io.EOF) {
			return Parsed{}, invalid(path, "frontmatter is malformed YAML")
		}
		if err == nil {
			var extra yaml.Node
			if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
				return Parsed{}, invalid(path, "frontmatter must contain exactly one YAML mapping")
			}
		}
		if len(root.Content) != 0 {
			p.Metadata = root.Content[0]
		} else {
			// The YAML parser discards a comment-only stream with no document.
			p.Metadata.HeadComment = strings.TrimSpace(strings.ReplaceAll(string(header), "\r\n", "\n"))
		}
		// Leading/trailing comments can belong to the YAML document wrapper,
		// not the mapping. Keep them when exposing just the mapping to callers.
		p.Metadata.HeadComment = joinComments(root.HeadComment, p.Metadata.HeadComment)
		p.Metadata.FootComment = joinComments(p.Metadata.FootComment, root.FootComment)
	}
	d, err := readMetadata(path, kind, p.Metadata)
	if err != nil {
		return Parsed{}, err
	}
	d.Raw, d.Body = string(raw), string(body)
	d.Revision = fmt.Sprintf("%x", sha256.Sum256(raw))
	p.Document, p.Body = d, bytes.Clone(body)
	encoded, err := marshalMetadata(p.Metadata)
	if err != nil {
		return Parsed{}, invalid(path, "frontmatter cannot be encoded")
	}
	originalDocument := d
	originalDocument.History = append([]model.Event(nil), d.History...)
	p.original = &source{
		raw: bytes.Clone(raw), metadata: encoded, body: bytes.Clone(body),
		document: originalDocument, newline: p.Newline, bom: p.BOM, frontmatter: frontmatter,
	}
	return p, nil
}

// Encode preserves the exact source when unchanged. On metadata edits it retains
// YAML values and comments, and never reserializes the Markdown body.
func Encode(p Parsed) ([]byte, error) {
	kind, err := KindFromPath(p.Document.Path)
	if err != nil {
		return nil, err
	}
	metadata := cloneNode(p.Metadata, make(map[*yaml.Node]*yaml.Node))
	if metadata == nil {
		metadata = emptyMapping()
	}
	if metadata.Kind != yaml.MappingNode {
		return nil, invalid(p.Document.Path, "frontmatter must be a mapping")
	}
	var before model.Document
	if p.original != nil {
		before = p.original.document
	}
	if err := applyMetadata(metadata, before, p.Document); err != nil {
		return nil, err
	}
	if _, err := readMetadata(p.Document.Path, kind, metadata); err != nil {
		return nil, err
	}
	encoded, err := marshalMetadata(metadata)
	if err != nil {
		return nil, invalid(p.Document.Path, "frontmatter cannot be encoded")
	}
	newline := p.Newline
	if newline == "" {
		newline = "\n"
	}
	if newline != "\n" && newline != "\r\n" {
		return nil, invalid(p.Document.Path, "unsupported newline style")
	}
	if old := p.original; old != nil && bytes.Equal(encoded, old.metadata) && p.BOM == old.bom && newline == old.newline {
		if bytes.Equal(p.Body, old.body) {
			return bytes.Clone(old.raw), nil
		}
		// A body-only edit need not reformat even a byte of the header.
		return append(bytes.Clone(old.raw[:len(old.raw)-len(old.body)]), p.Body...), nil
	}
	var out bytes.Buffer
	if p.BOM {
		out.WriteString("\xef\xbb\xbf")
	}
	if len(metadata.Content) != 0 || (p.original != nil && p.original.frontmatter) {
		out.WriteString("---" + newline)
		if len(metadata.Content) != 0 || metadata.HeadComment != "" || metadata.FootComment != "" {
			out.Write(bytes.ReplaceAll(encoded, []byte("\n"), []byte(newline)))
		}
		out.WriteString("---" + newline)
	}
	out.Write(p.Body)
	return out.Bytes(), nil
}

func splitFrontmatter(text []byte) (header, body []byte, found bool, err error) {
	line, next := sourceLine(text, 0)
	if string(line) != "---" {
		return nil, text, false, nil
	}
	start := next
	for next < len(text) {
		position := next
		line, next = sourceLine(text, position)
		if string(line) == "---" {
			return text[start:position], text[next:], true, nil
		}
	}
	return nil, nil, true, fmt.Errorf("unclosed frontmatter")
}

func sourceLine(text []byte, start int) ([]byte, int) {
	if i := bytes.IndexByte(text[start:], '\n'); i >= 0 {
		return bytes.TrimSuffix(text[start:start+i], []byte("\r")), start + i + 1
	}
	return text[start:], len(text)
}

func readMetadata(path string, kind model.Kind, metadata *yaml.Node) (model.Document, error) {
	d := model.Document{Path: path, Kind: kind, Title: strings.TrimSuffix(filepath.Base(path), "."+string(kind)+".md")}
	if metadata.Kind != yaml.MappingNode || !uniqueKeys(metadata) {
		return d, invalid(path, "frontmatter must be a mapping with unique keys")
	}
	fields, err := mappingFields(metadata)
	if err != nil {
		return d, invalid(path, "frontmatter has invalid mapping keys or merges")
	}
	for _, field := range []struct {
		name string
		out  *string
	}{
		{"id", &d.ID}, {"title", &d.Title},
	} {
		if node := fieldValue(fields, field.name); node != nil {
			if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
				return d, invalid(path, field.name+" must be a string")
			}
			*field.out = node.Value
		}
	}
	if node := fieldValue(fields, "type"); node != nil {
		if node.Kind != yaml.ScalarNode || node.Tag != "!!str" || (node.Value != string(model.Note) && node.Value != string(model.Task)) {
			return d, invalid(path, "type must be note or task")
		}
		if node.Value != string(kind) {
			return d, &model.Error{Code: "type_mismatch", Path: path, Message: "frontmatter type conflicts with filename"}
		}
	}
	if node := fieldValue(fields, "state"); node != nil {
		if !validState(node) {
			return d, invalid(path, "state must be a supported task state")
		}
		d.State = model.State(node.Value)
	}
	if history := fieldValue(fields, "state_history"); history != nil {
		if history.Kind != yaml.SequenceNode {
			return d, invalid(path, "state_history must be a sequence")
		}
		for _, event := range history.Content {
			eventFields, err := mappingFields(event)
			if err != nil {
				return d, invalid(path, "state_history entries must be mappings")
			}
			state, at := fieldValue(eventFields, "state"), fieldValue(eventFields, "at")
			if !validState(state) || at == nil || at.Kind != yaml.ScalarNode || (at.Tag != "!!str" && at.Tag != "!!timestamp") {
				return d, invalid(path, "state_history entries require a valid state and RFC3339 timestamp")
			}
			parsed, err := time.Parse(time.RFC3339Nano, at.Value)
			if err != nil {
				return d, invalid(path, "state_history timestamps must be RFC3339 with a timezone")
			}
			d.History = append(d.History, model.Event{State: model.State(state.Value), At: parsed})
		}
	}
	return d, nil
}

func validState(node *yaml.Node) bool {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return false
	}
	switch model.State(node.Value) {
	case model.Planned, model.InProgress, model.Stalled, model.Completed:
		return true
	}
	return false
}

func uniqueKeys(node *yaml.Node) bool {
	if node.Kind == yaml.MappingNode {
		if len(node.Content)%2 != 0 {
			return false
		}
		for i := 0; i < len(node.Content); i += 2 {
			for j := 0; j < i; j += 2 {
				if sameKey(node.Content[i], node.Content[j], make(map[[2]*yaml.Node]bool)) {
					return false
				}
			}
		}
	}
	for _, child := range node.Content {
		if !uniqueKeys(child) {
			return false
		}
	}
	return true
}

func sameKey(a, b *yaml.Node, seen map[[2]*yaml.Node]bool) bool {
	a, b = resolveAlias(a), resolveAlias(b)
	if a.Kind != b.Kind || a.Tag != b.Tag || a.Value != b.Value || len(a.Content) != len(b.Content) {
		return false
	}
	pair := [2]*yaml.Node{a, b}
	if seen[pair] {
		return true
	}
	seen[pair] = true
	for i := range a.Content {
		if !sameKey(a.Content[i], b.Content[i], seen) {
			return false
		}
	}
	return true
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		name := resolveAlias(node.Content[i])
		if name.Kind == yaml.ScalarNode && name.Tag == "!!str" && name.Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// Decoding to nodes applies YAML merge precedence without coercing field values
// or losing the original node tree used for writing.
func mappingFields(node *yaml.Node) (map[string]yaml.Node, error) {
	var fields map[string]yaml.Node
	if err := node.Decode(&fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, fmt.Errorf("not a mapping")
	}
	return fields, nil
}

func fieldValue(fields map[string]yaml.Node, key string) *yaml.Node {
	node, ok := fields[key]
	if !ok {
		return nil
	}
	return resolveAlias(&node)
}

func resolveAlias(value *yaml.Node) *yaml.Node {
	seen := make(map[*yaml.Node]bool)
	for value.Kind == yaml.AliasNode && value.Alias != nil && !seen[value] {
		seen[value] = true
		value = value.Alias
	}
	return value
}

func applyMetadata(metadata *yaml.Node, before, after model.Document) error {
	for _, field := range []struct{ name, before, after string }{
		{"id", before.ID, after.ID}, {"title", before.Title, after.Title},
		{"type", string(before.Kind), string(after.Kind)}, {"state", string(before.State), string(after.State)},
	} {
		if field.before != field.after {
			setValue(metadata, field.name, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: field.after})
		}
	}
	if !reflect.DeepEqual(before.History, after.History) {
		fields, err := mappingFields(metadata)
		if err != nil {
			return invalid(after.Path, "frontmatter has invalid mapping keys or merges")
		}
		history := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		old := fieldValue(fields, "state_history")
		for i, event := range after.History {
			entry := emptyMapping()
			if old != nil && old.Kind == yaml.SequenceNode && i < len(old.Content) {
				if i < len(before.History) && before.History[i].State == event.State && before.History[i].At.Equal(event.At) {
					// Keep unchanged events intact, including aliases and extra fields.
					history.Content = append(history.Content, cloneNode(old.Content[i], make(map[*yaml.Node]*yaml.Node)))
					continue
				}
				if resolved := resolveAlias(old.Content[i]); resolved.Kind == yaml.MappingNode {
					entry = cloneNode(resolved, make(map[*yaml.Node]*yaml.Node))
					if old.Content[i].Kind == yaml.AliasNode {
						entry.Anchor = "" // The original anchor remains elsewhere in metadata.
					}
				}
			}
			setValue(entry, "state", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: string(event.State)})
			setValue(entry, "at", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!timestamp", Value: event.At.Format(time.RFC3339Nano)})
			history.Content = append(history.Content, entry)
		}
		setValue(metadata, "state_history", history)
	}
	return nil
}

func setValue(mapping *yaml.Node, key string, value *yaml.Node) {
	if old := mappingValue(mapping, key); old != nil {
		value.HeadComment, value.LineComment, value.FootComment = old.HeadComment, old.LineComment, old.FootComment
		value.Anchor = old.Anchor
		if value.Kind == old.Kind {
			value.Style = old.Style
		}
		*old = *value
		return
	}
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func cloneNode(node *yaml.Node, seen map[*yaml.Node]*yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	if copy, ok := seen[node]; ok {
		return copy
	}
	copy := *node
	seen[node] = &copy
	copy.Content = make([]*yaml.Node, len(node.Content))
	for i, child := range node.Content {
		copy.Content[i] = cloneNode(child, seen)
	}
	copy.Alias = cloneNode(node.Alias, seen)
	return &copy
}

func emptyMapping() *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
}

func joinComments(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "\n" + b
}

func marshalMetadata(node *yaml.Node) ([]byte, error) {
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(node); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func invalid(path, message string) error {
	return &model.Error{Code: "invalid_document", Path: path, Message: message}
}
