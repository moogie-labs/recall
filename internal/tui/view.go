package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"recall/internal/model"
)

func stateLabel(s model.State) string {
	switch s {
	case model.Planned:
		return "Planned"
	case model.InProgress:
		return "In progress"
	case model.Stalled:
		return "Stalled"
	case model.Completed:
		return "Completed"
	}
	return ""
}

// Strip controls/escape sequences in authored names, snippets and diagnostics.
// Rendering user files must never execute terminal controls.
func display(s string) string { return cleanText(ansi.Strip(s)) }
func (m *Model) tree() []string {
	folders := append([]string(nil), m.snapshot.Folders...)
	sort.Strings(folders)
	if len(folders) == 0 || folders[0] != "." {
		folders = append([]string{"."}, folders...)
	}
	return folders
}
func scoped(path, folder string, recursive bool) bool {
	parent := filepath.Dir(path)
	return folder == "" || parent == folder || recursive && (folder == "." || strings.HasPrefix(parent, folder+"/"))
}

func (m *Model) rows() []row {
	var rows []row
	switch m.mode {
	case "browse":
		for _, path := range m.snapshot.Folders {
			if path != "." && filepath.Dir(path) == m.folder {
				rows = append(rows, row{path: path, title: filepath.Base(path), folder: true})
			}
		}
		for _, d := range m.snapshot.Documents {
			if filepath.Dir(d.Path) == m.folder {
				rows = append(rows, documentRow(d))
			}
		}
		sort.SliceStable(rows, func(i, j int) bool {
			a, b := rows[i], rows[j]
			if a.folder != b.folder {
				return a.folder
			}
			if a.title != b.title {
				return a.title < b.title
			}
			if a.id != b.id {
				return a.id < b.id
			}
			return a.path < b.path
		})
	case "tasks":
		for _, d := range m.taskPage.Items {
			rows = append(rows, documentRow(d))
		}
	case "search":
		for _, h := range m.hits {
			rows = append(rows, row{id: h.ID, path: h.Path, title: h.Title, kind: h.Kind, state: h.State, snippet: h.Snippet, indexState: h.IndexState})
		}
	}
	for _, d := range m.snapshot.Diagnostics {
		folder, recursive := m.filters.folder, m.filters.recursive
		if m.mode == "browse" {
			folder = m.folder
			recursive = false
		}
		if scoped(d.Path, folder, recursive) {
			diagnostic := d
			rows = append(rows, row{path: d.Path, title: d.Path, diagnostic: &diagnostic})
		}
	}
	return rows
}
func documentRow(d model.Document) row {
	return row{id: d.ID, path: d.Path, title: d.Title, kind: d.Kind, state: d.State, revision: d.Revision}
}
func (m *Model) selected() (row, bool) {
	if m.pane == 0 {
		tree := m.tree()
		if m.treeCursor < len(tree) {
			return row{path: tree[m.treeCursor], folder: true}, true
		}
	} else {
		rows := m.rows()
		if m.cursor < len(rows) {
			return rows[m.cursor], true
		}
	}
	return row{}, false
}

const help = `Recall — keyboard help
j/k or arrows: navigate   Tab: switch tree/list
Enter: open folder or edit ACTUAL source
t: mixed browser / task list (global by default)
/: search   F: task/search filters   Ctrl+r: retry/refresh
n: new note   N: new task   f: new folder
r: rename/move (full relative destination)
s: task state   d: permanent delete confirmation
[/]: previous/next 50 tasks; search shows top 20
q/Esc: back to browser, parent folder, then quit
?: help   Ctrl+c: quit/cancel editor
Forms: type normally (q is text); Tab/Up/Down fields
Enter submits; Ctrl+u clears; Esc/Ctrl+g cancels
Filters: empty folder=global, .=root, named=scoped
States: planned,in_progress,stalled,completed
Dates: local inclusive YYYY-MM-DD in collection timezone
Date field: created / state_changed / transition
Transition requires event state; blank dates are unbounded
Sort: title / created / state_changed; descending true/false
Search: hybrid or keyword (no silent fallback)
Invalid files: select diagnostic, Enter to repair or d to delete
Folder deletion includes ALL descendants; no trash
Help: q/Esc closes`

