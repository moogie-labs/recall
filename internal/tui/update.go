package tui

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"recall/internal/app"
	"recall/internal/model"
)

var states = []model.State{model.Planned, model.InProgress, model.Stalled, model.Completed}

func validState(s model.State) bool {
	for _, state := range states {
		if s == state {
			return true
		}
	}
	return false
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(1, msg.Width)
		m.height = max(1, msg.Height)
		if m.modal == "delete" {
			m.resetDeleteReview()
		}
	case loadedMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.problem = problem(msg.err)
		}
		if msg.metadata {
			m.snapshot = msg.snapshot
		}
		if msg.err == nil {
			m.taskPage = msg.page
			m.hits = msg.hits
			m.cursor = min(m.cursor, max(0, len(m.rows())-1))
			m.treeCursor = min(m.treeCursor, max(0, len(m.tree())-1))
		}
	case indexMsg:
		m.indexing = false
		m.indexProblem = problem(msg.err)
	case watcherStoppedMsg:
		m.watchProblem = problem(msg.err)
	case syncMsg:
		if msg.err != nil {
			m.problem = problem(msg.err)
			return m, nil
		}
		return m, tea.Batch(m.refresh(), m.indexPending())
	case operationMsg:
		m.busy = false
		m.problem = problem(msg.err)
		if msg.err == nil {
			if m.form == msg.form && (m.form == nil || m.form.version == msg.version) {
				m.form = nil
			}
			if m.modal == msg.modal && m.modalGeneration == msg.modalGeneration {
				m.modal = ""
			}
			m.notice = "Saved"
		}
		return m, tea.Batch(m.refresh(), m.indexPending())
	case editorReadyMsg:
		if msg.err != nil {
			m.busy = false
			m.problem = problem(msg.err)
			return m, nil
		}
		return m, tea.Exec(m.editorProcess(msg.command), func(err error) tea.Msg { return operationMsg{err: err} })
	case tea.PasteMsg:
		if m.form != nil {
			m.form.fields[m.form.active].value += cleanText(msg.Content)
			m.form.version++
		}
	case tea.KeyPressMsg:
		key := msg.String()
		if key == "ctrl+c" {
			return m, tea.Quit
		}
		if m.form != nil {
			return m, m.formKey(msg)
		}
		if m.modal != "" {
			return m, m.modalKey(key)
		}
		switch key {
		case "q", "esc":
			if m.mode != "browse" {
				m.mode = "browse"
				m.cursor = 0
				return m, m.refresh()
			}
			if m.folder != "." {
				m.folder = filepath.Dir(m.folder)
				m.cursor = 0
				return m, nil
			}
			return m, tea.Quit
		case "tab":
			m.pane = 1 - m.pane
		case "j", "down":
			if m.pane == 0 {
				m.treeCursor = min(m.treeCursor+1, max(0, len(m.tree())-1))
			} else {
				m.cursor = min(m.cursor+1, max(0, len(m.rows())-1))
			}
		case "k", "up":
			if m.pane == 0 {
				m.treeCursor = max(0, m.treeCursor-1)
			} else {
				m.cursor = max(0, m.cursor-1)
			}
		case "enter":
			target, ok := m.selected()
			if !ok {
				return m, nil
			}
			if target.folder {
				m.folder = target.path
				m.mode = "browse"
				m.pane = 1
				m.cursor = 0
				return m, nil
			}
			return m, m.prepareEditor(target)
		case "t":
			m.offset = 0
			m.cursor = 0
			m.pane = 1
			if m.mode == "tasks" {
				m.mode = "browse"
			} else {
				m.mode = "tasks"
			}
			return m, m.refresh()
		case "/":
			m.form = &form{kind: "search", fields: []field{{"Query", m.searchText}}}
		case "n", "N":
			kind := "note"
			if key == "N" {
				kind = "task"
			}
			m.form = &form{kind: kind, fields: []field{{"Bare name", ""}, {"Title (optional)", ""}, {"Folder", m.folder}}}
		case "f":
			m.form = &form{kind: "folder", fields: []field{{"Full folder path", ""}}}
		case "r":
			if target, ok := m.selected(); ok {
				m.form = &form{kind: "move", target: target, fields: []field{{"Full destination path", target.path}}}
			}
		case "s":
			if target, ok := m.selected(); ok && target.kind == model.Task && target.diagnostic == nil {
				m.target = target
				m.openModal("state")
				m.stateCursor = 0
			}
		case "d":
			if target, ok := m.selected(); ok && target.path != "." {
				m.target = target
				m.openModal("delete")
			}
		case "F":
			m.openFilters()
		case "?":
			m.openModal("help")
			m.helpOffset = 0
		case "]":
			if m.mode == "tasks" && m.taskPage.NextOffset != nil {
				m.offset = *m.taskPage.NextOffset
				m.cursor = 0
				return m, m.refresh()
			}
		case "[":
			if m.mode == "tasks" {
				m.offset = max(0, m.offset-50)
				m.cursor = 0
				return m, m.refresh()
			}
		case "ctrl+r":
			m.problem = ""
			return m, tea.Batch(m.refresh(), m.indexPending())
		}
	}
	return m, nil
}

func cleanText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func (m *Model) formKey(msg tea.KeyPressMsg) tea.Cmd {
	f := m.form
	switch msg.String() {
	case "esc", "ctrl+g":
		m.form = nil
		m.problem = ""
		return nil
	case "tab", "down":
		f.active = (f.active + 1) % len(f.fields)
		return nil
	case "shift+tab", "up":
		f.active = (f.active + len(f.fields) - 1) % len(f.fields)
		return nil
	case "backspace":
		f.version++
		r := []rune(f.fields[f.active].value)
		if len(r) > 0 {
			f.fields[f.active].value = string(r[:len(r)-1])
		}
		return nil
	case "ctrl+u":
		f.version++
		f.fields[f.active].value = ""
		return nil
	case "enter":
		if m.busy {
			return nil
		}
		return m.submitForm()
	}
	if msg.Text != "" {
		f.fields[f.active].value += cleanText(msg.Text)
		f.version++
	}
	return nil
}

