package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"recall/internal/config"
	"recall/internal/model"
)

// A short HOME is essential on Darwin: Go's normal t.TempDir prefix itself
// exceeds sockaddr_un once the production cache suffix is appended.
func shortHome(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/private/tmp", "re-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Resolve(filepath.Join(t.TempDir(), "collection"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ModelSignature = "test-model"
	cfg.ModelID = "test"
	cfg.RuntimeID = "test"
	cfg.ModelSHA256 = "test"
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func helperCommand(cfg config.Config) *exec.Cmd {
	b, _ := json.Marshal(cfg)
	c := exec.Command(os.Args[0], "-test.run=^TestEmbeddingProcess$")
	c.Env = append(os.Environ(), "RECALL_TEST_PROCESS=worker", "RECALL_TEST_CONFIG="+string(b))
	return c
}

func testClient(cfg config.Config) *client { return &client{cfg: cfg, command: helperCommand} }

func TestClientIsLazyAndCanceledCallDoesNotSpawn(t *testing.T) {
	home := shortHome(t)
	t.Setenv("HOME", home)
	cfg := testConfig(t)
	p := NewClient(cfg)
	if p == nil {
		t.Fatal("nil provider")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Tokenize(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".cache")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("constructor/canceled call created cache", err)
	}
}

func TestClientsShareWorkerAcrossProcessesAndRecoverCrash(t *testing.T) {
	t.Setenv("HOME", shortHome(t))
	cfg := testConfig(t)
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := helperCommand(cfg)
			c.Env = append(c.Env, "RECALL_TEST_PROCESS=client")
			if out, err := c.CombinedOutput(); err != nil {
				t.Errorf("client: %s %v", out, err)
			}
		}()
	}
	wg.Wait()
	log := filepath.Join(cfg.RunDir, "native-starts")
	if b, err := os.ReadFile(log); err != nil || len(strings.Fields(string(b))) != 1 {
		t.Fatalf("not singleton: %q %v", b, err)
	}
	pid := readPID(t, filepath.Join(cfg.RunDir, "manager-pid"))
	p, _ := os.FindProcess(pid)
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	// No pidfile is trusted for killing: this is the process just observed in
	// this isolated fixture. Guardian must reap the exact child after EOF.
	oldNative := readPID(t, filepath.Join(cfg.RunDir, "native-pid"))
	waitFor(t, func() bool { return processGone(oldNative) })
	if _, err := testClient(cfg).Tokenize(context.Background(), "recover"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); len(strings.Fields(string(b))) != 2 {
		t.Fatalf("restart count: %q", b)
	}
	stopFixtureWorker(t, cfg)
}

func TestLongRootUsesShortSocketPath(t *testing.T) {
	t.Setenv("HOME", shortHome(t))
	base := t.TempDir()
	root := filepath.Join(base, strings.Repeat("long", 40), strings.Repeat("root", 40))
	cfg, err := config.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ModelSignature = "test-model"
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := testClient(cfg).Tokenize(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	paths, err := socketPaths(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths.manager) >= 104 || strings.Contains(paths.manager, root) {
		t.Fatal(paths.manager)
	}
	for _, p := range []string{paths.manager, paths.native} {
		s, err := os.Stat(p)
		if err != nil || s.Mode().Perm() != 0600 {
			t.Fatal(p, s, err)
		}
	}
	s, err := os.Stat(filepath.Dir(paths.manager))
	if err != nil || s.Mode().Perm() != 0700 {
		t.Fatal(s, err)
	}
	stopFixtureWorker(t, cfg)
}

func TestSignatureMismatchDoesNotReplaceWorker(t *testing.T) {
	t.Setenv("HOME", shortHome(t))
	cfg := testConfig(t)
	if _, err := testClient(cfg).Tokenize(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	other := cfg
	other.ModelSignature = "different"
	if _, err := testClient(other).Tokenize(context.Background(), "a"); model.ErrorCode(err) != "asset_invalid" {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(cfg.RunDir, "native-starts"))
	if len(strings.Fields(string(b))) != 1 {
		t.Fatal("mismatch spawned a worker")
	}
	stopFixtureWorker(t, cfg)
}

func TestStartupCancellationStopsOwnedProcesses(t *testing.T) {
	t.Setenv("HOME", shortHome(t))
	t.Setenv("RECALL_TEST_SLOW", "1")
	cfg := testConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := testClient(cfg).Tokenize(ctx, "a"); done <- err }()
	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(cfg.RunDir, "native-pid")); return err == nil })
	pid := readPID(t, filepath.Join(cfg.RunDir, "native-pid"))
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return processGone(pid) })
}

