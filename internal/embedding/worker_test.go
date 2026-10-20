package embedding

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"recall/internal/config"
)

func TestEmbeddingProcess(t *testing.T) {
	mode := os.Getenv("RECALL_TEST_PROCESS")
	if mode == "" {
		return
	}
	var cfg config.Config
	if err := json.Unmarshal([]byte(os.Getenv("RECALL_TEST_CONFIG")), &cfg); err != nil {
		panic(err)
	}
	switch mode {
	case "client":
		_, err := testClient(cfg).Embed(context.Background(), "query", []string{"hello"})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "worker":
		opts := defaultWorkerOptions()
		opts.command = helperCommand
		opts.idle = 400 * time.Millisecond
		if os.Getenv("RECALL_TEST_REAL") == "1" {
			opts.idle = 2 * time.Second
		} else {
			opts.verify = func(context.Context, config.Config) error { return nil }
			opts.nativeCommand = func(cfg config.Config, socket string) *exec.Cmd {
				c := helperCommand(cfg)
				c.Env = append(c.Env, "RECALL_TEST_PROCESS=native", "RECALL_TEST_SOCKET="+socket)
				return c
			}
		}
		if os.Getenv(guardianEnv) == "" {
			os.WriteFile(filepath.Join(cfg.RunDir, "manager-pid"), []byte(strconv.Itoa(os.Getpid())), 0600)
		}
		if err := runWorker(context.Background(), cfg, opts); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	case "native":
		f, err := os.OpenFile(filepath.Join(cfg.RunDir, "native-starts"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			panic(err)
		}
		fmt.Fprintln(f, os.Getpid())
		f.Close()
		os.WriteFile(filepath.Join(cfg.RunDir, "native-pid"), []byte(strconv.Itoa(os.Getpid())), 0600)
		if os.Getenv("RECALL_TEST_IGNORE_TERM") == "1" {
			signal.Ignore(syscall.SIGTERM)
			time.AfterFunc(8*time.Second, func() { os.Exit(0) })
		}
		if os.Getenv("RECALL_TEST_NATIVE_EXIT") == "1" {
			os.Exit(17)
		}
		if os.Getenv("RECALL_TEST_SLOW") == "1" {
			select {}
		}
		l, err := net.Listen("unix", os.Getenv("RECALL_TEST_SOCKET"))
		if err != nil {
			panic(err)
		}
		http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/health":
				w.Write([]byte(`{"status":"ok"}`))
			case "/tokenize":
				if strings.Contains(os.Getenv("RECALL_TEST_BLOCK"), "1") {
					time.Sleep(time.Second)
				}
				w.Write([]byte(`{"tokens":[1,2,3]}`))
			case "/v1/embeddings":
				var b struct {
					Input []string `json:"input"`
				}
				json.NewDecoder(r.Body).Decode(&b)
				var data []any
				for i := range b.Input {
					v := make([]float32, 768)
					v[0] = 2
					data = append(data, map[string]any{"index": i, "embedding": v})
				}
				json.NewEncoder(w).Encode(map[string]any{"data": data})
			default:
				http.NotFound(w, r)
			}
		}))
	}
	os.Exit(0)
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}
func processGone(pid int) bool { return syscall.Kill(pid, 0) == syscall.ESRCH }
func stopFixtureWorker(t *testing.T, cfg config.Config) {
	t.Helper()
	pid := readPID(t, filepath.Join(cfg.RunDir, "manager-pid"))
	p, _ := os.FindProcess(pid)
	p.Signal(syscall.SIGTERM)
	paths, _ := socketPaths(cfg)
	waitFor(t, func() bool { _, err := os.Stat(paths.manager); return os.IsNotExist(err) })
	waitFor(t, func() bool {
		lock, err := acquireLock(context.Background(), paths.lifetime, false)
		if err != nil {
			return false
		}
		lock.Close()
		return true
	})
}

