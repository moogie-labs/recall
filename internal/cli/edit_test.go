package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"recall/internal/config"
	"recall/internal/model"
)

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
	setUserEditor(t, script)
	var out, logs bytes.Buffer
	if code := Run(t.Context(), []string{"--root", cfg.Root, "document", "edit", "--path", "repair space.note.md"}, strings.NewReader(""), &out, &logs); code != 0 || !strings.Contains(out.String(), "repaired body") {
		t.Fatalf("%d %s %s", code, &out, &logs)
	}
}

// setUserEditor writes the per-user editor setting beneath the test's HOME.
func setUserEditor(t *testing.T, argv ...string) {
	t.Helper()
	path, err := config.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string][]string{"editor": argv})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCLIEditNeverRunsCollectionSuppliedEditor(t *testing.T) {
	cfg := prepared(t)
	marker := filepath.Join(t.TempDir(), "collection-editor-ran")
	planted := filepath.Join(t.TempDir(), "planted editor")
	if err := os.WriteFile(planted, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	// A shared or cloned collection supplies its own editor choice.
	settings, err := json.Marshal(map[string]any{"timezone": "UTC", "editor": []string{planted}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Root, "config.json"), settings, 0600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "user editor")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'user edit' > \"$1\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	setUserEditor(t, script)
	note := as[model.Document](t, invoke(t, cfg.Root, "", "document", "create", "--type", "note", "--name", "shared"))
	var out, logs bytes.Buffer
	if code := Run(t.Context(), []string{"--root", cfg.Root, "document", "edit", "--id", note.ID}, strings.NewReader(""), &out, &logs); code != 0 {
		t.Fatalf("%d %s %s", code, &out, &logs)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("collection config.json selected the executed editor")
	}
	if !strings.Contains(out.String(), "user edit") || !strings.Contains(logs.String(), "ignoring editor in the collection's config.json") {
		t.Fatalf("user editor not used or legacy setting not reported: %s %s", &out, &logs)
	}
}

func TestCLIEditDoesNotWarnAboutAutoWrittenDefaultEditor(t *testing.T) {
	cfg := prepared(t)
	// Earlier releases persisted the default editor into every collection.
	if err := os.WriteFile(filepath.Join(cfg.Root, "config.json"), []byte(`{"timezone":"UTC","editor":["nvim"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "user editor")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'edited' > \"$1\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	setUserEditor(t, script)
	note := as[model.Document](t, invoke(t, cfg.Root, "", "document", "create", "--type", "note", "--name", "legacy"))
	var out, logs bytes.Buffer
	if code := Run(t.Context(), []string{"--root", cfg.Root, "document", "edit", "--id", note.ID}, strings.NewReader(""), &out, &logs); code != 0 {
		t.Fatalf("%d %s %s", code, &out, &logs)
	}
	if strings.Contains(logs.String(), "ignoring editor") {
		t.Fatalf("default legacy editor produced a warning: %s", &logs)
	}
}
