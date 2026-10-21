package tui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"recall/internal/app"
	"recall/internal/index"
	"recall/internal/model"
	"strings"
	"testing"
)

func TestKeyboardCreateMoveStateFolderAndFilterActions(t *testing.T) {
	m, a, _ := fixture(t)
	key(m, "f")
	key(m, "work")
	execute(m, key(m, "enter"))
	load(m)
	key(m, "N")
	key(m, "alpha")
	key(m, "tab")
	key(m, "Zulu")
	key(m, "tab")
	key(m, "ctrl+u")
	key(m, "work")
	execute(m, key(m, "enter"))
	load(m)
	m.folder = "work"
	m.pane = 1
	key(m, "s")
	key(m, "j")
	execute(m, key(m, "enter"))
	load(m)
	d, err := a.Read(t.Context(), model.Ref{Path: "work/alpha.task.md"})
	if err != nil || d.State != model.InProgress {
		t.Fatalf("state action: %+v %v", d, err)
	}
	key(m, "r")
	key(m, "ctrl+u")
	key(m, "moved.task.md")
	execute(m, key(m, "enter"))
	load(m)
	if _, err = a.Read(t.Context(), model.Ref{Path: "moved.task.md"}); err != nil {
		t.Fatal(err)
	}
	key(m, "F")
	values := []string{".", "false", "in_progress", "", "", "", "", "keyword", "title", "true"}
	for i, value := range values {
		key(m, "ctrl+u")
		if value != "" {
			key(m, value)
		}
		if i < len(values)-1 {
			key(m, "tab")
		}
	}
	execute(m, key(m, "enter"))
	if m.form != nil || m.mode != "tasks" || len(m.taskPage.Items) != 1 || m.taskPage.Items[0].Path != "moved.task.md" || !strings.Contains(m.Render(), "desc") {
		t.Fatalf("filter action: %s", m.Render())
	}
}

type blockedCreate struct {
	*app.App
	started, release chan struct{}
}

func (s *blockedCreate) Create(ctx context.Context, in app.CreateInput) (model.Document, error) {
	close(s.started)
	select {
	case <-s.release:
	case <-ctx.Done():
		return model.Document{}, ctx.Err()
	}
	return s.App.Create(ctx, in)
}

func TestPendingMutationDoesNotDiscardNewDraft(t *testing.T) {
	m, a, _ := fixture(t)
	s := &blockedCreate{a, make(chan struct{}), make(chan struct{})}
	m.service = s
	key(m, "n")
	key(m, "submitted")
	cmd := key(m, "enter")
	done := make(chan any, 1)
	go func() { done <- cmd() }()
	<-s.started
	key(m, "esc")
	key(m, "N")
	key(m, "new draft")
	close(s.release)
	m.Update(<-done)
	if m.form == nil || m.form.kind != "task" || m.form.fields[0].value != "new draft" {
		t.Fatalf("in-flight operation ate a draft: %+v", m.form)
	}
}

func TestDeletionCancellationAndSingleConfirmation(t *testing.T) {
	m, a, _ := fixture(t)
	d, err := a.Create(t.Context(), app.CreateInput{Kind: model.Note, Name: "delete me"})
	if err != nil {
		t.Fatal(err)
	}
	load(m)
	m.pane = 1
	key(m, "d")
	key(m, "q")
	if _, err = a.Read(t.Context(), model.Ref{ID: d.ID}); err != nil {
		t.Fatal("cancel deleted source", err)
	}
	key(m, "d")
	cmd := key(m, "y")
	if again := key(m, "y"); again != nil {
		t.Fatal("confirmation submitted twice")
	}
	execute(m, cmd)
	if _, err = a.Read(t.Context(), model.Ref{ID: d.ID}); model.ErrorCode(err) != "not_found" {
		t.Fatalf("confirm did not delete: %v", err)
	}
}

