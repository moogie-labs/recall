package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"recall/internal/app"
	"recall/internal/bootstrap"
	"recall/internal/config"
	"recall/internal/embedding"
	"recall/internal/model"
)

func prepared(t *testing.T) config.Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cfg, err := config.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Timezone = "America/New_York"
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// The app's real lazy client executes os.Executable. Dispatch that private
// entry in the test binary too, rather than recursively starting this suite.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "_embedding-worker" {
		os.Exit(Run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

func TestHelpAndRootDoNotOpenCollection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("RECALL_HOME", "relative-invalid-collection")
	r := Runner{open: func(context.Context, config.Config) (*app.App, error) {
		t.Fatal("opened app for help")
		return nil, nil
	}}
	missing := filepath.Join(t.TempDir(), "not-initialized")
	for _, args := range [][]string{nil, {}, {"--root", missing}, {"--help"}, {"document", "--help"}, {"--json", "search", "--help"}, {"mcp", "--help"}, {"--json"}, {"--json", "--root", missing}} {
		var out, logs bytes.Buffer
		input := strings.NewReader("leave stdin untouched")
		code := r.Run(t.Context(), args, input, &out, &logs)
		if code != 0 || logs.Len() != 0 || !strings.Contains(out.String(), "Usage:") || input.Len() != len("leave stdin untouched") {
			t.Fatalf("%v: %d %s %s", args, code, out.String(), logs.String())
		}
		if len(args) > 0 && args[0] == "--json" {
			result := decodeResult(t, &out)
			var help string
			if result.Error != nil || json.Unmarshal(result.Data, &help) != nil || !strings.Contains(help, "Usage:") {
				t.Fatalf("help is not a successful JSON result: %+v", result)
			}
		}
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("help initialized a collection")
	}
}

func TestMCPAdapterIsInjectable(t *testing.T) {
	cfg := prepared(t)
	r := Runner{MCP: func(ctx context.Context, got config.Config, in io.Reader, out, logs io.Writer) error {
		if got.Root != cfg.Root {
			t.Fatalf("root %s", got.Root)
		}
		_, err := io.WriteString(out, "adapter ran\n")
		return err
	}}
	var out, logs bytes.Buffer
	if code := r.Run(t.Context(), []string{"--root", cfg.Root, "mcp"}, strings.NewReader(""), &out, &logs); code != 0 || out.String() != "adapter ran\n" {
		t.Fatalf("%d %s %s", code, &out, &logs)
	}
}

func TestNilArgsDoNotReadTheHostProcessArguments(t *testing.T) {
	original := os.Args
	os.Args = []string{original[0], "host-process-argument"}
	t.Cleanup(func() { os.Args = original })
	t.Setenv("RECALL_HOME", "relative-invalid-collection")
	r := Runner{open: func(context.Context, config.Config) (*app.App, error) {
		t.Fatal("nil args opened collection")
		return nil, nil
	}}
	var out, logs bytes.Buffer
	if code := r.Run(t.Context(), nil, nil, &out, &logs); code != 0 || !strings.Contains(out.String(), "Usage:") || strings.Contains(out.String(), "host-process-argument") || logs.Len() != 0 {
		t.Fatalf("%d %s %s", code, &out, &logs)
	}
}

func TestPrivateEntryBypassesOrdinaryStartup(t *testing.T) {
	cfg := prepared(t)
	t.Setenv("RECALL_EMBEDDING_GUARD", "1")
	r := Runner{
		open: func(context.Context, config.Config) (*app.App, error) {
			t.Fatal("ordinary app startup")
			return nil, nil
		},
		worker: func(ctx context.Context, got config.Config) error {
			if got.Root != cfg.Root || os.Getenv("RECALL_EMBEDDING_GUARD") != "1" {
				t.Fatal("worker identity/environment changed")
			}
			return &model.Error{Code: "runtime_unavailable", Message: "worker fixture"}
		},
	}
	var out, logs bytes.Buffer
	if code := r.Run(t.Context(), []string{"_embedding-worker", "--root", cfg.Root}, nil, &out, &logs); code != 1 || out.Len() != 0 {
		t.Fatalf("%d %s %s", code, &out, &logs)
	}
}

type smokeProvider struct {
	err    error
	vector []float32
}

func (p smokeProvider) Embed(context.Context, string, []string) ([][]float32, error) {
	return [][]float32{p.vector}, p.err
}
func (smokeProvider) Tokenize(context.Context, string) ([]int, error) {
	return nil, errors.New("unexpected tokenizer call")
}

