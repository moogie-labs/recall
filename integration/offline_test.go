package integration

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"recall/internal/model"
)

const networkDenied = `(version 1)(allow default)(deny network-outbound (remote ip "*:*"))`

// No manager is preowned: init and searches must start the packaged executable's
// production private dispatch, inheriting the sandbox through guardian/native.
func TestRealPackagedOnboardingAndOffline(t *testing.T) {
	if os.Getenv("RECALL_REAL_MODEL_TEST") != "1" {
		t.Skip("opt in: real assets, one cold download, production idle waits")
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Fatal("requires Darwin/arm64")
	}
	archive, err := filepath.Abs("../build/recall-darwin-arm64.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "../scripts/verify-package.sh", archive).CombinedOutput(); err != nil {
		t.Fatalf("package: %v %s", err, out)
	}
	dest := t.TempDir()
	if out, err := exec.Command("tar", "-xzf", archive, "-C", dest).CombinedOutput(); err != nil {
		t.Fatalf("extract: %v %s", err, out)
	}
	bin := filepath.Join(dest, "recall-darwin-arm64", "bin", "recall")
	// Darwin sockaddr_un is 104 bytes. The harness temp base plus cache suffix
	// exceeds that, so only this owned HOME uses the short private temp location.
	home, err := os.MkdirTemp("/private/tmp", "recall14-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "collection")
	t.Setenv("HOME", home)
	t.Setenv("RECALL_HOME", root)
	t.Setenv("RECALL_ASSETS_DIR", "")
	t.Setenv("RECALL_EMBEDDING_GUARD", "")
	t.Setenv("RECALL_TEST_BINARY", bin)
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(root)))[:16]
	base := filepath.Join(home, ".cache", "recall", "sockets", key)
	t.Cleanup(func() {
		// No guessed PID is signalled. Respect the real 120 s policy, then
		// require the lifetime lease free before deleting only this fixture.
		if waitIdle(t, base, root) {
			os.RemoveAll(home)
		} else {
			t.Errorf("owned fixture retained for diagnosis: %s", home)
		}
	})
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("cold root already exists")
	}
	seed := os.Getenv("RECALL_REUSE_MODEL")
	if seed != "" {
		var manifest struct {
			Filename, SHA256 string
			Size             int64
		}
		raw, err := os.ReadFile("../assets/model.lock.json")
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(seed)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		hash := sha256.New()
		n, err := io.Copy(hash, f)
		if err != nil || n != manifest.Size || fmt.Sprintf("%x", hash.Sum(nil)) != manifest.SHA256 {
			t.Fatal("reuse model is not pinned verified bytes")
		}
		if _, err := f.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "models"), 0700); err != nil {
			t.Fatal(err)
		}
		dst, err := os.OpenFile(filepath.Join(root, "models", manifest.Filename), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(dst, f)
		closeErr := dst.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("seed copy %v %v", err, closeErr)
		}
	}
	start := time.Now()
	cmd := exec.Command(bin, "--root", root, "init", "--json")
	var progress bytes.Buffer
	cmd.Stderr = &progress
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("cold init: %v %s %s", err, out, &progress)
	}
	if !bytes.Contains(out, []byte(`"semantic_ready":true`)) || seed == "" && !strings.Contains(progress.String(), "Downloading model:") {
		t.Fatalf("not cold downloaded inference: %s %s", out, &progress)
	}
	if seed == "" {
		t.Logf("pristine packaged init incl one model download + automatic lazy manager: %s", time.Since(start))
	} else {
		t.Logf("fresh packaged config/runtime + automatic lazy manager with verified seeded model (NOT pristine download): %s", time.Since(start))
	}
	initial, err := os.Stat(base + ".sock")
	if err != nil {
		t.Fatal(err)
	}
	account := create(t, "note", "account", "To recover forgotten credentials, open account settings and reset the password.")
	create(t, "note", "garden", "Tomatoes need sunlight, soil and regular watering.")
	create(t, "task", "offline task", "Inspect login recovery workflow.")
	start = time.Now()
	command[model.SyncReport](t, "sync", "--wait")
	t.Logf("initial indexing (three documents): %s", time.Since(start))
	start = time.Now()
	h := command[[]model.Hit](t, "search", "forgot my password", "--mode", "semantic")
	t.Logf("warm CLI semantic query: %s", time.Since(start))
	if len(h) == 0 || h[0].ID != account.ID {
		t.Fatalf("real related query ranking: %+v", h)
	}
	reused, err := os.Stat(base + ".sock")
	if err != nil || !os.SameFile(initial, reused) {
		t.Fatal("worker endpoint replaced, not reused")
	}
	deleteDuringRealIndexing(t, root)
	if !waitIdle(t, base, root) {
		t.Fatal("initial worker failed to idle")
	}
	// Separate cancellation gate: own the exact manager handle. This is not
	// the fresh/lazy-launch fixture and never signals a PID read from disk.
	manager := exec.Command(bin, "_embedding-worker", "--root", root)
	var workerLogs bytes.Buffer
	manager.Stderr = &workerLogs
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if manager.ProcessState == nil {
			manager.Process.Signal(os.Interrupt)
			manager.Wait()
		}
	})
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(base + ".sock"); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(base + ".sock"); err != nil {
		t.Fatal("owned cancellation manager did not become ready")
	}
	start = time.Now()
	if err := manager.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := manager.Wait(); err == nil {
		t.Fatal("cancelled manager unexpectedly reported ordinary success")
	}
	if !waitIdle(t, base, root) {
		t.Fatal("cancelled manager retained native child/ownership")
	}
	// This manager received no document/query request. Its captured stderr is
	// lifecycle diagnostics only, not evidence about active-workload privacy.
	t.Logf("owned real-manager cancellation reaped native child/removed sockets/released lifetime lease: %s (lifecycle only; no active request)", time.Since(start))
	// A real TCP control succeeds outside the restriction. Inside it both
	// loopback and external connections must fail with EPERM/EACCES, not DNS.
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	control, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	control.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	probe := fmt.Sprintf(`import socket,errno; a=socket.socket().connect_ex(("127.0.0.1",%d)); b=socket.socket().connect_ex(("1.1.1.1",443)); print(a,b); assert a in (errno.EPERM,errno.EACCES) and b in (errno.EPERM,errno.EACCES)`, port)
	probeOut, err := exec.Command("/usr/bin/sandbox-exec", "-p", networkDenied, "python3", "-c", probe).CombinedOutput()
	if err != nil {
		t.Fatalf("outbound denial unproven: %v %s", err, probeOut)
	}
	t.Logf("sandbox TCP denial loopback/external errno: %s", probeOut)
	denied := func(args ...string) *exec.Cmd {
		return exec.Command("/usr/bin/sandbox-exec", append([]string{"-p", networkDenied, bin}, args...)...)
	}
	query := func(mode string) []model.Hit {
		text := "forgot my password"
		if mode == "keyword" {
			text = "password"
		}
		c := denied("--root", root, "--json", "search", text, "--mode", mode)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("offline search: %v %s", err, out)
		}
		return result[[]model.Hit](t, string(out), 0)
	}
	if _, err := os.Stat(base + ".sock"); !os.IsNotExist(err) {
		t.Fatal("unsandboxed manager still available")
	}
	start = time.Now()
	h = query("semantic")
	t.Logf("cold model-load CLI semantic query, network denied: %s", time.Since(start))
	if len(h) == 0 || h[0].ID != account.ID {
		t.Fatal("offline related retrieval failed")
	}
	for _, mode := range []string{"semantic", "hybrid", "keyword"} {
		start = time.Now()
		h = query(mode)
		if len(h) == 0 || h[0].ID != account.ID {
			t.Fatalf("offline %s rank", mode)
		}
		t.Logf("warm network-denied CLI %s: %s", mode, time.Since(start))
	}
	// Full native chain is now descended from sandbox-exec, not a previously
	// running unsandboxed worker. MCP and CLI CRUD/search share that worker.
	s := connectMCP(t, denied("--root", root, "mcp"))
	d := tool[model.Document](t, s, "document_create", map[string]any{"kind": "task", "name": "denied", "body": "offline recovery task"})
	d = tool[model.Document](t, s, "task_set_state", map[string]any{"id": d.ID, "state": "completed"})
	d = tool[model.Document](t, s, "document_update", map[string]any{"id": d.ID, "if_revision": d.Revision, "raw": strings.Replace(d.Raw, "offline recovery task", "edited recovery task", 1)})
	d = tool[model.Document](t, s, "document_move", map[string]any{"id": d.ID, "to": "moved.task.md"})
	got := tool[model.Document](t, s, "document_get", map[string]any{"id": d.ID})
	if got.Path != d.Path || got.State != model.Completed || len(got.History) != 2 {
		t.Fatal("offline CRUD/history failed")
	}
	for _, mode := range []string{"semantic", "hybrid", "keyword"} {
		text := "forgot my password"
		if mode == "keyword" {
			text = "password"
		}
		r := tool[struct {
			Hits []model.Hit `json:"hits"`
		}](t, s, "search", map[string]any{"text": text, "mode": mode})
		if len(r.Hits) == 0 || r.Hits[0].ID != account.ID {
			t.Fatalf("offline MCP %s ranking", mode)
		}
	}
	tool[any](t, s, "document_delete", map[string]any{"id": d.ID})
	// CLI source CRUD is restricted too, not just the MCP adapter.
	for _, args := range [][]string{{"document", "create", "--type", "note", "--name", "denied-cli"}, {"document", "get", "--path", "denied-cli.note.md"}, {"document", "move", "--path", "denied-cli.note.md", "--to", "renamed.note.md"}, {"document", "delete", "--path", "renamed.note.md"}} {
		c := denied(append([]string{"--root", root, "--json"}, args...)...)
		c.Stdin = strings.NewReader("offline CLI body")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("offline CLI CRUD: %v %s", err, out)
		}
	}
	t.Log("PASS network-denied packaged CLI/MCP CRUD and semantic/hybrid/keyword; sandbox inherited by lazily launched manager/guardian/native")
}

