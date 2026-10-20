package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"recall/internal/app"
	"recall/internal/config"
)

func TestDefaultTUIRejectsNonterminalBeforeOpeningApp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	r := Runner{open: func(context.Context, config.Config) (*app.App, error) {
		t.Fatal("nonterminal TUI opened app")
		return nil, nil
	}}
	var out, logs bytes.Buffer
	if code := r.Run(t.Context(), nil, strings.NewReader("q"), &out, &logs); code != 1 || out.Len() != 0 || !strings.Contains(logs.String(), "terminal") {
		t.Fatalf("%d stdout=%s stderr=%s", code, &out, &logs)
	}
}

func TestCLIEditCanRepairMalformedTypedFile(t *testing.T) {
	cfg := prepared(t)
	path := filepath.Join(cfg.Content, "repair space.note.md")
	if err := os.WriteFile(path, []byte("---\nid: [bad\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "editor")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'repaired body' > \"$1\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Editor = []string{script}
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	var out, logs bytes.Buffer
	if code := Run(t.Context(), []string{"--root", cfg.Root, "document", "edit", "--path", "repair space.note.md"}, strings.NewReader(""), &out, &logs); code != 0 || !strings.Contains(out.String(), "repaired body") {
		t.Fatalf("%d %s %s", code, &out, &logs)
	}
}