func TestInitRetriesAndRequiresValidInference(t *testing.T) {
	root := t.TempDir()
	bundle := t.TempDir()
	t.Setenv("RECALL_ASSETS_DIR", bundle)
	vector := make([]float32, embedding.Dimensions)
	vector[0] = 1
	p := smokeProvider{err: &model.Error{Code: "runtime_unavailable", Message: "smoke failed"}}
	r := Runner{
		initialize: func(ctx context.Context, cfg config.Config, assets string, progress func(int64, int64)) (bootstrap.Readiness, error) {
			if assets != bundle {
				t.Fatalf("assets %s", assets)
			}
			if err := config.Prepare(cfg); err != nil {
				return bootstrap.Readiness{}, err
			}
			progress(10, 10)
			return bootstrap.Readiness{MetadataReady: true, ModelReady: true, RuntimeReady: true}, nil
		},
		provider: func(config.Config) embedding.Provider { return p },
	}
	for i, want := range []int{1, 1, 0} {
		if i == 1 {
			p.err = nil
			p.vector = []float32{1}
		}
		if i == 2 {
			p.vector = vector
		}
		var out, logs bytes.Buffer
		code := r.Run(t.Context(), []string{"init", "--root", root, "--json"}, strings.NewReader(""), &out, &logs)
		if code != want {
			t.Fatalf("attempt %d: %d %s %s", i, code, &out, &logs)
		}
		result := decodeResult(t, &out)
		if i < 2 && result.Error.Code != "runtime_unavailable" {
			t.Fatalf("%+v", result)
		}
		if i == 2 && !bytes.Contains(result.Data, []byte(`"semantic_ready":true`)) {
			t.Fatalf("%s", result.Data)
		}
		if logs.Len() == 0 {
			t.Fatal("progress missing from stderr")
		}
	}
}

func TestAssetsAreExecutableRelativeOrAbsoluteOverride(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(t.TempDir(), "recall")
	if err := os.WriteFile(bin, nil, 0700); err != nil {
		t.Fatal(err)
	}
	bin, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	t.Setenv("RECALL_ASSETS_DIR", "")
	r := Runner{executable: func() (string, error) { return bin, nil }, initialize: func(_ context.Context, _ config.Config, assets string, _ func(int64, int64)) (bootstrap.Readiness, error) {
		if assets != filepath.Join(filepath.Dir(bin), "assets") {
			t.Fatalf("cwd assets: %s", assets)
		}
		return bootstrap.Readiness{}, &model.Error{Code: "asset_invalid", Message: "fixture"}
	}}
	var out, logs bytes.Buffer
	if code := r.Run(t.Context(), []string{"--root", root, "init", "--json"}, nil, &out, &logs); code != 1 {
		t.Fatal(code)
	}
	t.Setenv("RECALL_ASSETS_DIR", "relative")
	out.Reset()
	if code := r.Run(t.Context(), []string{"--root", root, "init", "--json"}, nil, &out, &logs); code != 2 {
		t.Fatalf("%d %s", code, &out)
	}
}

func TestInitFindsAssetsBesideSymlinkTarget(t *testing.T) {
	t.Setenv("RECALL_ASSETS_DIR", "")
	realBin := filepath.Join(t.TempDir(), "recall")
	if err := os.WriteFile(realBin, nil, 0700); err != nil {
		t.Fatal(err)
	}
	realBin, err := filepath.EvalSymlinks(realBin)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "recall")
	if err := os.Symlink(realBin, link); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(realBin), "assets")
	called := false
	r := Runner{executable: func() (string, error) { return link, nil }, initialize: func(_ context.Context, _ config.Config, assets string, _ func(int64, int64)) (bootstrap.Readiness, error) {
		called = true
		if assets != want {
			t.Fatalf("symlink CLI looked for assets at %s; want %s", assets, want)
		}
		return bootstrap.Readiness{}, &model.Error{Code: "asset_invalid", Message: "fixture"}
	}}
	var out, logs bytes.Buffer
	if code := r.Run(t.Context(), []string{"--root", t.TempDir(), "init"}, nil, &out, &logs); code != 1 || !called {
		t.Fatalf("%d called=%v %s", code, called, &logs)
	}
}