func waitIdle(t *testing.T, base, root string) bool {
	t.Helper()
	start := time.Now()
	var peakRSS int
	for time.Since(start) < 135*time.Second {
		// Sample the full fixture chain only; this is sampled RSS, not a
		// kernel high-water mark or a full-app CPU profile.
		out, _ := exec.Command("ps", "-axo", "rss=,command=").Output()
		sum := 0
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, root) && (strings.Contains(line, "_embedding-worker") || strings.Contains(line, "llama-server")) {
				var rss int
				fmt.Sscan(line, &rss)
				sum += rss
			}
		}
		if sum > peakRSS {
			peakRSS = sum
		}
		_, a := os.Stat(base + ".sock")
		_, b := os.Stat(base + ".native.sock")
		if os.IsNotExist(a) && os.IsNotExist(b) {
			f, err := os.OpenFile(base+".worker.lock", os.O_RDWR, 0)
			if os.IsNotExist(err) {
				return true
			}
			if err == nil {
				err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
				if err == nil {
					unix.Flock(int(f.Fd()), unix.LOCK_UN)
				}
				f.Close()
			}
			if err == nil && sum == 0 {
				t.Logf("actual idle shutdown observed after %s; sampled aggregate chain RSS max %d KiB (1 s snapshots, not true peak)", time.Since(start), peakRSS)
				return true
			}
		}
		time.Sleep(time.Second)
	}
	return false
}
