package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"recall/internal/app"
	"recall/internal/config"
	"recall/internal/model"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEmptyNarrowModelRenders(t *testing.T) {
	for _, size := range [][2]int{{24, 8}, {8, 4}, {1, 1}, {80, 24}} {
		m := NewModel(nil, config.Config{})
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, k := range []string{"", "?", "esc", "n", "esc", "F"} {
			if k != "" {
				key(m, k)
			}
			got := m.Render()
			if got == "" || !utf8.ValidString(got) {
				t.Fatalf("invalid narrow view: %q", got)
			}
			if lipgloss.Width(got) > size[0] || len(strings.Split(got, "\n")) > size[1] {
				t.Fatalf("overflow %v: %q", size, got)
			}
		}
	}
}

func TestNarrowSelectedDiagnosticRetainsCodeAndRecoveryControls(t *testing.T) {
	m := NewModel(nil, config.Config{})
	m.Update(tea.WindowSizeMsg{Width: 24, Height: 8})
	m.pane = 1
	m.snapshot.Diagnostics = []model.Diagnostic{{Path: "a very long filename.note.md", Code: "invalid_document", Message: "frontmatter is malformed"}}
	got := m.Render()
	if !strings.Contains(got, "invalid_document") || !strings.Contains(got, "repair") {
		t.Fatalf("narrow diagnostics not actionable: %s", got)
	}
}

func TestNarrowFormsShowActiveValueAndHelpScrolls(t *testing.T) {
	m := NewModel(nil, config.Config{})
	m.Update(tea.WindowSizeMsg{Width: 24, Height: 8})
	key(m, "F")
	key(m, "ctrl+u")
	key(m, "work folder")
	if !strings.Contains(m.Render(), "work folder") {
		t.Fatalf("active input hidden: %s", m.Render())
	}
	key(m, "esc")
	key(m, "?")
	for range 20 {
		key(m, "j")
	}
	if !strings.Contains(m.Render(), "Invalid files") {
		t.Fatalf("cannot reach help: %s", m.Render())
	}
}

func TestDeleteConfirmationShowsEscapedDistinctTargets(t *testing.T) {
	m, a, _ := fixture(t)
	for _, name := range []string{"a\nb", "ab", "a\x1bb"} {
		if _, err := a.Create(t.Context(), app.CreateInput{Kind: model.Note, Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	load(m)
	m.pane = 1
	views := map[string]bool{}
	for _, r := range m.rows() {
		for i, candidate := range m.rows() {
			if candidate.path == r.path {
				m.cursor = i
			}
		}
		key(m, "d")
		got := m.Render()
		if !strings.Contains(got, strconv.QuoteToASCII(r.path)) || !strings.Contains(got, "FILE ONLY") {
			t.Fatalf("target/scope not safely visible: %q", got)
		}
		if views[got] {
			t.Fatalf("distinct targets look identical: %q", got)
		}
		views[got] = true
		key(m, "esc")
	}
	for i, r := range m.rows() {
		if r.path == "a\nb.note.md" {
			m.cursor = i
		}
	}
	key(m, "d")
	execute(m, key(m, "y"))
	if _, err := a.Read(t.Context(), model.Ref{Path: "a\nb.note.md"}); model.ErrorCode(err) != "not_found" {
		t.Fatalf("escaped target was not deleted exactly: %v", err)
	}
	for _, path := range []string{"ab.note.md", "a\x1bb.note.md"} {
		if _, err := a.Read(t.Context(), model.Ref{Path: path}); err != nil {
			t.Fatalf("wrong sibling deleted: %q %v", path, err)
		}
	}
}

func TestLongSiblingDeleteTargetsCanBeInspectedAtNarrowWidth(t *testing.T) {
	m, a, _ := fixture(t)
	for _, name := range []string{strings.Repeat("prefix", 20) + "LEFT", strings.Repeat("prefix", 20) + "RIGHT"} {
		if _, err := a.Create(t.Context(), app.CreateInput{Kind: model.Note, Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	load(m)
	m.pane = 1
	m.Update(tea.WindowSizeMsg{Width: 24, Height: 8})
	for i, tail := range []string{"LEFT.note.md", "RIGHT.note.md"} {
		m.cursor = i
		key(m, "d")
		if cmd := key(m, "y"); cmd != nil {
			t.Fatal("accepted delete before target tail was accessible")
		}
		var frames string
		for range 200 {
			frames += m.Render() + "\n"
			key(m, "j")
		}
		if !strings.Contains(strings.ReplaceAll(frames, "\n", ""), tail) {
			t.Fatalf("target tail cannot be inspected: %s", frames)
		}
		key(m, "esc")
	}
}
