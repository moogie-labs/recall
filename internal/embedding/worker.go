package embedding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"recall/assets"
	"recall/internal/bootstrap"
	"recall/internal/config"
	"recall/internal/model"
)

const guardianEnv = "RECALL_EMBEDDING_GUARD"

type workerOptions struct {
	idle, startup time.Duration
	command       func(config.Config) *exec.Cmd
	nativeCommand func(config.Config, string) *exec.Cmd
	verify        func(context.Context, config.Config) error
}

func defaultWorkerOptions() workerOptions {
	return workerOptions{idle: 120 * time.Second, startup: 120 * time.Second, command: workerCommand, verify: verifyAssets,
		nativeCommand: func(cfg config.Config, socket string) *exec.Cmd {
			c := exec.Command(filepath.Join(cfg.RuntimeDir, "b11669", "llama-server"), nativeArgs(cfg, socket)...)
			// Environment-based llama options must not enable tools, networking,
			// prompt logging or override the explicit, pinned launch configuration.
			for _, v := range os.Environ() {
				if !strings.HasPrefix(v, "LLAMA_") && !strings.HasPrefix(v, "RECALL_EMBEDDING_") {
					c.Env = append(c.Env, v)
				}
			}
			return c
		}}
}

// RunWorker is the hidden executable entry. The same entry also hosts a small
// guardian, connected only by inherited anonymous descriptors. On macOS there
// is no parent-death signal: the guardian observes manager pipe EOF (including
// SIGKILL), terminates and reaps its native child, then releases the inherited
// lifetime lock. No pidfile or unrelated process is ever signalled.
func RunWorker(ctx context.Context, cfg config.Config) error {
	return runWorker(ctx, cfg, defaultWorkerOptions())
}

func runWorker(ctx context.Context, cfg config.Config, opts workerOptions) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if os.Getenv(guardianEnv) == "1" {
		return runGuardian(ctx, cfg, opts)
	}
	p, err := socketPaths(cfg)
	if err != nil {
		return err
	}
	if err := privateSocketDir(filepath.Dir(p.manager)); err != nil {
		return err
	}
	lifetime, err := acquireLock(ctx, p.lifetime, false)
	if err != nil {
		return err
	}
	defer lifetime.Close()
	if err := opts.verify(ctx, cfg); err != nil {
		return err
	}
	// Only the lifetime owner can remove stale endpoints. Lock files persist:
	// unlinking a flock inode would admit independent competing owners.
	for _, path := range []string{p.manager, p.native} {
		if err := removeSocket(path); err != nil {
			return err
		}
	}
	defer os.Remove(p.manager)
	defer os.Remove(p.native)
	read, write, err := os.Pipe()
	if err != nil {
		return unavailable("cannot create native ownership pipe")
	}
	guard := opts.command(cfg)
	guard.Env = append(commandEnv(guard), guardianEnv+"=1")
	guard.ExtraFiles = []*os.File{read, lifetime}
	child, err := startOwned(guard)
	read.Close()
	if err != nil {
		write.Close()
		return unavailable("cannot start native guardian")
	}
	// Outer shutdown deadlines must exceed the guardian's native-child grace
	// period, otherwise killing the guardian could orphan a stubborn native.
	defer func() { write.Close(); child.waitOrStop(5 * time.Second) }()
	n := newNative(p.native)
	defer n.http.CloseIdleConnections()
	startup, stopStartup := context.WithTimeout(ctx, opts.startup)
	defer stopStartup()
	for {
		select {
		case <-child.done:
			return child.failure("native guardian exited during startup")
		default:
		}
		if nativeReady(startup, n.http) {
			break
		}
		if err := pause(startup, 20*time.Millisecond); err != nil {
			return err
		}
	}
	if err := os.Chmod(p.native, 0600); err != nil {
		return unavailable("cannot secure native socket")
	}
	listener, err := net.Listen("unix", p.manager)
	if err != nil {
		return unavailable("cannot listen on worker socket")
	}
	defer listener.Close()
	if err := os.Chmod(p.manager, 0600); err != nil {
		return unavailable("cannot secure worker socket")
	}
	root, err := filepath.EvalSymlinks(cfg.Root)
	if err != nil {
		return unavailable("collection root is unavailable")
	}
	var mu sync.Mutex
	active := 0
	last := time.Now()
	closing := false
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, health{1, root, cfg.ModelSignature}) })
	serve := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if closing {
			mu.Unlock()
			http.Error(w, "worker stopping", 503)
			return
		}
		active++
		mu.Unlock()
		defer func() { mu.Lock(); active--; last = time.Now(); mu.Unlock() }()
		var req workerRequest
		if err := decodeJSON(http.MaxBytesReader(w, r.Body, 1<<20), &req); err != nil {
			writeJSON(w, workerResponse{Code: "invalid_argument"})
			return
		}
		if req.Signature != cfg.ModelSignature {
			writeJSON(w, workerResponse{Code: "asset_invalid"})
			return
		}
		request, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		var out workerResponse
		var err error
		switch r.URL.Path {
		case "/embed":
			out.Vectors, err = n.Embed(request, req.Purpose, req.Texts)
		case "/tokenize":
			out.Tokens, err = n.Tokenize(request, req.Text)
		}
		if err != nil {
			out = workerResponse{Code: model.ErrorCode(err)}
		}
		writeJSON(w, out)
	}
	mux.HandleFunc("POST /embed", serve)
	mux.HandleFunc("POST /tokenize", serve)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 65 * time.Second, IdleTimeout: 5 * time.Second}
	defer server.Close()
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	ticker := time.NewTicker(min(opts.idle/4, time.Second))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-child.done:
			return child.failure("native guardian exited")
		case err := <-served:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return unavailable("embedding worker listener failed")
		case <-ticker.C:
			mu.Lock()
			expired := active == 0 && time.Since(last) >= opts.idle
			if expired {
				closing = true
			}
			mu.Unlock()
			if expired {
				return nil
			}
		}
	}
}

