package editor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"recall/internal/app"
	"recall/internal/config"
	"recall/internal/document"
	"recall/internal/index"
	"recall/internal/model"
)

// Catches shell splitting, interpolation, and mutation of the configured argv.
func TestEditorKeepsPathAsOneArgument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a b;echo nope.task.md")
	argv := make([]string, 2, 5)
	copy(argv, []string{"nvim", "--"})
	cmd, err := Command(argv, path)
	if err != nil || !reflect.DeepEqual(cmd.Args, []string{"nvim", "--", path}) {
		t.Fatalf("command = %v, %v", cmd, err)
	}
	argv = append(argv, "changed")
	if cmd.Args[2] != path {
		t.Fatalf("command aliases config: %q", cmd.Args)
	}
}

func TestEditorRejectsInvalidArguments(t *testing.T) {
	for _, tt := range []struct {
		argv []string
		path string
	}{
		{nil, "/absolute.task.md"},
		{[]string{""}, "/absolute.task.md"},
		{[]string{"nvim\x00"}, "/absolute.task.md"},
		{[]string{"nvim", "bad\x00arg"}, "/absolute.task.md"},
		{[]string{"nvim"}, "relative.task.md"},
		{[]string{"nvim"}, ""},
		{[]string{"nvim"}, "/bad\x00.task.md"},
	} {
		if _, err := Command(tt.argv, tt.path); model.ErrorCode(err) != "invalid_argument" {
			t.Fatalf("accepted %q %q: %v", tt.argv, tt.path, err)
		}
	}
}

// This is a real child process standing in for an editor, not Neovim coverage.
func TestEditorProcessHelper(t *testing.T) {
	mode := os.Getenv("RECALL_EDITOR_HELPER")
	if mode == "" {
		return
	}
	for i, arg := range os.Args {
		if arg != "--" {
			continue
		}
		args := os.Args[i+1:]
		if len(args) != 1 {
			os.Exit(90)
		}
		raw, err := os.ReadFile(args[0])
		if err != nil {
			os.Exit(91)
		}
		raw = []byte(strings.ReplaceAll(strings.Replace(string(raw), "state: planned", "state: completed", 1), "old body", "saved body 自由\r\n"))
		if err := os.WriteFile(args[0], raw, 0600); err != nil {
			os.Exit(92)
		}
		encoded, _ := json.Marshal(args)
		fmt.Print(string(encoded))
		if mode == "nonzero" {
			os.Exit(7)
		}
		os.Exit(0)
	}
	os.Exit(93)
}

// Catches editing a DB export instead of the actual file and skipping Sync on
// nonzero exit. Check the independent index BEFORE any source-backed app read.
func TestEditorCompletionReconcilesActualSourceOnEitherExitStatus(t *testing.T) {
	for _, mode := range []string{"success", "nonzero"} {
		t.Run(mode, func(t *testing.T) {
			cfg, err := config.Resolve(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := config.Prepare(cfg); err != nil {
				t.Fatal(err)
			}
			a, err := app.Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			d, err := a.Create(t.Context(), app.CreateInput{Kind: model.Task, Name: "a b;echo $(nope)", Body: "old body"})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(cfg.Content, d.Path)
			cmd, err := Command([]string{os.Args[0], "-test.run=^TestEditorProcessHelper$", "--"}, path)
			if err != nil {
				t.Fatal(err)
			}
			cmd.Env = append(os.Environ(), "RECALL_EDITOR_HELPER="+mode)
			output, exitErr := cmd.CombinedOutput()
			var received []string
			if err := json.Unmarshal(output, &received); err != nil || !reflect.DeepEqual(received, []string{path}) {
				t.Fatalf("received args: %q %v", output, err)
			}
			if (exitErr != nil) != (mode == "nonzero") {
				t.Fatalf("exit status: %v", exitErr)
			}
			sync := func(ctx context.Context) error { _, err := a.Sync(ctx); return err }
			err = Complete(t.Context(), exitErr, sync)
			if (exitErr == nil && err != nil) || (exitErr != nil && !errors.Is(err, exitErr)) {
				t.Fatalf("completion lost exit status: %v", err)
			}
			idx, err := index.Open(cfg.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer idx.Close()
			page, err := idx.Tasks(t.Context(), model.TaskQuery{})
			if err != nil || len(page.Items) != 1 || page.Items[0].Body != "saved body 自由\r\n" || page.Items[0].State != model.Completed || len(page.Items[0].History) != 2 {
				t.Fatalf("indexed completion: %+v %v", page, err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			p, err := document.Parse(d.Path, raw)
			if err != nil || p.Document.Body != "saved body 自由\r\n" || len(p.Document.History) != 2 || p.Document.History[1].State != model.Completed {
				t.Fatalf("actual source: %+v %v", p.Document, err)
			}
			if err := Complete(t.Context(), nil, sync); err != nil {
				t.Fatal(err)
			}
			page, err = idx.Tasks(t.Context(), model.TaskQuery{})
			if err != nil || len(page.Items) != 1 || len(page.Items[0].History) != 2 {
				t.Fatalf("repeat completion duplicated history: %+v %v", page, err)
			}
		})
	}
}

func TestEditorCompletionPreservesReconcileErrorAndCancellation(t *testing.T) {
	exitErr, syncErr := errors.New("editor exited"), errors.New("sync failed")
	err := Complete(t.Context(), exitErr, func(context.Context) error { return syncErr })
	if !errors.Is(err, exitErr) || !errors.Is(err, syncErr) {
		t.Fatalf("lost error: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = Complete(ctx, nil, func(ctx context.Context) error { return ctx.Err() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}
