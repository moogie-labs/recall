package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"recall/internal/cli"
	"recall/internal/config"
	"recall/internal/model"
)

var binary string
var buildOnce sync.Once
var buildError error
var repository = func() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(file))
}()

// Dispatch before testing flags: never recursively run tests as an inference worker.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "_embedding-worker" {
		os.Exit(cli.Run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	code := m.Run()
	if binary != "" {
		os.RemoveAll(filepath.Dir(binary))
	} // only this process's MkdirTemp
	os.Exit(code)
}

func executable(t testing.TB) string {
	t.Helper()
	if path := os.Getenv("RECALL_TEST_BINARY"); path != "" {
		return path
	}
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "recall-integration-bin-")
		if err != nil {
			buildError = err
			return
		}
		binary = filepath.Join(dir, "recall")
		cmd := exec.Command("go", "build", "-o", binary, "./cmd/recall")
		cmd.Dir = repository
		out, err := cmd.CombinedOutput()
		if err != nil {
			buildError = &model.Error{Code: "internal", Message: string(out)}
		}
	})
	if buildError != nil {
		t.Fatal(buildError)
	}
	return binary
}

func isolated(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("RECALL_HOME", filepath.Join(t.TempDir(), "collection"))
	t.Setenv("RECALL_ASSETS_DIR", "")
	t.Setenv("RECALL_EMBEDDING_GUARD", "")
	t.Setenv("RECALL_TEST_BINARY", "")
	return os.Getenv("RECALL_HOME")
}

// Metadata-only fixture: not pristine onboarding and never evidence of inference.
func prepared(t *testing.T) string {
	t.Helper()
	root := isolated(t)
	cfg, err := config.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	return root
}

func runBinary(t testing.TB, args ...string) (stdout string, exitCode int) {
	t.Helper()
	return runInput(t, "", args...)
}

func runInput(t testing.TB, input string, args ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable(t), args...)
	cmd.Stdin = strings.NewReader(input)
	var logs bytes.Buffer
	cmd.Stderr = &logs
	out, err := cmd.Output()
	if err == nil {
		return string(out), 0
	}
	if e, ok := err.(*exec.ExitError); ok {
		return string(out), e.ExitCode()
	}
	t.Fatalf("execute: %v stderr=%s", err, &logs)
	return "", -1
}

func result[T any](t testing.TB, out string, code int) T {
	t.Helper()
	var envelope struct {
		Data  T            `json:"data"`
		Error *model.Error `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil || code != 0 || envelope.Error != nil {
		t.Fatalf("exit=%d output=%s decode=%v", code, out, err)
	}
	return envelope.Data
}

func command[T any](t testing.TB, args ...string) T {
	t.Helper()
	out, code := runBinary(t, append([]string{"--json"}, args...)...)
	return result[T](t, out, code)
}

func create(t testing.TB, kind, name, body string) model.Document {
	t.Helper()
	out, code := runInput(t, body, "--json", "document", "create", "--type", kind, "--name", name)
	return result[model.Document](t, out, code)
}