func TestSearchHitMutationsRejectChangedOrReplacedSources(t *testing.T) {
	for _, action := range []string{"move", "state", "delete"} {
		for _, replacement := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/replaced=%v", action, replacement), func(t *testing.T) {
				m, a, cfg := fixture(t)
				d, err := a.Create(t.Context(), app.CreateInput{Kind: model.Task, Name: "source", Body: "needle original"})
				if err != nil {
					t.Fatal(err)
				}
				m.filters.mode = "keyword"
				key(m, "/")
				key(m, "needle")
				execute(m, key(m, "enter"))
				if len(m.hits) != 1 {
					t.Fatal(m.Render())
				}
				switch action {
				case "move":
					key(m, "r")
					key(m, "ctrl+u")
					key(m, "moved.task.md")
				case "state":
					key(m, "s")
					key(m, "j")
				case "delete":
					key(m, "d")
				}
				raw := strings.Replace(d.Raw, "needle original", "needle changed", 1)
				if replacement {
					raw = strings.Replace(raw, d.ID, "different-id", 1)
				}
				path := filepath.Join(cfg.Content, d.Path)
				if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
				confirm := "enter"
				if action == "delete" {
					confirm = "y"
				}
				cmd := key(m, confirm)
				if cmd == nil {
					t.Fatal("no mutation attempted")
				}
				result := cmd()
				m.Update(result)
				if r, ok := result.(operationMsg); !ok || model.ErrorCode(r.err) != "conflict" {
					t.Fatalf("stale %s mutated newer source: %+v", action, result)
				}
				got, err := os.ReadFile(path)
				if err != nil || string(got) != raw {
					t.Fatalf("newer source lost: %q %v", got, err)
				}
			})
		}
	}
}

