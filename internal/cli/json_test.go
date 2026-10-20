package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"recall/internal/model"
)

type result struct {
	Data  json.RawMessage `json:"data"`
	Error *model.Error    `json:"error"`
}

func decodeResult(t *testing.T, out *bytes.Buffer) result {
	t.Helper()
	var got result
	d := json.NewDecoder(out)
	if err := d.Decode(&got); err != nil {
		t.Fatalf("JSON: %v: %s", err, out.String())
	}
	if err := d.Decode(new(any)); err != io.EOF {
		t.Fatalf("stdout contains extra value: %v", err)
	}
	return got
}

func TestUninitializedJSONIsOneError(t *testing.T) {
	var out, logs bytes.Buffer
	code := Run(t.Context(), []string{"--root", t.TempDir(), "task", "list", "--json"}, strings.NewReader(""), &out, &logs)
	got := decodeResult(t, &out)
	if code != 3 || got.Error == nil || got.Error.Code != "not_initialized" || string(got.Data) != "null" {
		t.Fatalf("%d %+v", code, got)
	}
}

func TestJSONInputErrorsAndExitMapping(t *testing.T) {
	cfg := prepared(t)
	for _, tt := range []struct {
		args []string
		exit int
		code string
	}{
		{[]string{"unknown-command"}, 2, "invalid_argument"},
		{[]string{"task", "list", "--unknown"}, 2, "invalid_argument"},
		{[]string{"document", "get"}, 2, "invalid_argument"},
		{[]string{"document", "get", "--id", "x", "--path", "x.note.md"}, 2, "invalid_argument"},
		{[]string{"document", "get", "--path", "absent.note.md"}, 3, "not_found"},
		{[]string{"document", "get", "--path", "../outside.note.md"}, 2, "outside_root"},
		{[]string{"document", "update", "--path", "absent.note.md"}, 2, "invalid_argument"},
		{[]string{"task", "state", "--id", "x", "--state", "deleted"}, 2, "invalid_argument"},
		{[]string{"task", "list", "--limit", "201"}, 2, "invalid_argument"},
		{[]string{"task", "list", "--offset", "-1"}, 2, "invalid_argument"},
		{[]string{"task", "list", "--date-field", "transition"}, 2, "invalid_argument"},
		{[]string{"search", "x", "--mode", "other"}, 2, "invalid_argument"},
		{[]string{"search", "x", "--mode", "keyword", "--from", "2026-01-01"}, 2, "invalid_argument"},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			var out, logs bytes.Buffer
			args := append([]string{"--root", cfg.Root, "--json"}, tt.args...)
			code := Run(t.Context(), args, strings.NewReader("secret body must not appear"), &out, &logs)
			got := decodeResult(t, &out)
			if code != tt.exit || got.Error == nil || got.Error.Code != tt.code || string(got.Data) != "null" {
				t.Fatalf("%d %+v %s", code, got, &logs)
			}
		})
	}
}

func TestMissingInputFileIsOneMissingError(t *testing.T) {
	cfg := prepared(t)
	var out, logs bytes.Buffer
	path := filepath.Join(t.TempDir(), "missing.md")
	code := Run(t.Context(), []string{"--root", cfg.Root, "document", "create", "--type", "note", "--name", "missing", "--body-file", path, "--json"}, nil, &out, &logs)
	got := decodeResult(t, &out)
	if code != 3 || got.Error == nil || got.Error.Code != "not_found" || got.Error.Path != path {
		t.Fatalf("%d %+v", code, got.Error)
	}
}

func TestTrailingJSONFlagAndJSONHelpStayFramed(t *testing.T) {
	for _, args := range [][]string{{"unknown", "--json"}, {"task", "list", "--unknown", "--json"}, {"document", "create", "--help", "--json"}} {
		var out, logs bytes.Buffer
		code := Run(t.Context(), args, nil, &out, &logs)
		got := decodeResult(t, &out)
		if args[0] == "document" {
			if code != 0 || got.Error != nil || !bytes.Contains(got.Data, []byte("Usage:")) {
				t.Fatalf("%d %+v", code, got)
			}
		} else if code != 2 || got.Error == nil || got.Error.Code != "invalid_argument" {
			t.Fatalf("%d %+v", code, got)
		}
	}
}