func TestNativeCrashStopsManagerAndRecovers(t *testing.T) {
	t.Setenv("HOME", shortHome(t))
	cfg := testConfig(t)
	c := testClient(cfg)
	if _, err := c.Tokenize(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	pid := readPID(t, filepath.Join(cfg.RunDir, "native-pid"))
	p, _ := os.FindProcess(pid)
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	paths, _ := socketPaths(cfg)
	waitFor(t, func() bool { _, err := os.Stat(paths.manager); return os.IsNotExist(err) })
	if _, err := c.Tokenize(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	stopFixtureWorker(t, cfg)
}

func TestNativeStartupExitIsExplicitAndDoesNotLeakPrompt(t *testing.T) {
	t.Setenv("HOME", shortHome(t))
	t.Setenv("RECALL_TEST_NATIVE_EXIT", "1")
	cfg := testConfig(t)
	_, err := testClient(cfg).Tokenize(context.Background(), "secret prompt")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	if !strings.Contains(err.Error(), "exit status") {
		t.Fatal("worker failure omitted process status:", err)
	}
	paths, _ := socketPaths(cfg)
	waitFor(t, func() bool {
		lock, err := acquireLock(context.Background(), paths.lifetime, false)
		if err != nil {
			return false
		}
		lock.Close()
		return true
	})
}

func TestStartupCancellationReapsNativeIgnoringTermination(t *testing.T) {
	t.Setenv("HOME", shortHome(t))
	t.Setenv("RECALL_TEST_SLOW", "1")
	t.Setenv("RECALL_TEST_IGNORE_TERM", "1")
	cfg := testConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := testClient(cfg).Tokenize(ctx, "a"); done <- err }()
	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(cfg.RunDir, "native-pid")); return err == nil })
	pid := readPID(t, filepath.Join(cfg.RunDir, "native-pid"))
	cancel()
	<-done
	if !processGone(pid) {
		t.Error("canceled startup returned before stubborn native was reaped")
		waitFor(t, func() bool { return processGone(pid) })
	}
}

func TestIdleExpiryStopsNativeAndRemovesSockets(t *testing.T) {
	t.Setenv("HOME", shortHome(t))
	cfg := testConfig(t)
	if _, err := testClient(cfg).Tokenize(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	pid := readPID(t, filepath.Join(cfg.RunDir, "native-pid"))
	paths, _ := socketPaths(cfg)
	waitFor(t, func() bool { _, err := os.Stat(paths.manager); return os.IsNotExist(err) })
	waitFor(t, func() bool { return processGone(pid) })
	waitFor(t, func() bool { _, err := os.Stat(paths.native); return os.IsNotExist(err) })
}

func TestInflightRequestPreventsIdleShutdown(t *testing.T) {
	t.Setenv("HOME", shortHome(t))
	t.Setenv("RECALL_TEST_BLOCK", "1")
	cfg := testConfig(t)
	if _, err := testClient(cfg).Tokenize(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	stopFixtureWorker(t, cfg)
}

func TestHealthChecksDoNotExtendIdleLifetime(t *testing.T) {
	t.Setenv("HOME", shortHome(t))
	cfg := testConfig(t)
	if _, err := testClient(cfg).Tokenize(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	paths, _ := socketPaths(cfg)
	h := unixHTTP(paths.manager, 100*time.Millisecond)
	defer h.CloseIdleConnections()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := checkHealth(context.Background(), h, cfg); err != nil {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("health checks prevented idle expiry")
}

func TestRequestCancellationDoesNotStopSharedWorker(t *testing.T) {
	t.Setenv("HOME", shortHome(t))
	t.Setenv("RECALL_TEST_BLOCK", "1")
	cfg := testConfig(t)
	c := testClient(cfg)
	if _, err := c.Tokenize(context.Background(), "warm"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.Tokenize(ctx, "cancel"); done <- err }()
	if _, err := testClient(cfg).Tokenize(context.Background(), "continue"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != context.DeadlineExceeded {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(cfg.RunDir, "native-starts"))
	if len(strings.Fields(string(b))) != 1 {
		t.Fatal("request cancellation restarted worker")
	}
	stopFixtureWorker(t, cfg)
}

func TestLifetimeLockPreventsSecondOwnerAndIgnoresStalePID(t *testing.T) {
	t.Setenv("HOME", shortHome(t))
	cfg := testConfig(t)
	paths, err := socketPaths(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := privateSocketDir(filepath.Dir(paths.manager)); err != nil {
		t.Fatal(err)
	}
	f, err := acquireLock(context.Background(), paths.lifetime, false)
	if err != nil {
		t.Fatal(err)
	}
	// Even a pidfile naming this test process must never be signalled.
	os.WriteFile(filepath.Join(cfg.RunDir, "worker.pid"), []byte(strconv.Itoa(os.Getpid())), 0600)
	opts := defaultWorkerOptions()
	opts.verify = func(context.Context, config.Config) error { t.Fatal("second owner reached native launch"); return nil }
	if err := runWorker(context.Background(), cfg, opts); err == nil {
		t.Fatal("second lifetime owner admitted")
	}
	unix.Flock(int(f.Fd()), unix.LOCK_UN)
	f.Close()
	if _, err := testClient(cfg).Tokenize(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	stopFixtureWorker(t, cfg)
}
