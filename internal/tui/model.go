// Package tui is Recall's keyboard-first terminal adapter. App owns all source,
// query, reconciliation, and index rules; this package owns only interaction.
package tui

import (
	"context"
	"io"
	"sync"

	tea "charm.land/bubbletea/v2"
	"recall/internal/app"
	"recall/internal/config"
	"recall/internal/model"
)

type service interface {
	List(context.Context, string, bool) (model.Snapshot, error)
	Tasks(context.Context, model.TaskQuery) (model.Page[model.Document], error)
	Search(context.Context, model.SearchQuery) ([]model.Hit, error)
	Create(context.Context, app.CreateInput) (model.Document, error)
	CreateFolder(context.Context, string) error
	Move(context.Context, model.Ref, string, string) (model.Document, error)
	MoveFolder(context.Context, string, string) error
	SetState(context.Context, model.Ref, model.State, string) (model.Document, error)
	Delete(context.Context, model.Ref, string) error
	DeleteFolder(context.Context, string, bool) error
	EditorPath(context.Context, model.Ref) (string, error)
	Sync(context.Context) (model.SyncReport, error)
	IndexPending(context.Context) error
}

type filters struct {
	folder, states, dateField, eventState, from, through, sort, mode string
	recursive, desc                                                  bool
}
type field struct{ label, value string }
type form struct {
	kind    string
	fields  []field
	active  int
	target  row
	version uint64
}
type row struct {
	id                                         string
	path, title, revision, snippet, indexState string
	kind                                       model.Kind
	state                                      model.State
	folder                                     bool
	diagnostic                                 *model.Diagnostic
}

type Model struct {
	service                                 service
	cfg                                     config.Config
	ctx                                     context.Context
	jobs                                    work
	stderr                                  io.Writer
	width, height, pane, cursor, treeCursor int
	folder, mode, searchText                string
	filters                                 filters
	snapshot                                model.Snapshot
	taskPage                                model.Page[model.Document]
	hits                                    []model.Hit
	offset                                  int
	form                                    *form
	modal                                   string
	modalGeneration                         uint64
	deleteOffset, deleteReviewed            int
	deleteWidth, deleteHeight               int
	target                                  row
	stateCursor                             int
	helpOffset                              int
	generation                              uint64
	loading, busy, indexing                 bool
	problem, indexProblem, notice           string
	watchProblem                            string
}

func NewModel(application *app.App, cfg config.Config) *Model {
	m := &Model{cfg: cfg, ctx: context.Background(), width: 80, height: 24, folder: ".", mode: "browse", filters: filters{sort: "title", mode: "hybrid", recursive: true}}
	if application != nil {
		m.service = application
	}
	return m
}

func (m *Model) Init() tea.Cmd  { return tea.Batch(m.refresh(), m.indexPending()) }
func (m *Model) View() tea.View { v := tea.NewView(m.Render()); v.AltScreen = true; return v }

// Bubble Tea intentionally doesn't join arbitrary command goroutines on exit.
// Gate our App commands so delayed commands cannot use a closed application;
// RunIO cancels, closes the gate, and joins active work before returning.
type work struct {
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func (w *work) command(fn tea.Cmd) tea.Cmd {
	return func() tea.Msg {
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return nil
		}
		w.wg.Add(1)
		w.mu.Unlock()
		defer w.wg.Done()
		return fn()
	}
}
func (w *work) stop() { w.mu.Lock(); w.closed = true; w.mu.Unlock(); w.wg.Wait() }
