package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
	"recall/internal/app"
	"recall/internal/config"
	"recall/internal/editor"
	"recall/internal/model"
	"recall/internal/syncer"
	"recall/internal/tasks"
)

func Run(ctx context.Context, application *app.App, cfg config.Config) error {
	return RunIO(ctx, application, cfg, os.Stdin, os.Stdout, os.Stderr)
}

// IsTerminal checks the supplied streams, never a hidden /dev/tty fallback.
func IsTerminal(input io.Reader, output io.Writer) bool {
	in, ok := input.(interface{ Fd() uintptr })
	if !ok {
		return false
	}
	out, ok := output.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(in.Fd()) && term.IsTerminal(out.Fd())
}

// RunIO leaves App and streams owned by the caller and preserves injected IO for
// both Bubble Tea and the editor (Exec's default stderr is otherwise os.Stderr).
func RunIO(ctx context.Context, application *app.App, cfg config.Config, input io.Reader, output, stderr io.Writer) error {
	if !IsTerminal(input, output) {
		return &model.Error{Code: "runtime_unavailable", Message: "TUI requires terminal input and output; use a CLI subcommand or --help"}
	}
	ctx, cancel := context.WithCancel(ctx)
	m := NewModel(application, cfg)
	m.ctx = ctx
	m.stderr = stderr
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output))
	var watcher sync.WaitGroup
	if application != nil {
		watcher.Add(1)
		go func() {
			defer watcher.Done()
			runWatcher(ctx, application, cfg.Content, p.Send)
		}()
	}
	_, err := p.Run()
	cancel()
	watcher.Wait()
	m.jobs.stop()
	return err
}

func runWatcher(ctx context.Context, application service, content string, send func(tea.Msg)) {
	err := syncer.Run(ctx, content, func(ctx context.Context) error { _, err := application.Sync(ctx); send(syncMsg{err}); return err }, func(err error) { send(syncMsg{err}) })
	if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return
	}
	if err == nil {
		err = &model.Error{Code: "internal", Message: "watcher returned unexpectedly"}
	}
	send(watcherStoppedMsg{err})
}

type loadedMsg struct {
	generation uint64
	metadata   bool
	snapshot   model.Snapshot
	page       model.Page[model.Document]
	hits       []model.Hit
	err        error
}
type operationMsg struct {
	err             error
	form            *form
	version         uint64
	modal           string
	modalGeneration uint64
}
type indexMsg struct{ err error }
type syncMsg struct{ err error }
type watcherStoppedMsg struct{ err error }
type editorReadyMsg struct {
	command *exec.Cmd
	err     error
}

func invalid(message string) error { return &model.Error{Code: "invalid_argument", Message: message} }
func problem(err error) string {
	if err == nil {
		return ""
	}
	var e *model.Error
	if errors.As(err, &e) {
		text := e.Code + ": " + e.Message
		if e.Path != "" {
			text = e.Path + ": " + text
		}
		return text
	}
	if errors.Is(err, context.Canceled) {
		return "operation canceled"
	}
	return "internal: operation failed; retry or use recall sync"
}

func (m *Model) taskQuery() (model.TaskQuery, error) {
	f := m.filters
	query := model.TaskQuery{Filter: model.Filter{Folder: f.folder, Recursive: f.recursive}, Sort: f.sort, Desc: f.desc, Limit: 50, Offset: m.offset}
	if f.sort != "title" && f.sort != "created" && f.sort != "state_changed" {
		return query, invalid("sort: title, created, or state_changed")
	}
	for _, s := range strings.Split(f.states, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !validState(model.State(s)) {
			return query, invalid("states: planned, in_progress, stalled, completed")
		}
		query.Filter.States = append(query.Filter.States, model.State(s))
	}
	if f.dateField != "" && f.dateField != "created" && f.dateField != "state_changed" && f.dateField != "transition" {
		return query, invalid("date field: created, state_changed, or transition")
	}
	if f.eventState != "" && !validState(model.State(f.eventState)) {
		return query, invalid("event state must be a supported task state")
	}
	if f.dateField == "transition" && f.eventState == "" {
		return query, invalid("transition dates require an event state")
	}
	if (f.from != "" || f.through != "") && f.dateField == "" {
		return query, invalid("date bounds require a date field")
	}
	query.Filter.DateField = model.DateField(f.dateField)
	query.Filter.EventState = model.State(f.eventState)
	loc := time.Local
	if m.cfg.Timezone != "" {
		var err error
		loc, err = time.LoadLocation(m.cfg.Timezone)
		if err != nil {
			return query, invalid("invalid collection timezone")
		}
	}
	var err error
	query.Filter.From, query.Filter.Until, err = tasks.DateRange(f.from, f.through, loc)
	return query, err
}

