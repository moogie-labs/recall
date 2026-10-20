package tui

import (
	"context"
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
