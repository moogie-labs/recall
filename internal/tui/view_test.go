package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"recall/internal/config"
	"recall/internal/model"
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
