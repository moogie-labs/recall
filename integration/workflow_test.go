package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"recall/internal/model"
)

func TestExecutableRejectsUninitializedRoot(t *testing.T) {
	isolated(t)
	out, code := runBinary(t, "--root", t.TempDir(), "document", "list", "--json")
	if code != 3 || !strings.Contains(out, `"not_initialized"`) {
		t.Fatalf("%d %s", code, out)
	}
}

func TestCentralCollectionAdoptionAndSourceRecovery(t *testing.T) {
	root := prepared(t)
	plain := []byte("# ordinary Markdown\nkept verbatim\n")
	if err := os.WriteFile(filepath.Join(root, "content", "plain.md"), plain, 0600); err != nil {
		t.Fatal(err)
	}
	other := []byte("not markdown\n")
	if err := os.WriteFile(filepath.Join(root, "content", "other.txt"), other, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "content", "external.md"), []byte("external body\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	command[model.SyncReport](t, "sync") // startup reconciliation already adopted the Markdown files
	d := command[model.Document](t, "document", "get", "--path", "external.md")
	t.Chdir(t.TempDir())
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(filepath.Join(root, "index.sqlite") + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	command[model.SyncReport](t, "sync", "--rebuild")
	got := command[model.Document](t, "document", "get", "--id", d.ID)
	if !reflect.DeepEqual(got, d) {
		t.Fatal("rebuild changed source document")
	}
	adopted := command[model.Document](t, "document", "get", "--path", "plain.md")
	if adopted.ID == "" || adopted.Body != string(plain) || !strings.HasSuffix(adopted.Raw, string(plain)) {
		t.Fatalf("plain Markdown not adopted intact: %+v", adopted)
	}
	if got, err := os.ReadFile(filepath.Join(root, "content", "other.txt")); err != nil || !bytes.Equal(got, other) {
		t.Fatal("non-Markdown file changed")
	}
}

func connectMCP(t *testing.T, cmd *exec.Cmd) *mcp.ClientSession {
	t.Helper()
	var logs bytes.Buffer
	cmd.Stderr = &logs
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	s, err := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "1"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
		cancel()
		if logs.Len() != 0 {
			t.Errorf("MCP stderr: %s", &logs)
		}
	})
	return s
}

func tool[T any](t *testing.T, s *mcp.ClientSession, name string, args map[string]any) T {
	t.Helper()
	r, err := s.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(r.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if r.IsError {
		t.Fatalf("%s: %s", name, b)
	}
	return result[T](t, string(b), 0)
}

func TestExecutableMCPCLICompleteCRUD(t *testing.T) {
	root := prepared(t)
	s := connectMCP(t, exec.Command(executable(t), "--root", root, "mcp"))
	tool[any](t, s, "folder_create", map[string]any{"path": "inbox"})
	for _, name := range []string{"first", "second"} {
		d := tool[model.Document](t, s, "document_create", map[string]any{"name": name, "folder": "inbox", "body": "private acceptance needle"})
		got := command[model.Document](t, "document", "get", "--id", d.ID)
		if !reflect.DeepEqual(got, d) {
			t.Fatal("CLI/MCP get mismatch")
		}
		out, code := runInput(t, strings.Replace(d.Raw, "private acceptance needle", "updated needle", 1), "--json", "document", "update", "--id", d.ID, "--if-revision", d.Revision)
		d = result[model.Document](t, out, code)
		got = tool[model.Document](t, s, "document_get", map[string]any{"id": d.ID})
		if !reflect.DeepEqual(got, d) {
			t.Fatal("CLI update not visible in MCP")
		}
		d = tool[model.Document](t, s, "document_update", map[string]any{"id": d.ID, "if_revision": d.Revision, "raw": strings.Replace(d.Raw, "updated needle", "MCP needle", 1)})
		d = tool[model.Document](t, s, "document_move", map[string]any{"id": d.ID, "to": "moved." + name + ".md", "if_revision": d.Revision})
		got = command[model.Document](t, "document", "get", "--path", d.Path)
		if got.ID != d.ID {
			t.Fatal("move changed identity")
		}
		h := tool[struct {
			Hits []model.Hit `json:"hits"`
		}](t, s, "search", map[string]any{"text": "needle", "mode": "keyword"})
		if len(h.Hits) != 1 || h.Hits[0].ID != d.ID {
			t.Fatal("MCP keyword mismatch")
		}
		tool[any](t, s, "document_delete", map[string]any{"id": d.ID, "if_revision": d.Revision})
		out, code = runBinary(t, "--json", "document", "get", "--id", d.ID)
		if code != 3 || !strings.Contains(out, "not_found") {
			t.Fatal("deleted source returned")
		}
	}
	tool[any](t, s, "folder_move", map[string]any{"from": "inbox", "to": "empty"})
	tool[any](t, s, "folder_delete", map[string]any{"path": "empty"})
	for _, name := range []string{"documents_list", "folders_list"} {
		tool[any](t, s, name, map[string]any{})
	}
	entries, err := os.ReadDir(filepath.Join(root, "content"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("deletion residue: %v %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".cache", "recall")); !os.IsNotExist(err) {
		t.Fatal("CRUD/keyword launched inference")
	}
}
