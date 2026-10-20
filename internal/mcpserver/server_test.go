package mcpserver_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"recall/internal/app"
	"recall/internal/cli"
	"recall/internal/config"
	"recall/internal/mcpserver"
)

func TestMain(m *testing.M) {
	// A real lazy client may execute this binary. Never recursively run tests.
	if len(os.Args) > 1 && os.Args[1] == "_embedding-worker" {
		os.Exit(cli.Run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

func newTestApp(t testing.TB) *app.App {
	t.Helper()
	a, _ := newTestAppWithSources(t, nil)
	return a
}

func newTestAppWithSources(t testing.TB, sources map[string]string) (*app.App, config.Config) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cfg, err := config.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	for path, raw := range sources {
		filename := filepath.Join(cfg.Content, path)
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	a, err := app.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	})
	return a, cfg
}

func connect(t *testing.T, a *app.App, version string) *mcp.ClientSession {
	t.Helper()
	st, ct := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- mcpserver.New(a).Run(ctx, st) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, &mcp.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		session.Close()
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("server did not stop")
		}
	})
	return session
}

func TestDiscoverySchemasAndProtocolVersionsWithoutInference(t *testing.T) {
	for _, version := range []string{"", "2025-06-18"} {
		t.Run(version, func(t *testing.T) {
			s := connect(t, newTestApp(t), version)
			result, err := s.ListTools(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]bool{"search": true, "documents_list": true, "document_get": true, "document_create": true, "document_update": true, "document_move": true, "document_delete": true, "folders_list": true, "folder_create": true, "folder_move": true, "folder_delete": true, "tasks_list": true, "task_set_state": true, "task_history": true}
			if len(result.Tools) != len(want) {
				t.Fatalf("tools=%+v", result.Tools)
			}
			for _, tool := range result.Tools {
				if !want[tool.Name] || tool.InputSchema == nil || tool.OutputSchema == nil {
					t.Fatalf("tool=%+v", tool)
				}
				var schema map[string]any
				b, _ := json.Marshal(tool.OutputSchema)
				json.Unmarshal(b, &schema)
				if schema["type"] != "object" {
					t.Fatalf("non-object output: %s", b)
				}
				var input struct {
					Type       string                     `json:"type"`
					Required   []string                   `json:"required"`
					Properties map[string]json.RawMessage `json:"properties"`
				}
				inputJSON, _ := json.Marshal(tool.InputSchema)
				if err := json.Unmarshal(inputJSON, &input); err != nil || input.Type != "object" {
					t.Fatalf("input schema: %s %v", inputJSON, err)
				}
				if tool.Name == "document_update" && (!slices.Contains(input.Required, "if_revision") || !slices.Contains(input.Required, "raw") || input.Properties["id"] == nil || input.Properties["path"] == nil) {
					t.Fatalf("update schema: %s", inputJSON)
				}
				if tool.Name == "tasks_list" || tool.Name == "documents_list" {
					for _, forbidden := range []string{`"body"`, `"raw"`, `"history"`} {
						if bytes.Contains(b, []byte(forbidden)) {
							t.Fatalf("list output schema exposes %s: %s", forbidden, b)
						}
					}
					if !bytes.Contains(b, []byte(`"revision"`)) || !bytes.Contains(b, []byte(`"created_at"`)) || !bytes.Contains(b, []byte(`"state_changed_at"`)) {
						t.Fatalf("incomplete metadata schema: %s", b)
					}
				}
				if tool.Name == "documents_list" {
					type schemaShape struct {
						Properties map[string]schemaShape `json:"properties"`
						Items      *schemaShape           `json:"items"`
						Required   []string               `json:"required"`
						Type       json.RawMessage        `json:"type"`
					}
					var shape schemaShape
					if err := json.Unmarshal(b, &shape); err != nil {
						t.Fatal(err)
					}
					list := shape.Properties["data"]
					diagnostics := list.Properties["diagnostics"]
					if !slices.Contains(list.Required, "diagnostics") || diagnostics.Items == nil || len(diagnostics.Items.Properties) != 3 {
						t.Fatalf("missing typed diagnostic output schema: %s", b)
					}
					for _, field := range []string{"path", "code", "message"} {
						if !slices.Contains(diagnostics.Items.Required, field) || string(diagnostics.Items.Properties[field].Type) != `"string"` {
							t.Fatalf("missing diagnostic %s schema: %s", field, b)
						}
					}
				}
				if tool.Name == "document_delete" || tool.Name == "folder_delete" || tool.Name == "document_update" {
					if tool.Annotations == nil || tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
						t.Fatalf("destructive annotation: %+v", tool)
					}
				}
			}
			if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".cache", "recall")); !os.IsNotExist(err) {
				t.Fatalf("handshake started worker: %v", err)
			}
		})
	}
}

func TestRunCancellationUnblocksBackpressuredOutput(t *testing.T) {
	a := newTestApp(t)
	r, w := io.Pipe()
	outR, outW := io.Pipe()
	defer outR.Close()
	defer w.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- mcpserver.Run(ctx, a, r, outW) }()
	_, err := io.WriteString(w, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"backpressure","version":"1"}}}`+"\n")
	if err != nil {
		t.Fatal(err)
	}
	// No output reader: cancellation must retain the supplied writer's Close.
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked output retained session")
	}
}

func TestRunCancellationClosesIdleInputAndOutput(t *testing.T) {
	a := newTestApp(t)
	r, w := io.Pipe()
	defer w.Close()
	outR, outW := io.Pipe()
	defer outR.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- mcpserver.Run(ctx, a, r, outW) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation retained idle reader")
	}
	if _, err := w.Write([]byte("x")); err == nil {
		t.Fatal("input not closed")
	}
	if b, err := io.ReadAll(outR); err != nil || len(b) != 0 {
		t.Fatalf("output not closed: %q %v", b, err)
	}
}

func TestRunInitializationClientProtocolOnlyAndEOF(t *testing.T) {
	a := newTestApp(t)
	r, w := io.Pipe()
	outR, outW := io.Pipe()
	defer outR.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- mcpserver.Run(ctx, a, r, outW) }()
	frames := bufio.NewScanner(outR)
	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"legacy","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	}
	for n, request := range requests {
		if _, err := io.WriteString(w, request+"\n"); err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			continue
		}
		if !frames.Scan() {
			t.Fatal("missing response", frames.Err())
		}
		var response struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      int             `json:"id"`
			Result  json.RawMessage `json:"result"`
			Error   any             `json:"error"`
		}
		if err := json.Unmarshal(frames.Bytes(), &response); err != nil || response.JSONRPC != "2.0" || response.Error != nil || response.ID != map[int]int{0: 1, 2: 2}[n] {
			t.Fatalf("non-protocol stdout: %s %v", frames.Bytes(), err)
		}
		if n == 0 && !bytes.Contains(response.Result, []byte(`"protocolVersion":"2025-06-18"`)) {
			t.Fatalf("init: %s", response.Result)
		}
		if n == 2 && !bytes.Contains(response.Result, []byte(`"name":"document_delete"`)) {
			t.Fatalf("tools: %s", response.Result)
		}
	}
	w.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("EOF did not stop server")
	}
	if frames.Scan() {
		t.Fatalf("trailing stdout: %s", frames.Bytes())
	}
}