func TestClientWaitsForCrashedManagersLifetimeOwner(t *testing.T) {
	t.Setenv("HOME", shortHome(t))
	cfg := testConfig(t)
	paths, err := socketPaths(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := privateSocketDir(filepath.Dir(paths.manager)); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireLock(context.Background(), paths.lifetime, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	done := make(chan error, 1)
	go func() { _, err := testClient(cfg).Tokenize(context.Background(), "restart"); done <- err }()
	// Models the guardian retaining ownership while reaping a crashed
	// manager's native child. A new manager must not be launched yet.
	time.Sleep(100 * time.Millisecond)
	unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	if err := <-done; err != nil {
		t.Fatal("restart raced with old lifetime owner:", err)
	}
	stopFixtureWorker(t, cfg)
}

func TestClientCancellationWhileWaitingForSpawnLock(t *testing.T) {
	t.Setenv("HOME", shortHome(t))
	cfg := testConfig(t)
	paths, _ := socketPaths(cfg)
	if err := privateSocketDir(filepath.Dir(paths.manager)); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireLock(context.Background(), paths.spawn, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := testClient(cfg).Tokenize(ctx, "a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.RunDir, "manager-pid")); !os.IsNotExist(err) {
		t.Fatal("lock waiter spawned manager", err)
	}
}

func TestClientRejectsInvalidWorkerTokenProtocol(t *testing.T) {
	for _, body := range []string{`{}`, `{"tokens":[-1]}`} {
		t.Run(body, func(t *testing.T) {
			t.Setenv("HOME", shortHome(t))
			cfg := testConfig(t)
			paths, _ := socketPaths(cfg)
			if err := privateSocketDir(filepath.Dir(paths.manager)); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("unix", paths.manager)
			if err != nil {
				t.Fatal(err)
			}
			s := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/health" {
					root, _ := filepath.EvalSymlinks(cfg.Root)
					json.NewEncoder(w).Encode(map[string]any{"version": 1, "root": root, "model_signature": "test-model"})
					return
				}
				w.Write([]byte(body))
			})}
			go s.Serve(listener)
			defer s.Close()
			if _, err := testClient(cfg).Tokenize(context.Background(), "private"); err == nil {
				t.Fatal("invalid worker tokens accepted")
			}
		})
	}
}

func TestClientBatchesWorkerRequests(t *testing.T) {
	t.Setenv("HOME", shortHome(t))
	cfg := testConfig(t)
	paths, _ := socketPaths(cfg)
	if err := privateSocketDir(filepath.Dir(paths.manager)); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", paths.manager)
	if err != nil {
		t.Fatal(err)
	}
	var batches []int
	s := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			root, _ := filepath.EvalSymlinks(cfg.Root)
			json.NewEncoder(w).Encode(map[string]any{"version": 1, "root": root, "model_signature": "test-model"})
			return
		}
		var in struct {
			Texts []string `json:"texts"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		batches = append(batches, len(in.Texts))
		vectors := make([][]float32, len(in.Texts))
		for i := range vectors {
			vectors[i] = make([]float32, 768)
			vectors[i][0] = 1
		}
		json.NewEncoder(w).Encode(map[string]any{"vectors": vectors})
	})}
	go s.Serve(l)
	defer s.Close()
	v, err := testClient(cfg).Embed(context.Background(), "query", []string{"a", "b", "c", "d", "e"})
	if err != nil || len(v) != 5 {
		t.Fatal(v, err)
	}
	if len(batches) != 2 || batches[0] != 4 || batches[1] != 1 {
		t.Fatalf("worker request batches: %v", batches)
	}
}

func TestCanonicalAliasesShareButDifferentRootsDoNot(t *testing.T) {
	t.Setenv("HOME", shortHome(t))
	first, second := testConfig(t), testConfig(t)
	if _, err := testClient(first).Tokenize(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(first.Root, alias); err != nil {
		t.Fatal(err)
	}
	other := first
	other.Root = alias
	if _, err := testClient(other).Tokenize(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(first.RunDir, "native-starts"))
	if len(strings.Fields(string(b))) != 1 {
		t.Fatal("alias started second worker")
	}
	if _, err := testClient(second).Tokenize(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	stopFixtureWorker(t, first)
	if _, err := testClient(second).Tokenize(context.Background(), "a"); err != nil {
		t.Fatal("stopping another root affected this worker:", err)
	}
	stopFixtureWorker(t, second)
}

func TestSocketPathRejectsLongHomeAndUnsafeCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir()+strings.Repeat("x", 110))
	cfg := testConfig(t)
	if _, err := socketPaths(cfg); model.ErrorCode(err) != "runtime_unavailable" {
		t.Fatal(err)
	}
	home := shortHome(t)
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".cache/recall"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(home, ".cache/recall/sockets")); err != nil {
		t.Fatal(err)
	}
	if _, err := testClient(cfg).Tokenize(context.Background(), "a"); err == nil {
		t.Fatal("symlink cache accepted")
	}
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for time.Now().Before(end) {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition timed out")
}