func (m *Model) submitForm() tea.Cmd {
	f := m.form
	value := func(i int) string { return f.fields[i].value }
	switch f.kind {
	case "search":
		m.problem = ""
		m.searchText = value(0)
		m.mode = "search"
		m.pane = 1
		m.cursor = 0
		m.form = nil
		return m.refresh()
	case "filters":
		recursive, err := boolField(value(1))
		if err != nil {
			m.problem = problem(err)
			return nil
		}
		desc, err := boolField(value(9))
		if err != nil {
			m.problem = problem(err)
			return nil
		}
		old := m.filters
		m.filters = filters{folder: value(0), recursive: recursive, states: value(2), dateField: value(3), eventState: value(4), from: value(5), through: value(6), sort: value(8), desc: desc, mode: value(7)}
		if _, err := m.taskQuery(); err != nil {
			m.filters = old
			m.problem = problem(err)
			return nil
		}
		if m.filters.mode != "hybrid" && m.filters.mode != "keyword" {
			m.filters = old
			m.problem = problem(invalid("search mode: hybrid or keyword"))
			return nil
		}
		m.form = nil
		m.offset = 0
		m.cursor = 0
		if m.mode == "browse" {
			m.mode = "tasks"
			m.pane = 1
		}
		return m.refresh()
	case "note", "task":
		input := app.CreateInput{Kind: model.Kind(f.kind), Name: value(0), Title: value(1), Folder: value(2)}
		return m.mutation(func(ctx context.Context, a service) error { _, err := a.Create(ctx, input); return err })
	case "folder":
		path := value(0)
		return m.mutation(func(ctx context.Context, a service) error { return a.CreateFolder(ctx, path) })
	case "move":
		target, destination := f.target, value(0)
		return m.mutation(func(ctx context.Context, a service) error {
			if target.folder {
				return a.MoveFolder(ctx, target.path, destination)
			}
			_, err := a.Move(ctx, model.Ref{Path: target.path}, destination, target.revision)
			return err
		})
	}
	return nil
}

func (m *Model) modalKey(key string) tea.Cmd {
	if key == "q" || key == "esc" || key == "ctrl+g" || m.modal == "delete" && key == "n" {
		m.modal = ""
		return nil
	}
	if m.busy {
		return nil
	}
	switch m.modal {
	case "help":
		if key == "j" || key == "down" {
			m.helpOffset = min(m.helpOffset+1, max(0, len(strings.Split(help, "\n"))-max(1, m.height-3)))
		}
		if key == "k" || key == "up" {
			m.helpOffset = max(0, m.helpOffset-1)
		}
	case "delete":
		switch key {
		case "j", "down":
			m.scrollDelete(1)
		case "k", "up":
			m.scrollDelete(-1)
		case "space", "pgdown":
			m.scrollDelete(max(1, m.height-3))
		case "pgup":
			m.scrollDelete(-max(1, m.height-3))
		}
		if key == "y" && m.deleteReady() {
			target := m.target
			m.modal = ""
			return m.mutation(func(ctx context.Context, a service) error {
				if target.folder {
					return a.DeleteFolder(ctx, target.path, true)
				}
				return a.Delete(ctx, model.Ref{Path: target.path}, target.revision)
			})
		}
	case "state":
		switch key {
		case "j", "down":
			m.stateCursor = (m.stateCursor + 1) % len(states)
		case "k", "up":
			m.stateCursor = (m.stateCursor + len(states) - 1) % len(states)
		case "enter":
			target, state := m.target, states[m.stateCursor]
			return m.mutation(func(ctx context.Context, a service) error {
				_, err := a.SetState(ctx, model.Ref{Path: target.path}, state, target.revision)
				return err
			})
		}
	}
	return nil
}

func (m *Model) openModal(kind string) {
	m.modalGeneration++
	m.modal = kind
	if kind == "delete" {
		m.resetDeleteReview()
	}
}

func (m *Model) resetDeleteReview() {
	m.deleteWidth, m.deleteHeight = m.width, m.height
	m.deleteOffset, m.deleteReviewed = 0, 0
	m.scrollDelete(0)
}

func (m *Model) scrollDelete(delta int) {
	lines := m.deleteLines()
	page := max(1, m.height-3)
	m.deleteOffset = max(0, min(m.deleteOffset+delta, max(0, len(lines)-page)))
	if m.width >= 24 && m.height >= 6 {
		m.deleteReviewed = max(m.deleteReviewed, min(len(lines), m.deleteOffset+page))
	}
}

func (m *Model) deleteReady() bool {
	return m.width >= 24 && m.height >= 6 && m.deleteWidth == m.width && m.deleteHeight == m.height && m.deleteReviewed >= len(m.deleteLines())
}

func (m *Model) openFilters() {
	f := m.filters
	m.form = &form{kind: "filters", fields: []field{
		{"Folder (empty=global, .=root)", f.folder}, {"Recursive (true/false)", strconv.FormatBool(f.recursive)},
		{"States (comma separated)", f.states}, {"Date field (created/state_changed/transition)", f.dateField},
		{"Event state (transition)", f.eventState}, {"From (YYYY-MM-DD)", f.from}, {"Through (YYYY-MM-DD)", f.through},
		{"Search mode (hybrid/keyword)", f.mode}, {"Sort (title/created/state_changed)", f.sort}, {"Descending (true/false)", strconv.FormatBool(f.desc)},
	}}
}