func TestCancellationAndBoundedInput(t *testing.T) {
	cfg := prepared(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out, logs bytes.Buffer
	if code := Run(ctx, []string{"--root", cfg.Root, "document", "create", "--type", "note", "--name", "canceled", "--json"}, strings.NewReader("body"), &out, &logs); code != 1 {
		t.Fatalf("%d %s", code, &out)
	}
	out.Reset()
	if code := Run(t.Context(), []string{"--root", cfg.Root, "document", "create", "--type", "note", "--name", "large", "--json"}, io.LimitReader(zeroReader{}, 16<<20+1), &out, &logs); code != 2 {
		t.Fatalf("%d %s", code, &out)
	}
	if _, err := os.Stat(filepath.Join(cfg.Content, "large.note.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("oversized input mutated source")
	}
}

type zeroReader struct{}

func (zeroReader) Read(b []byte) (int, error) { clear(b); return len(b), nil }

func buildExecutable(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "recall")
	command := exec.Command("go", "build", "-o", bin, "./cmd/recall")
	command.Dir = "../.."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	return bin
}

func shortHome(t *testing.T) string {
	t.Helper()
	// Darwin's 104-byte Unix-socket limit cannot fit the standard Go test HOME
	// plus Recall's prescribed cache path. Remove only this owned private fixture.
	home, err := os.MkdirTemp("/private/tmp", "rc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	return home
}

func TestExecutableGuardianPreservesInheritedDescriptors(t *testing.T) {
	bin := buildExecutable(t)
	cfg := prepared(t)
	t.Setenv("HOME", shortHome(t))
	// Ordinary startup would fail on this index. The guardian must not touch it.
	if err := os.WriteFile(cfg.Database, []byte("not a SQLite index"), 0600); err != nil {
		t.Fatal(err)
	}
	native := filepath.Join(cfg.RuntimeDir, "b11669", "llama-server")
	if err := os.MkdirAll(filepath.Dir(native), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(native, []byte("#!/bin/sh\nprintf alive >&4\nexec /bin/sleep 5\n"), 0700); err != nil {
		t.Fatal(err)
	}
	controlR, controlW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer controlR.Close()
	defer controlW.Close()
	lock, err := os.CreateTemp(t.TempDir(), "lease")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	nativeR, nativeW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer nativeR.Close()
	defer nativeW.Close()
	guardR, guardW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer guardR.Close()
	defer guardW.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, bin, "_embedding-worker", "--root", cfg.Root)
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
	command.Env = append(os.Environ(), "RECALL_EMBEDDING_GUARD=1")
	command.ExtraFiles = []*os.File{controlR, lock, nativeW, guardW}
	var out, logs bytes.Buffer
	command.Stdout, command.Stderr = &out, &logs
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	controlR.Close()
	nativeW.Close()
	guardW.Close()
	data := make([]byte, 5)
	_, readErr := io.ReadFull(contextReader{ctx, nativeR}, data)
	controlW.Close() // EOF must stop and reap the exact native child.
	err = command.Wait()
	if err != nil || readErr != nil || string(data) != "alive" || out.Len() != 0 {
		t.Fatalf("guardian: %v read=%v data=%q out=%s logs=%s", err, readErr, data, &out, &logs)
	}
	if tail, err := io.ReadAll(contextReader{ctx, nativeR}); err != nil || len(tail) != 0 {
		t.Fatalf("native lifetime retained: %q %v", tail, err)
	}
	if tail, err := io.ReadAll(contextReader{ctx, guardR}); err != nil || len(tail) != 0 {
		t.Fatalf("guardian lifetime retained: %q %v", tail, err)
	}
	if data, err := os.ReadFile(cfg.Database); err != nil || string(data) != "not a SQLite index" {
		t.Fatal("private entry touched the index")
	}
}

func TestIdleStdinPipeCanBeCanceled(t *testing.T) {
	cfg := prepared(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	defer cancel()
	var out, logs bytes.Buffer
	code := Run(ctx, []string{"--root", cfg.Root, "--json", "document", "create", "--type", "note", "--name", "idle"}, r, &out, &logs)
	got := decodeResult(t, &out)
	if code != 1 || got.Error == nil || got.Error.Message != "operation timed out" {
		t.Fatalf("%d %+v", code, got)
	}
	if _, err := os.Stat(filepath.Join(cfg.Content, "idle.note.md")); !os.IsNotExist(err) {
		t.Fatal("canceled input mutated source")
	}
}
