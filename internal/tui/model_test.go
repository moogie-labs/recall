package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"recall/internal/app"
	"recall/internal/config"
	"recall/internal/fsstore"
	"recall/internal/index"
	"recall/internal/model"
	"recall/internal/search"
)

func fixture(t *testing.T) (*Model, *app.App, config.Config) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cfg, err := config.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Timezone = "America/New_York"
	if err = config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	m := NewModel(a, cfg)
	m.ctx = t.Context()
	return m, a, cfg
}

func key(m *Model, s string) tea.Cmd {
	code := rune(0)
	text := s
	switch s {
	case "enter":
		code = tea.KeyEnter
		text = ""
	case "esc":
		code = tea.KeyEscape
		text = ""
	case "tab":
		code = tea.KeyTab
		text = ""
	case "backspace":
		code = tea.KeyBackspace
		text = ""
	}
	if code == 0 {
		code = []rune(s)[0]
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: code, Text: text})
	return cmd
}

func execute(m *Model, cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	_, next := m.Update(cmd())
	return next
}
func load(m *Model) { execute(m, m.refresh()) }

func TestMixedFoldersStatesAndScopedTaskQueries(t *testing.T) {
	m, a, _ := fixture(t)
	if err := a.CreateFolder(t.Context(), "work"); err != nil {
		t.Fatal(err)
	}
	for _, in := range []app.CreateInput{{Kind: model.Note, Name: "note"}, {Kind: model.Task, Name: "root", State: model.Planned}, {Kind: model.Task, Folder: "work", Name: "progress", State: model.InProgress}, {Kind: model.Task, Folder: "work", Name: "stalled", State: model.Stalled}, {Kind: model.Task, Folder: "work", Name: "done", State: model.Completed}} {
		if _, err := a.Create(t.Context(), in); err != nil {
			t.Fatal(err)
		}
	}
	load(m)
	if got := m.Render(); !strings.Contains(got, "note") || !strings.Contains(got, "root") || !strings.Contains(got, "work") {
		t.Fatal(got)
	}
	execute(m, key(m, "t")) // global tasks by default
	for _, label := range []string{"Planned", "In progress", "Stalled", "Completed"} {
		if !strings.Contains(m.Render(), label) {
			t.Fatalf("missing %s: %s", label, m.Render())
		}
	}
	m.folder = "work"
	m.filters.folder = "work"
	m.filters.recursive = false
	execute(m, m.refresh())
	if len(m.taskPage.Items) != 3 {
		t.Fatalf("scoped tasks: %+v", m.taskPage)
	}
	m.filters.states = "completed"
	m.filters.sort = "title"
	m.filters.desc = true
	load(m)
	if len(m.taskPage.Items) != 1 || m.taskPage.Items[0].State != model.Completed {
		t.Fatalf("filtered tasks: %+v", m.taskPage)
	}
}

func TestLocalDateFilterAndStableSort(t *testing.T) {
	m, _, _ := fixture(t)
	m.filters.dateField = "transition"
	m.filters.eventState = "completed"
	m.filters.from = "2026-03-08"
	m.filters.through = "2026-03-08"
	m.filters.sort = "state_changed"
	m.filters.desc = true
	q, err := m.taskQuery()
	if err != nil {
		t.Fatal(err)
	}
	if q.Filter.From.Format(time.RFC3339) != "2026-03-08T05:00:00Z" || q.Filter.Until.Format(time.RFC3339) != "2026-03-09T04:00:00Z" || q.Sort != "state_changed" || !q.Desc {
		t.Fatalf("query: %+v", q)
	}
}

