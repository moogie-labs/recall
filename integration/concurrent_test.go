package integration

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"recall/internal/model"
)

func TestConcurrentExecutablesShareCollection(t *testing.T) {
	prepared(t)
	executable(t)
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Go(func() { create(t, "task", fmt.Sprintf("task-%d", n), "body") })
	}
	wg.Wait()
	p := command[model.Page[model.DocumentSummary]](t, "task", "list")
	if len(p.Items) != 8 {
		t.Fatalf("lost concurrent write: %d", len(p.Items))
	}
}

// Real inference only. A second public semantic search must observe an already
// leased job (indexing), proving deletion overlaps indexing rather than guessing
// that a background command is still busy. No private DB/protocol hooks.
func deleteDuringRealIndexing(t *testing.T, root string) {
	t.Helper()
	d := create(t, "task", "inflight", strings.Repeat("deleteinflighttoken credentials recovery source. ", 10000))
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable(t), "--root", root, "--json", "sync", "--wait")
	var output, logs bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { cancel(); <-ctx.Done() })
	time.Sleep(100 * time.Millisecond)
	out, code := runBinary(t, "--json", "search", "deleteinflighttoken", "--mode", "semantic")
	if code != 1 || !strings.Contains(out, `"code":"indexing"`) {
		cancel()
		<-done
		t.Fatalf("did not establish an in-flight embedding lease: %d %s", code, out)
	}
	command[any](t, "document", "delete", "--id", d.ID, "--if-revision", d.Revision)
	err := <-done
	if err != nil && !strings.Contains(output.String(), `"code":"indexing"`) {
		t.Fatalf("indexing process: %v %s %s", err, &output, &logs)
	}
	if logs.Len() != 0 {
		t.Fatalf("indexing stderr: %s", &logs)
	}
	if _, err := os.Stat(filepath.Join(root, "content", d.Path)); !os.IsNotExist(err) {
		t.Fatal("deleted source resurrected")
	}
	if hits := command[[]model.Hit](t, "search", "deleteinflighttoken", "--mode", "keyword"); len(hits) != 0 {
		t.Fatal("deleted document retained searchable derived data")
	}
	out, code = runBinary(t, "--json", "task", "history", "--id", d.ID)
	if code != 3 || !strings.Contains(out, "not_found") {
		t.Fatal("deleted history remained")
	}
	t.Log("PASS real executable deletion during publicly observed in-flight semantic indexing; source, retrieval and task history absent afterward")
}
