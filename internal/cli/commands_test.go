package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"recall/assets"
	"recall/internal/bootstrap"
	"recall/internal/config"
	"recall/internal/embedding"
	"recall/internal/model"
)

func invoke(t *testing.T, root, input string, args ...string) result {
	t.Helper()
	var out, logs bytes.Buffer
	code := Run(t.Context(), append([]string{"--root", root, "--json"}, args...), strings.NewReader(input), &out, &logs)
	got := decodeResult(t, &out)
	if code != 0 || got.Error != nil {
		t.Fatalf("%v: exit %d %+v logs=%s", args, code, got, &logs)
	}
	return got
}

func as[T any](t *testing.T, got result) T {
	t.Helper()
	var data T
	if err := json.Unmarshal(got.Data, &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCRUDHistoryAndKeywordFromUnrelatedCWDWithoutInference(t *testing.T) {
	cfg := prepared(t) // No runtime or model: semantic operations must fail, never metadata.
	t.Chdir(t.TempDir())
	t.Setenv("RECALL_HOME", cfg.Root)
	invoke(t, cfg.Root, "", "folder", "create", "projects")
	invoke(t, cfg.Root, "", "folder", "create", "projects/sub")
	note := as[model.Document](t, invoke(t, cfg.Root, "free-form café\r\n", "document", "create", "--type", "note", "--folder", "projects", "--name", "guide", "--title", "Guide"))
	if note.Path != "projects/guide.note.md" || note.Body != "free-form café\r\n" {
		t.Fatalf("%+v", note)
	}
	task := as[model.Document](t, invoke(t, cfg.Root, "reset password", "document", "create", "--type", "task", "--folder", "projects/sub", "--name", "reset"))
	if task.State != model.Planned {
		t.Fatal(task)
	}
	for _, state := range []string{"completed", "in_progress", "in_progress", "stalled"} {
		task = as[model.Document](t, invoke(t, cfg.Root, "", "task", "state", "--id", task.ID, "--state", state))
	}
	history := as[[]model.Event](t, invoke(t, cfg.Root, "", "task", "history", "--path", task.Path))
	if len(history) != 4 || history[1].State != model.Completed || history[3].State != model.Stalled {
		t.Fatalf("%+v", history)
	}
	page := as[model.Page[model.DocumentSummary]](t, invoke(t, cfg.Root, "", "task", "list", "--folder", "projects", "--state", "stalled", "--date-field", "transition", "--event-state", "completed", "--sort", "state_changed", "--desc"))
	if len(page.Items) != 1 || page.Items[0].ID != task.ID {
		t.Fatalf("%+v", page)
	}
	page = as[model.Page[model.DocumentSummary]](t, invoke(t, cfg.Root, "", "task", "list", "--folder", "projects", "--recursive=false"))
	if len(page.Items) != 0 {
		t.Fatal(page)
	}
	updated := as[model.Document](t, invoke(t, cfg.Root, strings.Replace(note.Raw, "free-form café", "updated café", 1), "document", "update", "--id", note.ID, "--if-revision", note.Revision))
	if updated.Revision == note.Revision || updated.Body != "updated café\r\n" {
		t.Fatal(updated)
	}
	var out, logs bytes.Buffer
	if code := Run(t.Context(), []string{"--root", cfg.Root, "--json", "document", "update", "--id", note.ID, "--if-revision", note.Revision}, strings.NewReader(note.Raw), &out, &logs); code != 4 || decodeResult(t, &out).Error.Code != "conflict" {
		t.Fatalf("stale update %d %s", code, &logs)
	}
	invoke(t, cfg.Root, "", "document", "move", "--id", note.ID, "--to", "projects/sub/renamed.note.md")
	invoke(t, cfg.Root, "", "folder", "move", "projects", "archive")
	read := as[model.Document](t, invoke(t, cfg.Root, "", "document", "get", "--id", note.ID))
	if read.Path != "archive/sub/renamed.note.md" {
		t.Fatal(read)
	}
	hits := as[[]model.Hit](t, invoke(t, cfg.Root, "", "search", "updated", "--mode", "keyword", "--folder", "archive", "--type", "note"))
	if len(hits) != 1 || hits[0].ID != note.ID {
		t.Fatal(hits)
	}
	invoke(t, cfg.Root, "", "status")
	invoke(t, cfg.Root, "", "document", "list", "--folder", "archive")
	invoke(t, cfg.Root, "", "folder", "list")
	invoke(t, cfg.Root, "", "sync", "--rebuild")
	out.Reset()
	if code := Run(t.Context(), []string{"--root", cfg.Root, "--json", "sync", "--wait"}, nil, &out, &logs); code != 1 {
		t.Fatalf("inference unexpectedly usable: %d %s", code, &out)
	}
	out.Reset()
	if code := Run(t.Context(), []string{"--root", cfg.Root, "--json", "folder", "delete", "archive"}, nil, &out, &logs); code != 4 || decodeResult(t, &out).Error.Code != "folder_not_empty" {
		t.Fatalf("nonrecursive delete: %d %s", code, &logs)
	}
	invoke(t, cfg.Root, "input must not become a confirmation", "document", "delete", "--id", task.ID)
	invoke(t, cfg.Root, "", "folder", "delete", "archive", "--recursive")
	if _, err := os.Stat(filepath.Join(cfg.Content, "archive")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestBodyAndRawFilesEmptyCreationAndEditorReconciliation(t *testing.T) {
	cfg := prepared(t)
	// Any accidental editor invocation on ordinary commands leaves a marker.
	marker := filepath.Join(t.TempDir(), "unexpected-editor")
	trap := filepath.Join(t.TempDir(), "editor trap")
	if err := os.WriteFile(trap, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 9\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Editor = []string{trap}
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "body.txt")
	if err := os.WriteFile(file, []byte("from body file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	note := as[model.Document](t, invoke(t, cfg.Root, "ignored stdin", "document", "create", "--type", "note", "--name", "file", "--body-file", file))
	if note.Body != "from body file\n" {
		t.Fatal(note)
	}
	empty := as[model.Document](t, invoke(t, cfg.Root, "", "document", "create", "--type", "note", "--name", "empty"))
	if empty.Body != "" {
		t.Fatal(empty)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("creation launched an editor")
	}
	if err := os.WriteFile(file, []byte(strings.Replace(note.Raw, "from body file", "from raw file", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	note = as[model.Document](t, invoke(t, cfg.Root, "", "document", "update", "--path", note.Path, "--if-revision", note.Revision, "--raw-file", file))
	// The editor writes the actual managed path, emits stdout, then fails.
	script := filepath.Join(t.TempDir(), "editor fixture")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '\\neditor saved\\n' >> \"$1\"\nprintf 'editor stdout\\n'\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Editor = []string{script}
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	var out, logs bytes.Buffer
	if code := Run(t.Context(), []string{"--root", cfg.Root, "document", "edit", "--id", note.ID, "--json"}, strings.NewReader(""), &out, &logs); code != 1 {
		t.Fatalf("%d %s %s", code, &out, &logs)
	}
	decodeResult(t, &out)
	if !strings.Contains(logs.String(), "editor stdout") {
		t.Fatal("editor polluted JSON or lost output")
	}
	hits := as[[]model.Hit](t, invoke(t, cfg.Root, "", "search", "saved", "--mode", "keyword"))
	if len(hits) != 1 || hits[0].ID != note.ID {
		t.Fatal("nonzero editor exit was not reconciled")
	}
}

func TestHumanSearchIncludesTheMatchedSourcePassage(t *testing.T) {
	cfg := prepared(t)
	invoke(t, cfg.Root, "needle useful source passage\n", "document", "create", "--type", "note", "--name", "guide")
	var out, logs bytes.Buffer
	code := Run(t.Context(), []string{"--root", cfg.Root, "search", "needle", "--mode", "keyword"}, nil, &out, &logs)
	if code != 0 || !strings.Contains(out.String(), "guide.note.md") || !strings.Contains(out.String(), "needle useful source passage") {
		t.Fatalf("%d stdout=%s stderr=%s", code, &out, &logs)
	}
	// The default must stay hybrid, not silently downgrade on inference failure.
	out.Reset()
	code = Run(t.Context(), []string{"--root", cfg.Root, "search", "needle", "--json"}, nil, &out, &logs)
	got := decodeResult(t, &out)
	if code != 1 || got.Error == nil || got.Error.Code != "runtime_unavailable" {
		t.Fatalf("%d %+v", code, got)
	}
}

func TestDateFiltersUsePersistedTimezoneAndPagination(t *testing.T) {
	cfg := prepared(t)
	for name, at := range map[string]string{"early": "2026-03-08T04:30:00Z", "inside": "2026-03-08T05:30:00Z", "late": "2026-03-09T04:30:00Z"} {
		raw := "---\ntype: task\nid: " + name + "\nstate: planned\nstate_history:\n  - state: planned\n    at: " + at + "\n---\nneedle\n"
		if err := os.WriteFile(filepath.Join(cfg.Content, name+".task.md"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"--date-field", "created", "--from", "2026-03-08", "--through", "2026-03-08"}
	page := as[model.Page[model.DocumentSummary]](t, invoke(t, cfg.Root, "", append([]string{"task", "list"}, args...)...))
	if len(page.Items) != 1 || page.Items[0].ID != "inside" {
		t.Fatal(page)
	}
	hits := as[[]model.Hit](t, invoke(t, cfg.Root, "", append([]string{"search", "needle", "--mode", "keyword"}, args...)...))
	if len(hits) != 1 || hits[0].ID != "inside" {
		t.Fatal(hits)
	}
	page = as[model.Page[model.DocumentSummary]](t, invoke(t, cfg.Root, "", "task", "list", "--sort", "title", "--limit", "1", "--offset", "1"))
	if len(page.Items) != 1 || page.Items[0].ID != "inside" || page.NextOffset == nil || *page.NextOffset != 2 {
		t.Fatal(page)
	}
}

func TestExecutablePinnedNativeWorkflow(t *testing.T) {
	assetDir := os.Getenv("RECALL_NATIVE_ASSETS")
	if assetDir == "" {
		t.Skip("set RECALL_NATIVE_ASSETS to checksum-pinned validation assets")
	}
	assetDir, err := filepath.Abs(assetDir)
	if err != nil {
		t.Fatal(err)
	}
	bin := buildExecutable(t)
	cfg := prepared(t)
	t.Setenv("HOME", shortHome(t))
	var m, rt bootstrap.Manifest
	if err := json.Unmarshal([]byte(assets.ModelLock()), &m); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(assets.RuntimeLock()), &rt); err != nil {
		t.Fatal(err)
	}
	cfg.ModelID, cfg.ModelSHA256, cfg.ModelSignature, cfg.RuntimeID = m.ID, m.SHA256, "sha256:"+m.SHA256, rt.ID
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(assetDir, m.Filename), filepath.Join(cfg.ModelsDir, m.Filename)); err != nil {
		t.Fatal(err)
	}
	bundle := t.TempDir()
	archive := filepath.Join(bundle, "runtime", "darwin-arm64", rt.Filename)
	if err := os.MkdirAll(filepath.Dir(archive), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(assetDir, rt.Filename), archive); err != nil {
		t.Fatal(err)
	}
	if _, err := bootstrap.Initialize(t.Context(), cfg, bundle, nil); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RECALL_ASSETS_DIR", bundle)
	// Own the manager as an exact child so acceptance can shut it down without
	// PID lookup/signalling or waiting out the production 120-second idle period.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	worker := exec.CommandContext(ctx, bin, "_embedding-worker", "--root", cfg.Root)
	worker.Cancel = func() error { return worker.Process.Signal(syscall.SIGTERM) }
	var workerOut, workerLogs bytes.Buffer
	worker.Stdout, worker.Stderr = &workerOut, &workerLogs
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(cfg.Root)))[:16]
	manager := filepath.Join(os.Getenv("HOME"), ".cache", "recall", "sockets", key+".sock")
	nativeSocket := strings.TrimSuffix(manager, ".sock") + ".native.sock"
	t.Cleanup(func() {
		worker.Process.Signal(syscall.SIGTERM)
		worker.Wait()
		if workerOut.Len() != 0 {
			t.Errorf("private stdout: %s", &workerOut)
		}
		for _, path := range []string{manager, nativeSocket} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("socket retained: %s: %v", path, err)
			}
		}
		processes, err := exec.Command("ps", "-axo", "args").Output()
		if err != nil || strings.Contains(string(processes), filepath.Join(cfg.RuntimeDir, "b11669", "llama-server")) {
			t.Error("owned native process not reaped", err)
		}
		t.Log("real executable manager/guardian/native shutdown: sockets removed; native reaped; private stdout empty")
	})
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", manager)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	started := time.Now()
	for {
		request, _ := http.NewRequestWithContext(ctx, "GET", "http://unix/health", nil)
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("real worker startup timeout")
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Logf("real executable worker/guardian/native startup: %s", time.Since(started))
	cwd := t.TempDir()
	run := func(input string, args ...string) result {
		t.Helper()
		command := exec.CommandContext(ctx, bin, append([]string{"--root", cfg.Root, "--json"}, args...)...)
		command.Dir = cwd
		command.Stdin = strings.NewReader(input)
		var out, logs bytes.Buffer
		command.Stdout, command.Stderr = &out, &logs
		if err := command.Run(); err != nil {
			t.Fatalf("%v: %v stdout=%s stderr=%s", args, err, &out, &logs)
		}
		got := decodeResult(t, &out)
		if got.Error != nil {
			t.Fatalf("%v: %+v", args, got.Error)
		}
		return got
	}
	started = time.Now()
	ready := run("", "init")
	if !bytes.Contains(ready.Data, []byte(`"semantic_ready":true`)) {
		t.Fatalf("init: %s", ready.Data)
	}
	t.Logf("real executable init asset verification plus inference: %s", time.Since(started))
	provider := embedding.NewClient(cfg)
	vectors, err := provider.Embed(ctx, "query", []string{"how do I reset my account password?"})
	if err != nil || len(vectors) != 1 || !validSmokeVector(vectors[0]) {
		t.Fatal("real vector shape/norm", err)
	}
	t.Logf("real private provider: %d finite L2-normalized components", len(vectors[0]))
	run("", "folder", "create", "guides")
	related := as[model.Document](t, run("To reset your account password, open account settings and choose Reset password.", "document", "create", "--type", "note", "--name", "account", "--folder", "guides"))
	run("A sourdough bread recipe uses flour, water and salt.", "document", "create", "--type", "note", "--name", "bread", "--folder", "guides")
	run("", "sync", "--rebuild", "--wait")
	for _, mode := range []string{"keyword", "semantic", "hybrid"} {
		started = time.Now()
		hits := as[[]model.Hit](t, run("", "search", "reset password", "--mode", mode, "--folder", "guides"))
		if len(hits) == 0 || hits[0].ID != related.ID {
			t.Fatalf("%s ranking: %+v", mode, hits)
		}
		t.Logf("real executable %s search: %d hit(s), related first, %s", mode, len(hits), time.Since(started))
	}
	queryExecutableMCP(t, bin, cfg.Root, related.ID, []string{"keyword", "semantic", "hybrid"})
	run("", "init") // Re-verification and inference are retryable.
}