type workerRequest struct {
	Signature string   `json:"model_signature"`
	Purpose   string   `json:"purpose,omitempty"`
	Texts     []string `json:"texts,omitempty"`
	Text      string   `json:"text,omitempty"`
}
type workerResponse struct {
	Tokens  []int       `json:"tokens,omitempty"`
	Vectors [][]float32 `json:"vectors,omitempty"`
	Code    string      `json:"code,omitempty"`
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func runGuardian(ctx context.Context, cfg config.Config, opts workerOptions) error {
	pipe := os.NewFile(3, "manager-lifetime")
	lock := os.NewFile(4, "worker-ownership")
	if pipe == nil || lock == nil {
		return unavailable("native guardian requires inherited ownership")
	}
	defer pipe.Close()
	defer lock.Close()
	unix.CloseOnExec(3)
	unix.CloseOnExec(4)
	if s, err := pipe.Stat(); err != nil || s.Mode()&os.ModeNamedPipe == 0 {
		return unavailable("invalid native guardian pipe")
	}
	if s, err := lock.Stat(); err != nil || !s.Mode().IsRegular() {
		return unavailable("invalid native guardian lock")
	}
	p, err := socketPaths(cfg)
	if err != nil {
		return err
	}
	child, err := startOwned(opts.nativeCommand(cfg, p.native))
	if err != nil {
		return unavailable("cannot launch native runtime")
	}
	defer child.stop(2 * time.Second)
	closed := make(chan struct{})
	go func() { io.Copy(io.Discard, pipe); close(closed) }()
	select {
	case <-closed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-child.done:
		return child.failure("native runtime exited")
	}
}

func commandEnv(cmd *exec.Cmd) []string {
	if cmd.Env != nil {
		return cmd.Env
	}
	return os.Environ()
}

type ownedProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func startOwned(cmd *exec.Cmd) (*ownedProcess, error) {
	// nil standard streams are /dev/null; no request or native diagnostics leak.
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &ownedProcess{cmd: cmd, done: make(chan struct{})}
	go func() { cmd.Wait(); close(p.done) }()
	return p, nil
}
func (p *ownedProcess) stop(grace time.Duration) {
	select {
	case <-p.done:
		return
	default:
	}
	p.cmd.Process.Signal(syscall.SIGTERM)
	p.waitOrStop(grace)
}
func (p *ownedProcess) waitOrStop(grace time.Duration) {
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-p.done:
		return
	case <-t.C:
		p.cmd.Process.Kill()
		<-p.done
	}
}

