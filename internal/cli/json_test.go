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
	code := Run(t.Context(), []string{"--root", t.TempDir(), "document", "list", "--json"}, strings.NewReader(""), &out, &logs)
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
		{[]string{"document", "list", "--unknown"}, 2, "invalid_argument"},
		{[]string{"document", "get"}, 2, "invalid_argument"},
		{[]string{"document", "get", "--id", "x", "--path", "x.md"}, 2, "invalid_argument"},
		{[]string{"document", "get", "--path", "absent.md"}, 3, "not_found"},
		{[]string{"document", "get", "--path", "../outside.md"}, 2, "outside_root"},
		{[]string{"document", "update", "--path", "absent.md"}, 2, "invalid_argument"},
		{[]string{"document", "list", "--limit", "201"}, 2, "invalid_argument"},
		{[]string{"document", "list", "--offset", "-1"}, 2, "invalid_argument"},
		{[]string{"search", "x", "--mode", "other"}, 2, "invalid_argument"},
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
	code := Run(t.Context(), []string{"--root", cfg.Root, "document", "create", "--name", "missing", "--body-file", path, "--json"}, nil, &out, &logs)
	got := decodeResult(t, &out)
	if code != 3 || got.Error == nil || got.Error.Code != "not_found" || got.Error.Path != path {
		t.Fatalf("%d %+v", code, got.Error)
	}
}

func TestTrailingJSONFlagAndJSONHelpStayFramed(t *testing.T) {
	for _, args := range [][]string{{"unknown", "--json"}, {"document", "list", "--unknown", "--json"}, {"document", "create", "--help", "--json"}} {
		var out, logs bytes.Buffer
		code := Run(t.Context(), args, nil, &out, &logs)
		got := decodeResult(t, &out)
		if args[1] == "create" {
			if code != 0 || got.Error != nil || !bytes.Contains(got.Data, []byte("Usage:")) {
				t.Fatalf("%d %+v", code, got)
			}
		} else if code != 2 || got.Error == nil || got.Error.Code != "invalid_argument" {
			t.Fatalf("%d %+v", code, got)
		}
	}
}

func TestJSONParseFailureUsesLastRequestedMode(t *testing.T) {
	cfg := prepared(t)
	for _, tt := range []struct {
		name string
		args []string
		json bool
	}{
		{"enable_after_unknown", []string{"--json=false", "document", "list", "--unknown", "--json"}, true},
		{"disable_after_unknown", []string{"--json", "document", "list", "--unknown", "--json=false"}, false},
		{"repeated_enable", []string{"--json=false", "document", "list", "--unknown", "--json", "--json=false", "--json=true"}, true},
		{"repeated_disable", []string{"--json", "document", "list", "--unknown", "--json=false", "--json", "--json=false"}, false},
		{"boolean_equals_enable", []string{"--json=0", "document", "list", "--unknown", "--json=1"}, true},
		{"boolean_equals_disable", []string{"--json=1", "document", "list", "--unknown", "--json=0"}, false},
		{"enable_after_bad_integer", []string{"--json=false", "document", "list", "--limit", "bad", "--json"}, true},
		{"disable_after_bad_integer", []string{"--json", "document", "list", "--limit", "bad", "--json=false"}, false},
		{"terminator_keeps_false", []string{"--json=false", "document", "list", "--unknown", "--", "--json"}, false},
		{"terminator_keeps_true", []string{"--json", "document", "list", "--unknown", "--", "--json=false"}, true},
		{"literal_title_after_unknown", []string{"document", "create", "--unknown", "--name", "x", "--title", "--json"}, false},
		{"literal_title_cannot_disable", []string{"--json", "document", "create", "--unknown", "--title", "--json=false"}, true},
		{"equals_title_then_enable", []string{"--json=false", "document", "create", "--unknown", "--title=--json=false", "--json"}, true},
		{"string_consumes_terminator", []string{"--json=false", "document", "create", "--unknown", "--title", "--", "--json"}, true},
		{"literal_folder_cannot_enable", []string{"document", "list", "--unknown", "--folder", "--json"}, false},
		{"literal_mode_cannot_enable", []string{"search", "needle", "--unknown", "--mode", "--json"}, false},
		{"boolean_takes_no_value", []string{"--json=false", "folder", "list", "--unknown", "--recursive", "--json"}, true},
		{"sibling_flag_is_unknown", []string{"--json=false", "document", "list", "--unknown", "--title", "--json"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, logs bytes.Buffer
			code := Run(t.Context(), append([]string{"--root", cfg.Root}, tt.args...), strings.NewReader(""), &out, &logs)
			if code != 2 {
				t.Fatalf("exit=%d stdout=%s stderr=%s", code, &out, &logs)
			}
			if tt.json {
				got := decodeResult(t, &out)
				if got.Error == nil || got.Error.Code != "invalid_argument" || string(got.Data) != "null" || logs.Len() != 0 {
					t.Fatalf("result=%+v stderr=%s", got, &logs)
				}
			} else if out.Len() != 0 || !strings.Contains(logs.String(), "recall:") {
				t.Fatalf("human failure: stdout=%s stderr=%s", &out, &logs)
			}
		})
	}
}

func TestJSONLiteralValuesAndSuccessfulToggles(t *testing.T) {
	cfg := prepared(t)
	for _, tt := range []struct {
		name  string
		args  []string
		title string
		json  bool
	}{
		{"literal_bare", []string{"--title", "--json"}, "--json", false},
		{"literal_equals", []string{"--title=--json"}, "--json", false},
		{"literal_false_with_json", []string{"--json", "--title", "--json=false"}, "--json=false", true},
		{"literal_then_real_mode", []string{"--title", "--json", "--json"}, "--json", true},
		{"string_terminator_then_mode", []string{"--title", "--", "--json"}, "--", true},
		{"repeated_enable", []string{"--json=false", "--json", "--title", "ordinary"}, "ordinary", true},
		{"repeated_disable", []string{"--json", "--json=false", "--title", "ordinary"}, "ordinary", false},
		{"equals_alias_enable", []string{"--json=0", "--json=TRUE", "--title=--json=false"}, "--json=false", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := []string{"--root", cfg.Root, "document", "create", "--name", tt.name}
			var out, logs bytes.Buffer
			code := Run(t.Context(), append(args, tt.args...), strings.NewReader("literal value body\n"), &out, &logs)
			if code != 0 || logs.Len() != 0 {
				t.Fatalf("exit=%d stdout=%s stderr=%s", code, &out, &logs)
			}
			if tt.json {
				got := as[model.Document](t, decodeResult(t, &out))
				if got.Title != tt.title || got.Body != "literal value body\n" {
					t.Fatalf("%+v", got)
				}
			} else if !strings.HasPrefix(out.String(), "---\n") {
				t.Fatalf("expected human Markdown, got %s", &out)
			}
			stored := as[model.Document](t, invoke(t, cfg.Root, "", "document", "get", "--path", tt.name+".md"))
			if stored.Title != tt.title {
				t.Fatalf("stored title %q, want %q", stored.Title, tt.title)
			}
		})
	}
}
