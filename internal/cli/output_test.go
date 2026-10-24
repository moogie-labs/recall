package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONPreviewWireBudgets(t *testing.T) {
	for _, tc := range []struct {
		name           string
		count, idBytes int
		title          string
		wantError      bool
	}{
		{"title", 1, 10, strings.Repeat("界", 100000), false},
		{"identity", 1, 8192, "Same", true},
		{"page", 20, 1800, "Same", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := prepared(t)
			for n := 0; n < tc.count; n++ {
				raw := fmt.Sprintf("---\nid: %03d%s\ntype: task\ntitle: %s\n---\nneedle\n", n, strings.Repeat("x", tc.idBytes), tc.title)
				if err := os.WriteFile(filepath.Join(cfg.Content, fmt.Sprintf("%03d.task.md", n)), []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, args := range [][]string{{"document", "list"}, {"task", "list"}, {"search", "needle", "--mode", "keyword"}} {
				var out, logs bytes.Buffer
				code := Run(t.Context(), append([]string{"--root", cfg.Root, "--json"}, args...), nil, &out, &logs)
				got := decodeResult(t, &out)
				if tc.wantError {
					if code != 1 || got.Error == nil || got.Error.Code != "output_too_large" {
						t.Errorf("%v accepted oversized output: exit=%d bytes=%d", args, code, len(got.Data))
					}
					continue
				}
				if code != 0 || got.Error != nil {
					t.Fatalf("%v failed: %d %+v", args, code, got.Error)
				}
				if len(got.Data) > 32768 || !bytes.Contains(got.Data, []byte(`"title_truncated":true`)) || bytes.Contains(got.Data, []byte(strings.Repeat("界", 86))) {
					t.Errorf("%v unbounded/unmarked title: %d bytes", args, len(got.Data))
				}
			}
		})
	}
}

func TestJSONPreviewWireBudgetsAuxiliaryArrays(t *testing.T) {
	cfg := prepared(t)
	for n := 0; n < 180; n++ {
		folder := fmt.Sprintf("%03d-", n) + strings.Repeat("f", 190)
		if err := os.Mkdir(filepath.Join(cfg.Content, folder), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cfg.Content, folder, "bad.note.md"), []byte("---\ntitle: [\n---\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"document", "list", "--limit", "1"}, {"folder", "list"}} {
		var out, logs bytes.Buffer
		code := Run(t.Context(), append([]string{"--root", cfg.Root, "--json"}, args...), nil, &out, &logs)
		if !json.Valid(out.Bytes()) || out.Len() > 2048 {
			t.Fatal("unbounded/invalid error")
		}
		got := decodeResult(t, &out)
		if code != 1 || got.Error == nil || got.Error.Code != "output_too_large" {
			t.Errorf("%v omitted auxiliary budget: exit=%d data=%d", args, code, len(got.Data))
		}
	}
}

func TestJSONPreviewWireBudgetsErrors(t *testing.T) {
	cfg := prepared(t)
	var out, logs bytes.Buffer
	code := Run(t.Context(), []string{"--root", cfg.Root, "--json", "document", "get", "--path", "../" + strings.Repeat("x", 10000)}, nil, &out, &logs)
	size := out.Len()
	got := decodeResult(t, &out)
	if code != 1 || got.Error == nil || got.Error.Code != "output_too_large" || size > 2048 {
		t.Fatalf("unbounded error: exit=%d bytes=%d error=%+v", code, size, got.Error)
	}
}
