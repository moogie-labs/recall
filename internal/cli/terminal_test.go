package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"recall/internal/model"
)

// No PTY: malicious controls are only captured as bytes, never sent to a terminal.
func TestExecutableHumanPreviewsAreInertAndRawJSONLossless(t *testing.T) {
	bin := buildExecutable(t) // Build before isolating runtime HOME.
	cfg := prepared(t)
	controls := "\x1b[2J\x1b]52;c;YWJj\a\u009b31m\u009d0;hidden\u009c\b\r\x7f"
	folder := "folder" + controls
	path := folder + "/guide" + controls + ".note.md"
	title, id := "Title"+controls, "ID"+controls
	body := "needle first line\nuseful second line " + controls + "\n"
	raw := "---\nid: " + strconv.Quote(id) + "\ntype: note\ntitle: " + strconv.Quote(title) + "\n---\n" + body
	if err := os.Mkdir(filepath.Join(cfg.Content, folder), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Content, path), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Content, "bad"+controls+".note.md"), []byte("---\ntitle: [\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (int, []byte, []byte) {
		t.Helper()
		command := exec.CommandContext(t.Context(), bin, append([]string{"--root", cfg.Root}, args...)...)
		var out, logs bytes.Buffer
		command.Stdout, command.Stderr = &out, &logs
		err := command.Run()
		code := 0
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		return code, out.Bytes(), logs.Bytes()
	}
	safe := func(t *testing.T, text []byte) {
		t.Helper()
		for _, r := range string(text) {
			if unicode.IsControl(r) && r != '\n' && r != '\t' {
				t.Fatalf("executable terminal control U+%04X in captured output", r)
			}
		}
	}
	for _, args := range [][]string{{"document", "list"}, {"folder", "list"}, {"search", "needle", "--mode", "keyword"}, {"status"}, {"sync"}, {"document", "get", "--path", "../" + controls}} {
		t.Run(strings.Join(args[:min(2, len(args))], "-"), func(t *testing.T) {
			code, out, logs := run(args...)
			if args[0] == "document" && args[1] == "get" {
				if code != 2 {
					t.Fatalf("error exit=%d", code)
				}
			} else if code != 0 {
				t.Fatalf("preview exit=%d logs=%q", code, logs)
			}
			safe(t, out)
			safe(t, logs)
			if args[0] == "search" && !bytes.Contains(out, []byte("needle first line\nuseful second line")) {
				t.Fatal("useful passage newlines lost")
			}
			combined := append(out, logs...)
			if !bytes.Contains(combined, []byte(`\x1b`)) && !bytes.Contains(combined, []byte(`\u001b`)) {
				t.Fatal("controls should remain visibly escaped")
			}
		})
	}
	code, out, logs := run("document", "get", "--path", path)
	if code != 0 || string(out) != raw || len(logs) != 0 {
		t.Fatalf("explicit raw changed: exit=%d rawEqual=%v logs=%q", code, string(out) == raw, logs)
	}
	code, out, _ = run("document", "get", "--path", path, "--json")
	var full struct {
		Data model.Document `json:"data"`
	}
	if err := json.Unmarshal(out, &full); err != nil {
		t.Fatal(err)
	}
	if code != 0 || full.Data.Raw != raw || full.Data.Title != title || full.Data.ID != id || full.Data.Path != path {
		t.Fatal("JSON full get lost authored data")
	}
	code, out, _ = run("search", "needle", "--mode", "keyword", "--json")
	var hits struct {
		Data []model.Hit `json:"data"`
	}
	if err := json.Unmarshal(out, &hits); err != nil {
		t.Fatal(err)
	}
	if code != 0 || len(hits.Data) != 1 || hits.Data[0].Path != path || hits.Data[0].Title != title || !strings.Contains(hits.Data[0].Snippet, controls) {
		t.Fatal("JSON preview sanitized authored controls")
	}
	code, out, _ = run("document", "get", "--path", "../"+controls, "--json")
	var failure struct {
		Error *model.Error `json:"error"`
	}
	if err := json.Unmarshal(out, &failure); err != nil {
		t.Fatal(err)
	}
	if code != 2 || failure.Error == nil || failure.Error.Path != "../"+controls {
		t.Fatal("JSON error lost exact path")
	}
}
