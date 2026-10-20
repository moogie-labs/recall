package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"recall/internal/model"
)

func TestJSONListsOmitLargeBodyButGetAndHistoryRemainExplicit(t *testing.T) {
	cfg := prepared(t)
	body := strings.Repeat("distinctive-heavy-payload-", 10000)
	for _, kind := range []string{"note", "task"} {
		d := as[model.Document](t, invoke(t, cfg.Root, body, "document", "create", "--type", kind, "--name", kind))
		got := as[model.Document](t, invoke(t, cfg.Root, "", "document", "get", "--id", d.ID))
		if got.Body != body || !strings.Contains(got.Raw, body) {
			t.Fatal("get lost full body")
		}
		if kind == "task" && len(as[[]model.Event](t, invoke(t, cfg.Root, "", "task", "history", "--id", d.ID))) != 1 {
			t.Fatal("history lost")
		}
	}
	for _, args := range [][]string{{"document", "list"}, {"task", "list"}} {
		r := invoke(t, cfg.Root, "", args...)
		for _, forbidden := range []string{"distinctive-heavy-payload", `"body"`, `"raw"`, `"history"`} {
			if bytes.Contains(r.Data, []byte(forbidden)) {
				t.Fatalf("%v leaks %s", args, forbidden)
			}
		}
		if len(r.Data) > 2000 {
			t.Fatalf("unbounded list %d", len(r.Data))
		}
		if !bytes.Contains(r.Data, []byte(`"revision"`)) {
			t.Fatal("revision omitted")
		}
	}
}

func queryExecutableMCP(t *testing.T, bin, root, wantID string, modes []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, bin, "mcp", "--root", root)
	command.Dir = t.TempDir()
	var logs bytes.Buffer
	command.Stderr = &logs
	s, err := mcp.NewClient(&mcp.Implementation{Name: "executable-test", Version: "1"}, nil).Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
		if logs.Len() != 0 {
			t.Errorf("MCP stderr: %s", &logs)
		}
	}()
	for _, mode := range modes {
		r, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "search", Arguments: map[string]any{"text": "reset password", "mode": mode, "limit": 2}})
		if err != nil || r.IsError {
			t.Fatalf("%s: %+v %v", mode, r, err)
		}
		b, _ := json.Marshal(r.StructuredContent)
		var envelope struct {
			Data struct {
				Hits []model.Hit `json:"hits"`
			} `json:"data"`
		}
		if err := json.Unmarshal(b, &envelope); err != nil || len(envelope.Data.Hits) == 0 || envelope.Data.Hits[0].ID != wantID {
			t.Fatalf("%s ranking: %s %v", mode, b, err)
		}
		t.Logf("real executable MCP %s: %d hit(s), related first", mode, len(envelope.Data.Hits))
	}
}

func TestExecutableMCPKeywordWorkflowWithoutInference(t *testing.T) {
	bin := buildExecutable(t)
	cfg := prepared(t)
	d := as[model.Document](t, invoke(t, cfg.Root, "reset password in account settings", "document", "create", "--type", "note", "--name", "account"))
	queryExecutableMCP(t, bin, cfg.Root, d.ID, []string{"keyword"})
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".cache", "recall")); !os.IsNotExist(err) {
		t.Fatalf("keyword started worker: %v", err)
	}
}

func TestDefaultMCPAdapterProtocolEOFAndJSONGuard(t *testing.T) {
	cfg := prepared(t)
	r, w := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var logs bytes.Buffer
	done := make(chan int, 1)
	stop := context.AfterFunc(ctx, func() { r.Close(); w.Close(); outR.Close(); outW.Close() })
	defer stop()
	go func() {
		code := Run(ctx, []string{"--root", cfg.Root, "mcp"}, r, outW, &logs)
		r.Close()
		outW.Close()
		done <- code
	}()
	s, err := mcp.NewClient(&mcp.Implementation{Name: "cli-test", Version: "1"}, nil).Connect(ctx, &mcp.IOTransport{Reader: outR, Writer: w}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := s.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 14 {
		t.Fatalf("%+v %v", tools, err)
	}
	s.Close()
	select {
	case code := <-done:
		if code != 0 || logs.Len() != 0 {
			t.Fatalf("%d logs=%s", code, &logs)
		}
	case <-ctx.Done():
		t.Fatal("MCP did not terminate on EOF")
	}
	var out bytes.Buffer
	if code := Run(t.Context(), []string{"--root", cfg.Root, "mcp", "--json"}, strings.NewReader(""), &out, &logs); code != 2 || !bytes.Contains(out.Bytes(), []byte(`"code":"invalid_argument"`)) {
		t.Fatalf("JSON mode: %d %s", code, &out)
	}
}