func TestTaskSortDirectionAndIDTieBreakingReachTheList(t *testing.T) {
	m, _, cfg := fixture(t)
	for name, raw := range map[string]string{
		"z.task.md": "---\nid: a\ntype: task\ntitle: Same\n---\n",
		"a.task.md": "---\nid: b\ntype: task\ntitle: Same\n---\n",
		"c.task.md": "---\nid: c\ntype: task\ntitle: Zulu\n---\n",
	} {
		if err := os.WriteFile(filepath.Join(cfg.Content, name), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	execute(m, key(m, "t"))
	if len(m.taskPage.Items) != 3 || m.taskPage.Items[0].ID != "a" || m.taskPage.Items[1].ID != "b" || m.taskPage.Items[2].ID != "c" {
		t.Fatalf("ascending: %+v", m.taskPage.Items)
	}
	m.filters.desc = true
	load(m)
	if m.taskPage.Items[0].ID != "c" || m.taskPage.Items[1].ID != "a" || m.taskPage.Items[2].ID != "b" {
		t.Fatalf("descending: %+v", m.taskPage.Items)
	}
}

func TestStaleQueryCannotReplaceCurrentResultsOrDraft(t *testing.T) {
	m, a, _ := fixture(t)
	if _, err := a.Create(t.Context(), app.CreateInput{Kind: model.Task, Name: "current"}); err != nil {
		t.Fatal(err)
	}
	m.mode = "tasks"
	old := m.refresh()
	current := m.refresh()
	execute(m, current)
	key(m, "n")
	key(m, "unfinished")
	execute(m, old)
	if m.form == nil || m.form.fields[0].value != "unfinished" || len(m.taskPage.Items) != 1 || m.loading {
		t.Fatalf("stale query changed state: %s", m.Render())
	}
}

func TestKeywordSearchRendersActualPassageWithoutInference(t *testing.T) {
	m, a, _ := fixture(t)
	if _, err := a.Create(t.Context(), app.CreateInput{Kind: model.Note, Name: "guide", Body: "distinctive keyword passage"}); err != nil {
		t.Fatal(err)
	}
	m.filters.mode = "keyword"
	key(m, "/")
	key(m, "distinctive")
	execute(m, key(m, "enter"))
	if len(m.hits) != 1 || m.hits[0].Path != "guide.note.md" || !strings.Contains(m.Render(), "keyword passage") || !strings.Contains(m.Render(), "current") {
		t.Fatalf("keyword search: %s", m.Render())
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".cache", "recall")); !os.IsNotExist(err) {
		t.Fatalf("keyword touched inference cache: %v", err)
	}
}

// Background completions must never clear a partially authored form.
func TestRefreshPreservesFormAndDiagnosticsAreActionable(t *testing.T) {
	m, _, cfg := fixture(t)
	if err := os.WriteFile(filepath.Join(cfg.Content, "broken.note.md"), []byte("---\nid: [\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	key(m, "n")
	key(m, "qjknN/r?sFd")
	want := m.form.fields[0].value
	load(m)
	if m.form == nil || m.form.fields[0].value != want || want != "qjknN/r?sFd" {
		t.Fatalf("typing dispatched a shortcut: %+v", m.form)
	}
	key(m, "esc")
	if got := m.Render(); !strings.Contains(got, "broken.note.md") || !strings.Contains(got, "invalid_document") || !strings.Contains(got, "repair") {
		t.Fatal(got)
	}
}

type slowService struct {
	*app.App
	started chan struct{}
	release chan struct{}
}

func (s *slowService) Search(ctx context.Context, q model.SearchQuery) ([]model.Hit, error) {
	close(s.started)
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return nil, &model.Error{Code: "runtime_unavailable", Message: "fake embedding unavailable"}
}

func TestSlowSearchDoesNotBlockTypingAndErrorsStayVisible(t *testing.T) {
	m, a, _ := fixture(t)
	s := &slowService{a, make(chan struct{}), make(chan struct{})}
	m.service = s
	key(m, "/")
	key(m, "needle")
	cmd := key(m, "enter")
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	<-s.started
	if !strings.Contains(m.Render(), "pending") {
		t.Fatal(m.Render())
	}
	key(m, "n")
	key(m, "draft qjk")
	close(s.release)
	m.Update(<-done)
	if m.form == nil || m.form.fields[0].value != "draft qjk" || !strings.Contains(m.Render(), "runtime_unavailable") {
		t.Fatalf("completion lost input/error: %s", m.Render())
	}
}

type slowEmbedding struct{ started, release chan struct{} }

func (p *slowEmbedding) Tokenize(_ context.Context, s string) ([]int, error) {
	return make([]int, len(strings.Fields(s))+2), nil
}
func (p *slowEmbedding) Embed(ctx context.Context, _ string, _ []string) ([][]float32, error) {
	close(p.started)
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return nil, &model.Error{Code: "runtime_unavailable", Message: "fake embedding unavailable"}
}

type embeddingService struct {
	*app.App
	engine *search.Engine
}

func (s *embeddingService) IndexPending(ctx context.Context) error { return s.engine.RunPending(ctx) }

func TestRealPendingIndexWithSlowEmbeddingKeepsInputResponsive(t *testing.T) {
	m, a, cfg := fixture(t)
	if _, err := a.Create(t.Context(), app.CreateInput{Kind: model.Note, Name: "source", Body: "content to embed"}); err != nil {
		t.Fatal(err)
	}
	store, err := fsstore.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	idx, err := index.Open(cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	p := &slowEmbedding{make(chan struct{}), make(chan struct{})}
	m.service = &embeddingService{a, search.New(search.NewStoreSource(store), idx, p)}
	cmd := m.indexPending()
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case <-p.started:
	case <-time.After(3 * time.Second):
		t.Fatal("embedding did not start")
	}
	key(m, "n")
	key(m, "draft while embedding")
	if !strings.Contains(m.Render(), "indexing pending") {
		t.Fatal(m.Render())
	}
	close(p.release)
	m.Update(<-done)
	if m.form == nil || m.form.fields[0].value != "draft while embedding" || !strings.Contains(m.Render(), "runtime_unavailable") {
		t.Fatal(m.Render())
	}
}

func TestWatcherStartupFailureIsPersistentAndDoesNotDiscardDraft(t *testing.T) {
	m, _, cfg := fixture(t)
	if err := os.Rename(cfg.Content, cfg.Content+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cfg.Content+"-old", cfg.Content); err != nil {
		t.Fatal(err)
	}
	key(m, "n")
	key(m, "draft survives watcher failure")
	runWatcher(m.ctx, m.service, cfg.Content, func(msg tea.Msg) { m.Update(msg) })
	if !strings.Contains(m.Render(), "Watcher stopped") || !strings.Contains(m.Render(), "outside_root") {
		t.Fatalf("startup error disappeared: %s", m.Render())
	}
	load(m)
	m.Update(operationMsg{})
	if !strings.Contains(m.Render(), "Watcher stopped") || m.form == nil || m.form.fields[0].value != "draft survives watcher failure" {
		t.Fatalf("refresh/operation erased terminal watcher failure or draft: %s", m.Render())
	}
}

func TestWatcherExpectedCancellationDoesNotReportFailure(t *testing.T) {
	m, _, cfg := fixture(t)
	for _, deadline := range []bool{false, true} {
		var ctx context.Context
		var cancel context.CancelFunc
		if deadline {
			ctx, cancel = context.WithDeadline(m.ctx, time.Now().Add(-time.Second))
		} else {
			ctx, cancel = context.WithCancel(m.ctx)
			cancel()
		}
		var messages []tea.Msg
		runWatcher(ctx, m.service, cfg.Content, func(msg tea.Msg) { messages = append(messages, msg) })
		cancel()
		if len(messages) != 0 {
			t.Fatalf("expected shutdown became watcher failure: %+v", messages)
		}
	}
}