func (m *Model) refresh() tea.Cmd {
	if m.service == nil {
		return nil
	}
	m.generation++
	generation := m.generation
	a, ctx, mode, text := m.service, m.ctx, m.mode, m.searchText
	query, err := m.taskQuery()
	searchMode := m.filters.mode
	if err != nil {
		m.problem = problem(err)
		return nil
	}
	if searchMode != "hybrid" && searchMode != "keyword" {
		m.problem = problem(invalid("search mode: hybrid or keyword"))
		return nil
	}
	m.loading = true
	return m.jobs.command(func() tea.Msg {
		r := loadedMsg{generation: generation}
		r.snapshot, r.err = a.List(ctx, "", true)
		r.metadata = r.err == nil
		if r.err == nil {
			switch mode {
			case "tasks":
				r.page, r.err = a.Tasks(ctx, query)
			case "search":
				r.hits, r.err = a.Search(ctx, model.SearchQuery{Filter: query.Filter, Text: text, Mode: searchMode, Limit: 20})
			}
		}
		return r
	})
}

func (m *Model) indexPending() tea.Cmd {
	if m.service == nil || m.indexing {
		return nil
	}
	m.indexing = true
	a, ctx := m.service, m.ctx
	return m.jobs.command(func() tea.Msg { return indexMsg{a.IndexPending(ctx)} })
}

func (m *Model) mutation(change func(context.Context, service) error) tea.Cmd {
	if m.service == nil || m.busy {
		return nil
	}
	m.busy = true
	a, ctx := m.service, m.ctx
	f, modal, modalGeneration := m.form, m.modal, m.modalGeneration
	var version uint64
	if f != nil {
		version = f.version
	}
	return m.jobs.command(func() tea.Msg {
		return operationMsg{err: change(ctx, a), form: f, version: version, modal: modal, modalGeneration: modalGeneration}
	})
}

func (m *Model) prepareEditor(target row) tea.Cmd {
	if m.service == nil || m.busy {
		return nil
	}
	m.busy = true
	a, ctx, argv := m.service, m.ctx, append([]string(nil), m.cfg.Editor...)
	return m.jobs.command(func() tea.Msg {
		path, err := a.EditorPath(ctx, model.Ref{Path: target.path})
		if err != nil {
			return editorReadyMsg{err: err}
		}
		cmd, err := editor.Command(argv, path)
		if err != nil {
			return editorReadyMsg{err: err}
		}
		cmd = exec.CommandContext(ctx, cmd.Args[0], cmd.Args[1:]...)
		cmd.WaitDelay = 2 * time.Second
		return editorReadyMsg{command: cmd}
	})
}

type externalEditor struct {
	*exec.Cmd
	ctx     context.Context
	service service
}

func (e *externalEditor) SetStdin(r io.Reader) {
	if e.Stdin == nil {
		e.Stdin = r
	}
}
func (e *externalEditor) SetStdout(w io.Writer) {
	if e.Stdout == nil {
		e.Stdout = w
	}
}
func (e *externalEditor) SetStderr(w io.Writer) {
	if e.Stderr == nil {
		e.Stderr = w
	}
}
func (e *externalEditor) Run() error {
	err := e.Cmd.Run()
	if err != nil {
		err = errors.Join(&model.Error{Code: "internal", Message: "editor failed; saved changes reconciled where possible"}, e.ctx.Err())
	}
	return editor.Complete(e.ctx, err, func(ctx context.Context) error { _, err := e.service.Sync(ctx); return err })
}
func (m *Model) editorProcess(cmd *exec.Cmd) *externalEditor {
	cmd.Stderr = m.stderr
	return &externalEditor{cmd, m.ctx, m.service}
}

func boolField(value string) (bool, error) {
	b, err := strconv.ParseBool(value)
	if err != nil {
		return false, invalid("recursive/descending must be true or false")
	}
	return b, nil
}