func TestOldSearchHitDoesNotBorrowNewerSnapshotRevision(t *testing.T) {
	m, a, _ := fixture(t)
	d, err := a.Create(t.Context(), app.CreateInput{Kind: model.Note, Name: "source", Body: "needle original"})
	if err != nil {
		t.Fatal(err)
	}
	m.filters.mode = "keyword"
	key(m, "/")
	key(m, "needle")
	execute(m, key(m, "enter"))
	if _, err := a.Update(t.Context(), model.Ref{ID: d.ID}, d.Revision, strings.Replace(d.Raw, "needle original", "needle updated", 1)); err != nil {
		t.Fatal(err)
	}
	m.snapshot, err = a.List(t.Context(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	key(m, "d")
	cmd := key(m, "y")
	if cmd == nil {
		t.Fatal("delete not attempted")
	}
	result := cmd().(operationMsg)
	if model.ErrorCode(result.err) != "conflict" {
		t.Fatalf("old result borrowed a newer snapshot revision: %v", result.err)
	}
}

func TestRecursiveDeleteRequiresInspectableScopeAfterResize(t *testing.T) {
	m, a, cfg := fixture(t)
	if err := a.CreateFolder(t.Context(), "folder"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Create(t.Context(), app.CreateInput{Kind: model.Note, Folder: "folder", Name: "child"}); err != nil {
		t.Fatal(err)
	}
	load(m)
	m.pane = 1
	key(m, "d")
	m.Update(tea.WindowSizeMsg{Width: 24, Height: 4})
	if cmd := key(m, "y"); cmd != nil {
		t.Fatal("accepted recursive delete with inaccessible scope")
	}
	if _, err := os.Stat(filepath.Join(cfg.Content, "folder", "child.note.md")); err != nil {
		t.Fatal("resize confirmation lost source", err)
	}
	m.Update(tea.WindowSizeMsg{Width: 24, Height: 8})
	if cmd := key(m, "y"); cmd != nil {
		t.Fatal("resizing bypassed complete scope review")
	}
	var frames string
	for range 30 {
		frames += m.Render() + "\n"
		key(m, "j")
	}
	if !strings.Contains(frames, "ALL descendants") {
		t.Fatal("recursive scope never visible", frames)
	}
	m.Update(tea.WindowSizeMsg{Width: 8, Height: 4})
	if cmd := key(m, "y"); cmd != nil {
		t.Fatal("accepted after shrinking a reviewed confirmation")
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	cmd := key(m, "y")
	if cmd == nil {
		t.Fatalf("cannot confirm fully visible scope: %s", m.Render())
	}
	execute(m, cmd)
	if _, err := os.Stat(filepath.Join(cfg.Content, "folder")); !os.IsNotExist(err) {
		t.Fatalf("confirmed folder was not deleted: %v", err)
	}
}

type blockedState struct {
	*app.App
	started, release chan struct{}
}

func (s *blockedState) SetState(ctx context.Context, ref model.Ref, state model.State, revision string) (model.Document, error) {
	close(s.started)
	select {
	case <-s.release:
	case <-ctx.Done():
		return model.Document{}, ctx.Err()
	}
	return s.App.SetState(ctx, ref, state, revision)
}

func TestStateCompletionDoesNotDismissNewModalGeneration(t *testing.T) {
	m, a, _ := fixture(t)
	for _, name := range []string{"first", "second"} {
		if _, err := a.Create(t.Context(), app.CreateInput{Kind: model.Task, Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	load(m)
	m.pane = 1
	s := &blockedState{a, make(chan struct{}), make(chan struct{})}
	m.service = s
	key(m, "s")
	key(m, "j")
	cmd := key(m, "enter")
	done := make(chan any, 1)
	go func() { done <- cmd() }()
	<-s.started
	key(m, "esc")
	key(m, "j")
	key(m, "s")
	close(s.release)
	m.Update(<-done)
	if m.modal != "state" || m.target.path != "second.task.md" {
		t.Fatalf("older completion dismissed new modal: %s %+v", m.modal, m.target)
	}
}

func TestEditorUsesRealPathAndReconcilesNonzeroExit(t *testing.T) {
	for _, exit := range []string{"0", "7"} {
		t.Run(exit, func(t *testing.T) {
			m, a, cfg := fixture(t)
			path := filepath.Join(cfg.Content, "repair space.note.md")
			if err := os.WriteFile(path, []byte("---\nid: [bad\n---\n"), 0600); err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(t.TempDir(), "editor")
			if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'repaired body' > \"$1\"\nexit "+exit+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			m.cfg.Editor = []string{script}
			load(m)
			m.pane = 1
			cmd := key(m, "enter")
			msg := cmd()
			ready, ok := msg.(editorReadyMsg)
			if !ok || ready.err != nil {
				t.Fatalf("editor prepare: %+v", msg)
			}
			if ready.command.Args[len(ready.command.Args)-1] != path {
				t.Fatalf("wrong actual path: %v", ready.command.Args)
			}
			proc := m.editorProcess(ready.command)
			err := proc.Run()
			if (err != nil) != (exit != "0") {
				t.Fatalf("editor exit: %v", err)
			}
			// App.Read would sync incidentally and mask a missing Complete call.
			// Query the derived index directly before any further App operation.
			idx, err := index.Open(cfg.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer idx.Close()
			hits, err := idx.Keyword(t.Context(), model.SearchQuery{Text: "repaired", Mode: "keyword"})
			if err != nil || len(hits) != 1 || hits[0].Path != "repair space.note.md" {
				t.Fatalf("editor completion did not publish saved source: %+v %v", hits, err)
			}
			d, err := a.Read(t.Context(), model.Ref{Path: "repair space.note.md"})
			if err != nil || d.Body != "repaired body" {
				t.Fatalf("saved source not reconciled: %+v %v", d, err)
			}
		})
	}
}

func TestEditorFailureRemainsVisibleAfterSuccessfulRefresh(t *testing.T) {
	m, _, _ := fixture(t)
	_, cmd := m.Update(operationMsg{err: &model.Error{Code: "internal", Message: "editor failed; saved changes reconciled"}})
	_ = cmd // Batch is deliberately not executed here; refresh is independently delivered.
	load(m)
	if !strings.Contains(m.Render(), "editor failed") {
		t.Fatalf("refresh hid editor failure: %s", m.Render())
	}
}

func TestFailedSearchStillPublishesSourceDiagnostics(t *testing.T) {
	m, a, cfg := fixture(t)
	if err := os.WriteFile(filepath.Join(cfg.Content, "bad.task.md"), []byte("---\nstate: bad\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &slowService{a, make(chan struct{}), make(chan struct{})}
	close(s.release)
	m.service = s
	key(m, "/")
	key(m, "needle")
	execute(m, key(m, "enter"))
	if !strings.Contains(m.Render(), "bad.task.md") {
		t.Fatalf("search failure hid source diagnostics: %s", m.Render())
	}
}