func (m *Model) Render() string {
	w, h := max(1, m.width), max(1, m.height)
	status := m.notice
	for _, r := range m.rows() {
		if r.diagnostic != nil {
			status = "Invalid source: select in list, Enter to repair; d to delete"
			break
		}
	}
	if m.loading {
		status = "query pending"
	}
	if m.indexing {
		status += " | indexing pending"
	}
	if m.busy {
		status += " | saving/editor pending"
	}
	if m.indexProblem != "" {
		status += " | index: " + m.indexProblem + " (keyword available; Ctrl+r retries)"
	}
	if m.problem != "" {
		status = "Error: " + m.problem + " | " + status
	}
	title := "Recall | " + m.mode + " | " + display(m.folder)
	if m.mode != "browse" {
		folder := m.filters.folder
		if folder == "" {
			folder = "global"
		}
		title += " | " + display(folder) + " | " + m.filters.sort
		if m.filters.desc {
			title += " desc"
		} else {
			title += " asc"
		}
		if m.mode == "search" {
			title += " | " + m.filters.mode
		}
	}
	var body []string
	available := max(1, h-3)
	switch {
	case m.form != nil:
		f := m.form
		title = "[" + f.kind + "] " + f.fields[f.active].label
		if available > 1 {
			body = append(body, fmt.Sprintf("Field %d/%d | Tab: next", f.active+1, len(f.fields)))
		}
		value := display(f.fields[f.active].value)
		if excess := lipgloss.Width(value) - max(1, w-2); excess > 0 {
			value = ansi.TruncateLeft(value, excess+1, "…")
		}
		body = append(body, "> "+value)
		body = append(body, "Enter: submit | Esc/Ctrl+g: cancel", "Ctrl+u: clear field; q is text")
	case m.modal == "help":
		all := strings.Split(help, "\n")
		start := min(m.helpOffset, max(0, len(all)-available))
		body = all[start:min(len(all), start+available)]
	case m.modal == "delete":
		title = "DELETE permanently? y=yes n=no"
		body = []string{display(m.target.path), "y: delete once | n/q/Esc: cancel"}
		if m.target.folder {
			body = append(body, "Includes every descendant. No trash/undo.")
		}
	case m.modal == "state":
		title = "Task state | Enter: save, Esc: cancel"
		body = []string{"Task state | j/k select, Enter saves, q/Esc cancels"}
		for i, state := range states {
			mark := "  "
			if i == m.stateCursor {
				mark = "> "
			}
			body = append(body, mark+stateLabel(state))
		}
		if available < 5 {
			body = []string{"> " + stateLabel(states[m.stateCursor]), "j/k: select | Enter: save"}
		}
	default:
		tree := m.tree()
		left := make([]string, len(tree))
		for i, path := range tree {
			mark := "  "
			if i == m.treeCursor {
				mark = "> "
			}
			left[i] = mark + strings.Repeat(" ", strings.Count(path, "/")) + display(path) + "/"
		}
		rows := m.rows()
		right := make([]string, len(rows))
		for i, row := range rows {
			mark := "  "
			if i == m.cursor {
				mark = "> "
			}
			label := "[" + string(row.kind) + "] " + display(row.title)
			if row.folder {
				label = display(row.title) + "/"
			}
			if row.state != "" {
				label += " — " + stateLabel(row.state)
			}
			if row.indexState != "" {
				label += " [" + display(row.indexState) + "]"
			}
			if row.diagnostic != nil {
				label = "! " + display(row.path) + ": " + display(row.diagnostic.Code) + " — " + display(row.diagnostic.Message) + " (Enter: repair; d: delete)"
			}
			right[i] = mark + label
		}
		if len(right) == 0 {
			right = []string{"(empty) n: note / N: task / f: folder"}
		}
		var details []string
		if selected, ok := m.selected(); ok && !selected.folder {
			if d := selected.diagnostic; d != nil {
				details = []string{display(d.Code), display(d.Message), "Enter: repair | d: delete"}
			} else if selected.snippet != "" {
				details = strings.Split(ansi.Wrap("Passage: "+display(selected.snippet), w, ""), "\n")
			} else {
				details = []string{display(selected.path)}
			}
		}
		details = details[:min(len(details), max(0, available-1))]
		listHeight := max(1, available-len(details))
		if w < 60 {
			if m.pane == 0 {
				body = window(left, m.treeCursor, listHeight)
			} else {
				body = window(right, m.cursor, listHeight)
			}
		} else {
			lw := min(28, w/3)
			l := window(left, m.treeCursor, listHeight)
			r := window(right, m.cursor, listHeight)
			for i := 0; i < max(len(l), len(r)); i++ {
				a, b := "", ""
				if i < len(l) {
					a = l[i]
				}
				if i < len(r) {
					b = r[i]
				}
				body = append(body, lipgloss.NewStyle().Width(lw).Render(ansi.Truncate(a, lw, "…"))+" │ "+ansi.Truncate(b, w-lw-3, "…"))
			}
		}
		if len(body) == 0 {
			body = []string{"(empty)"}
		}
		body = append(body, details...)
	}
	footer := "Tab: pane | Enter: open | ?: help | q: back/quit"
	if m.form != nil {
		footer = "Esc: cancel | Enter: submit | Tab: field"
		if m.busy {
			footer = "Esc: close form (submitted action continues)"
		}
	}
	if m.modal == "help" {
		footer = "j/k: scroll help | q/Esc: close"
	}
	if m.pane == 0 {
		title += " | TREE"
	} else {
		title += " | LIST"
	}
	if m.mode == "tasks" && m.form == nil && m.modal == "" {
		footer = fmt.Sprintf("%d tasks (offset %d) | [/] page | F filters | ? help", len(m.taskPage.Items), m.offset)
	}
	lines := []string{title}
	if h > 2 {
		lines = append(lines, body[:min(len(body), max(0, h-3))]...)
	}
	if h > 1 {
		lines = append(lines, status)
	}
	if h > 2 {
		lines = append(lines, footer)
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(display(line), w, "…")
	}
	return strings.Join(lines[:min(len(lines), h)], "\n")
}

func window(lines []string, cursor, height int) []string {
	start := max(0, cursor-height+1)
	start = min(start, len(lines))
	return lines[start:min(len(lines), start+height)]
}