// Called only after done closes; ProcessState is then synchronized with Wait.
func (p *ownedProcess) failure(message string) error {
	return unavailable(fmt.Sprintf("%s (%s)", message, p.cmd.ProcessState.String()))
}

func nativeReady(ctx context.Context, c *http.Client) bool {
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/health", nil)
	res, err := c.Do(r)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	var h struct {
		Status string `json:"status"`
	}
	return res.StatusCode == 200 && decodeJSON(res.Body, &h) == nil && h.Status == "ok"
}

func removeSocket(path string) error {
	s, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || s.Mode()&os.ModeSocket == 0 {
		return unavailable("worker endpoint is not a socket")
	}
	if err := os.Remove(path); err != nil {
		return unavailable("cannot remove stale worker socket")
	}
	return nil
}

// Bootstrap already verifies the complete bundled runtime tree. At execution
// require its selected executable and independently rehash the pinned weights;
// missing assets never trigger a download or initialization from this package.
func verifyAssets(ctx context.Context, cfg config.Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	saved, err := config.Resolve(cfg.Root)
	if err != nil {
		return err
	}
	if cfg.ModelsDir != saved.ModelsDir || cfg.RuntimeDir != saved.RuntimeDir {
		return assetInvalid("embedding asset paths must match resolved collection layout")
	}
	var m, rt bootstrap.Manifest
	if json.Unmarshal([]byte(assets.ModelLock()), &m) != nil || json.Unmarshal([]byte(assets.RuntimeLock()), &rt) != nil {
		return assetInvalid("invalid embedded asset identities")
	}
	if cfg.ModelID != m.ID || cfg.ModelSHA256 != m.SHA256 || cfg.ModelSignature != "sha256:"+m.SHA256 || cfg.RuntimeID != rt.ID || saved.ModelSignature != cfg.ModelSignature || saved.ModelID != cfg.ModelID || saved.ModelSHA256 != cfg.ModelSHA256 || saved.RuntimeID != cfg.RuntimeID {
		return assetInvalid("configured embedding assets differ from pinned identities")
	}
	r, err := os.OpenRoot(cfg.Root)
	if err != nil {
		return unavailable("collection is not initialized")
	}
	defer r.Close()
	for _, dir := range []string{"models", "runtime", "runtime/" + rt.ID} {
		s, err := r.Lstat(dir)
		if err != nil || !s.IsDir() || s.Mode()&os.ModeSymlink != 0 {
			return assetInvalid("embedding asset directory is unavailable")
		}
	}
	f, err := r.OpenFile("models/"+m.Filename, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return unavailable("verified model is unavailable")
	}
	defer f.Close()
	s, err := f.Stat()
	if err != nil || !s.Mode().IsRegular() || s.Size() != m.Size {
		return assetInvalid("model size differs from pinned identity")
	}
	hash := sha256.New()
	buf := make([]byte, 128<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := f.Read(buf)
		hash.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return unavailable("cannot verify model")
		}
	}
	if hex.EncodeToString(hash.Sum(nil)) != m.SHA256 {
		return assetInvalid("model checksum differs from pinned identity")
	}
	s, err = r.Lstat("runtime/" + rt.ID + "/llama-server")
	if err != nil || !s.Mode().IsRegular() || s.Mode().Perm()&0111 == 0 {
		return unavailable("verified native executable is unavailable")
	}
	return nil
}
