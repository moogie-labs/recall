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

func TestGuardianCrashStopsNativeBeforeReleasingOwnership(t *testing.T) {
	for _, stage := range []string{"startup", "serving"} {
		t.Run(stage, func(t *testing.T) {
			t.Setenv("HOME", shortHome(t))
			// Bounds cleanup of the pre-fix orphan without signalling a PID from
			// a file. The fixed manager must stop it well before this self-expiry.
			t.Setenv("RECALL_TEST_IGNORE_TERM", "1")
			if stage == "startup" {
				t.Setenv("RECALL_TEST_SLOW", "1")
			}
			cfg := testConfig(t)
			paths, err := socketPaths(cfg)
			if err != nil {
				t.Fatal(err)
			}
			opts := defaultWorkerOptions()
			opts.idle = 10 * time.Second
			opts.verify = func(context.Context, config.Config) error { return nil }
			guardCtx, killGuard := context.WithCancel(context.Background())
			defer killGuard()
			opts.command = func(cfg config.Config) *exec.Cmd {
				helper := helperCommand(cfg)
				cmd := exec.CommandContext(guardCtx, helper.Args[0], helper.Args[1:]...)
				cmd.Env = helper.Env
				return cmd
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- runWorker(ctx, cfg, opts) }()
			waitFor(t, func() bool { _, err := os.Stat(filepath.Join(cfg.RunDir, "native-pid")); return err == nil })
			nativePID := readPID(t, filepath.Join(cfg.RunDir, "native-pid")) // observation only
			if stage == "serving" {
				h := unixHTTP(paths.manager, 100*time.Millisecond)
				defer h.CloseIdleConnections()
				waitFor(t, func() bool { return checkHealth(context.Background(), h, cfg) == nil })
			}
			// CommandContext's synchronized watcher SIGKILLs this exact owned
			// guardian. No pidfile, process-name lookup or unrelated process is
			// signalled, and the test does not race reading Cmd.Process at Start.
			killGuard()
			select {
			case err := <-done:
				if err == nil {
					t.Error("guardian crash was not reported")
				}
			case <-time.After(6 * time.Second):
				t.Fatal("manager did not finish guardian-crash cleanup")
			}
			if !processGone(nativePID) {
				lock, err := acquireLock(context.Background(), paths.lifetime, false)
				if err == nil {
					lock.Close()
					t.Error("guardian crash left native alive with lifetime ownership available")
				} else {
					t.Error("guardian crash left native alive after manager returned")
				}
				waitFor(t, func() bool { return processGone(nativePID) })
			}
			// A new owner is admitted only after the old native has actually exited.
			t.Setenv("RECALL_TEST_SLOW", "")
			if _, err := testClient(cfg).Tokenize(context.Background(), "replacement"); err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(filepath.Join(cfg.RunDir, "native-starts"))
			if len(strings.Fields(string(b))) != 2 {
				t.Fatalf("unexpected native starts: %q", b)
			}
			stopFixtureWorker(t, cfg)
		})
	}
}

func TestGuardianCrashRetainsNativeLeaseUntilGroupCleanup(t *testing.T) {
	t.Setenv("HOME", shortHome(t))
	t.Setenv("RECALL_TEST_IGNORE_TERM", "1")
	cfg := testConfig(t)
	paths, _ := socketPaths(cfg)
	if err := privateSocketDir(filepath.Dir(paths.manager)); err != nil {
		t.Fatal(err)
	}
	lifetime, err := acquireLock(context.Background(), paths.lifetime, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lifetime.Close()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer write.Close()
	ctx, kill := context.WithCancel(context.Background())
	defer kill()
	helper := helperCommand(cfg)
	cmd := exec.CommandContext(ctx, helper.Args[0], helper.Args[1:]...)
	cmd.Env = append(helper.Env, guardianEnv+"=1")
	guard, err := startGuardian(cmd, read, write, lifetime)
	read.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer guard.stop()
	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(cfg.RunDir, "native-pid")); return err == nil })
	nativePID := readPID(t, filepath.Join(cfg.RunDir, "native-pid"))
	group, err := syscall.Getpgid(nativePID)
	if err != nil || group != cmd.Process.Pid {
		t.Fatal("native did not inherit private guardian group", group, err)
	}
	// Remove the manager's lock descriptor, then kill only its exact owned
	// guardian. Native itself must retain the lease during this crash window.
	lifetime.Close()
	kill()
	<-guard.done
	if processGone(nativePID) {
		t.Fatal("fixture native exited before cleanup authority was exercised")
	}
	probe, err := acquireLock(context.Background(), paths.lifetime, false)
	if err == nil {
		probe.Close()
		t.Error("native lost lifetime ownership when guardian crashed")
	}
	guard.stop() // Actual group cleanup, not just retaining a flock.
	waitFor(t, func() bool { return processGone(nativePID) })
	probe, err = acquireLock(context.Background(), paths.lifetime, false)
	if err != nil {
		t.Fatal("lease not released after native exit", err)
	}
	probe.Close()
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
