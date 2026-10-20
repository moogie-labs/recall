package app

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"recall/internal/config"
	"recall/internal/fsstore"
	"recall/internal/model"
)

func TestIndexFailurePreservesSourceAndRecovers(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	d := mustCreate(t, a, CreateInput{Kind: model.Task, Name: "work", Body: "old"})
	db, err := sql.Open("sqlite", cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER injected_failure BEFORE UPDATE ON documents WHEN NEW.body = 'saved' BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	_, err = a.Update(t.Context(), model.Ref{ID: d.ID}, d.Revision, strings.Replace(d.Raw, "old", "saved", 1))
	wantCode(t, err, "index_unavailable")
	raw, err := os.ReadFile(filepath.Join(cfg.Content, d.Path))
	if err != nil || !strings.HasSuffix(string(raw), "saved") {
		t.Fatalf("source lost after index failure: %q %v", raw, err)
	}
	if _, err := db.Exec("DROP TRIGGER injected_failure"); err != nil {
		t.Fatal(err)
	}
	report, err := a.Sync(t.Context())
	if err != nil || report.Updated != 1 {
		t.Fatalf("recovery: %+v %v", report, err)
	}
	page, err := a.Tasks(t.Context(), model.TaskQuery{})
	if err != nil || len(page.Items) != 1 || page.Items[0].Body != "saved" {
		t.Fatalf("recovered index: %+v %v", page, err)
	}
}

func TestStartupAndSyncRefreshCompleteSnapshot(t *testing.T) {
	a, cfg := newTestApp(t)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Content, "direct.task.md"), []byte("body"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	page, err := a.index.Tasks(t.Context(), model.TaskQuery{})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID == "" {
		t.Fatalf("startup: %+v %v", page, err)
	}
	if err := os.Remove(filepath.Join(cfg.Content, "direct.task.md")); err != nil {
		t.Fatal(err)
	}
	report, err := a.Sync(t.Context())
	if err != nil || report.Removed != 1 {
		t.Fatalf("sync deletion: %+v %v", report, err)
	}
}

// Child helpers use real app/store instances, never an in-memory lock stand-in.
func TestAppProcessHelper(t *testing.T) {
	mode := os.Getenv("RECALL_APP_HELPER")
	if mode == "" {
		return
	}
	cfg, err := config.Resolve(os.Getenv("RECALL_APP_ROOT"))
	if err != nil {
		panic(err)
	}
	if mode == "lock" {
		s, err := fsstore.Open(cfg)
		if err != nil {
			panic(err)
		}
		defer s.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		err = s.WithLock(ctx, func() error { return nil })
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Print("blocked")
			os.Exit(0)
		}
		fmt.Printf("unexpected lock result: %v", err)
		os.Exit(1)
	}
	a, err := Open(context.Background(), cfg)
	if err != nil {
		panic(err)
	}
	defer a.Close()
	fmt.Println("ready")
	var b [1]byte
	if _, err := os.Stdin.Read(b[:]); err != nil {
		panic(err)
	}
	raw, err := base64.StdEncoding.DecodeString(os.Getenv("RECALL_APP_RAW"))
	if err != nil {
		panic(err)
	}
	_, err = a.Update(context.Background(), model.Ref{ID: os.Getenv("RECALL_APP_ID")}, os.Getenv("RECALL_APP_REVISION"), string(raw))
	if err == nil {
		fmt.Print("success")
	} else {
		fmt.Print(model.ErrorCode(err))
	}
	if err := a.Close(); err != nil {
		panic(err)
	}
	os.Exit(0)
}

func helperCommand(t testing.TB, cfg config.Config, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestAppProcessHelper$")
	cmd.Env = append(os.Environ(), "RECALL_APP_HELPER="+mode, "RECALL_APP_ROOT="+cfg.Root)
	return cmd
}

func TestIndependentProcessesConflictOnOneRevision(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	d := mustCreate(t, a, CreateInput{Kind: model.Task, Name: "work", Body: "old"})
	type child struct {
		release func()
		result  func() string
	}
	children := make([]child, 0, 2)
	for _, body := range []string{"first", "second"} {
		cmd := helperCommand(t, cfg, "update")
		cmd.Env = append(cmd.Env, "RECALL_APP_ID="+d.ID, "RECALL_APP_REVISION="+d.Revision, "RECALL_APP_RAW="+base64.StdEncoding.EncodeToString([]byte(strings.Replace(d.Raw, "old", body, 1))))
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		// Read the short readiness line without buffering away the result.
		for _, want := range []byte("ready\n") {
			var b [1]byte
			if _, err := stdout.Read(b[:]); err != nil || b[0] != want {
				t.Fatalf("child startup: %q %v", b, err)
			}
		}
		children = append(children, child{release: func() { stdin.Write([]byte("x")); stdin.Close() }, result: func() string {
			raw, err := io.ReadAll(stdout)
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			return string(raw)
		}})
	}
	for _, c := range children {
		c.release()
	}
	results := map[string]int{}
	for _, c := range children {
		results[c.result()]++
	}
	if results["success"] != 1 || results["conflict"] != 1 {
		t.Fatalf("competing results: %v", results)
	}
	read, err := a.Read(t.Context(), model.Ref{ID: d.ID})
	if err != nil || (read.Body != "first" && read.Body != "second") {
		t.Fatalf("winner lost: %+v %v", read, err)
	}
}

func TestMutationHoldsOuterLockThroughCompleteIndexApply(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	d := mustCreate(t, a, CreateInput{Kind: model.Task, Name: "work", Body: "old"})
	db, err := sql.Open("sqlite", cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("ROLLBACK")
	done := make(chan error, 1)
	go func() {
		_, err := a.Update(t.Context(), model.Ref{ID: d.ID}, d.Revision, strings.Replace(d.Raw, "old", "new", 1))
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, err := os.ReadFile(filepath.Join(cfg.Content, d.Path))
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(string(raw), "new") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("source mutation never reached index barrier")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cmd := helperCommand(t, cfg, "lock")
	if output, err := cmd.CombinedOutput(); err != nil || string(output) != "blocked" {
		t.Fatalf("outer lock released before Apply: %q %v", output, err)
	}
	if _, err := db.Exec("COMMIT"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	page, err := a.index.Tasks(t.Context(), model.TaskQuery{})
	if err != nil || len(page.Items) != 1 || page.Items[0].Body != "new" {
		t.Fatalf("post-barrier snapshot: %+v %v", page, err)
	}
}
