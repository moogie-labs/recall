package embedding

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"recall/internal/config"
	"recall/internal/model"
)

type client struct {
	cfg     config.Config
	command func(config.Config) *exec.Cmd
}

// NewClient performs no filesystem I/O and starts no process. The executable
// must dispatch `_embedding-worker --root <root>` to RunWorker (Task 11).
func NewClient(cfg config.Config) Provider { return &client{cfg: cfg, command: workerCommand} }

func workerCommand(cfg config.Config) *exec.Cmd {
	exe, err := os.Executable()
	if err != nil {
		return exec.Command("")
	}
	return exec.Command(exe, "_embedding-worker", "--root", cfg.Root)
}

type socketNames struct{ manager, native, spawn, lifetime string }

var errLockBusy = unavailable("worker already has a lifetime owner")

func socketPaths(cfg config.Config) (socketNames, error) {
	if !filepath.IsAbs(cfg.Root) {
		return socketNames{}, invalid("embedding requires an absolute collection root")
	}
	root, err := filepath.EvalSymlinks(cfg.Root)
	if err != nil {
		return socketNames{}, unavailable("collection root is unavailable")
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return socketNames{}, unavailable("embedding requires an absolute HOME")
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(root)))[:16]
	base := filepath.Join(home, ".cache", "recall", "sockets", key)
	p := socketNames{base + ".sock", base + ".native.sock", base + ".spawn.lock", base + ".worker.lock"}
	// Darwin sockaddr_un.sun_path is 104 bytes, including its trailing NUL.
	if len(p.manager) >= 104 || len(p.native) >= 104 {
		return socketNames{}, unavailable("HOME is too long for private Unix inference sockets")
	}
	return p, nil
}

func privateSocketDir(dir string) error {
	// Never follow a cache-component symlink or reuse another user's directory.
	for _, p := range []string{filepath.Dir(filepath.Dir(dir)), filepath.Dir(dir), dir} {
		if err := os.Mkdir(p, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return unavailable("cannot create private socket directory")
		}
		s, err := os.Lstat(p)
		if err != nil || !s.IsDir() || s.Mode()&os.ModeSymlink != 0 {
			return unavailable("socket cache must contain real directories")
		}
		st, ok := s.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != uint32(os.Getuid()) {
			return unavailable("socket cache has a different owner")
		}
		if p != filepath.Dir(filepath.Dir(dir)) {
			if err := os.Chmod(p, 0700); err != nil {
				return unavailable("cannot secure socket cache")
			}
		}
	}
	return nil
}

func acquireLock(ctx context.Context, path string, wait bool) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, unavailable("cannot open worker ownership lock")
	}
	s, err := f.Stat()
	if err != nil || !s.Mode().IsRegular() {
		f.Close()
		return nil, unavailable("invalid worker ownership lock")
	}
	if st, ok := s.Sys().(*syscall.Stat_t); !ok || st.Uid != uint32(os.Getuid()) {
		f.Close()
		return nil, unavailable("worker lock has different owner")
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return nil, unavailable("cannot secure worker lock")
	}
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			f.Close()
			return nil, unavailable("cannot acquire worker ownership lock")
		}
		if !wait {
			f.Close()
			return nil, errLockBusy
		}
		if err := pause(ctx, 10*time.Millisecond); err != nil {
			f.Close()
			return nil, err
		}
	}
}

type health struct {
	Version   int    `json:"version"`
	Root      string `json:"root"`
	Signature string `json:"model_signature"`
}

func checkHealth(ctx context.Context, h *http.Client, cfg config.Config) error {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/health", nil)
	if err != nil {
		return unavailable("cannot check worker health")
	}
	res, err := h.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return unavailable("embedding worker is unavailable")
	}
	defer res.Body.Close()
	var got health
	if res.StatusCode != 200 || decodeJSON(res.Body, &got) != nil {
		return unavailable("invalid worker health response")
	}
	root, err := filepath.EvalSymlinks(cfg.Root)
	if err != nil {
		return unavailable("collection root is unavailable")
	}
	if got.Version != 1 || got.Root != root || got.Signature != cfg.ModelSignature || got.Signature == "" {
		return assetInvalid("embedding worker identity does not match collection")
	}
	return nil
}

func (c *client) connect(ctx context.Context) (*http.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := socketPaths(c.cfg)
	if err != nil {
		return nil, err
	}
	if err := privateSocketDir(filepath.Dir(p.manager)); err != nil {
		return nil, err
	}
	h := unixHTTP(p.manager, 250*time.Millisecond)
	defer h.CloseIdleConnections()
	if err := checkHealth(ctx, h, c.cfg); err == nil {
		return unixHTTP(p.manager, 60*time.Second), nil
	} else if isIdentity(err) {
		return nil, err
	}
	startup, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	lock, err := acquireLock(startup, p.spawn, true)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	for {
		if err := checkHealth(startup, h, c.cfg); err == nil {
			return unixHTTP(p.manager, 60*time.Second), nil
		} else if isIdentity(err) {
			return nil, err
		}
		// A dying manager's guardian retains this lock until the old native
		// child has been reaped. Do not race it with a replacement manager.
		probe, err := acquireLock(startup, p.lifetime, false)
		if err == nil {
			probe.Close()
			break
		}
		if !errors.Is(err, errLockBusy) {
			return nil, err
		}
		if err := pause(startup, 20*time.Millisecond); err != nil {
			return nil, err
		}
	}
	cmd := c.command(c.cfg)
	process, err := startOwned(cmd)
	if err != nil {
		return nil, unavailable("cannot start embedding worker")
	}
	for {
		select {
		case <-process.done:
			return nil, process.failure("embedding worker exited during startup")
		default:
		}
		if err := checkHealth(startup, h, c.cfg); err == nil {
			return unixHTTP(p.manager, 60*time.Second), nil
		} else if isIdentity(err) {
			process.stop(8 * time.Second)
			return nil, err
		}
		if err := pause(startup, 20*time.Millisecond); err != nil {
			process.stop(8 * time.Second)
			return nil, err
		}
	}
}

func isIdentity(err error) bool { return model.ErrorCode(err) == "asset_invalid" }

func (c *client) Tokenize(ctx context.Context, text string) ([]int, error) {
	h, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer h.CloseIdleConnections()
	out, err := c.request(ctx, h, "/tokenize", workerRequest{Signature: c.cfg.ModelSignature, Text: text})
	if err != nil {
		return nil, err
	}
	if out.Tokens == nil {
		return nil, unavailable("worker omitted tokens")
	}
	for _, token := range out.Tokens {
		if token < 0 {
			return nil, unavailable("worker returned invalid tokens")
		}
	}
	return out.Tokens, nil
}

func (c *client) Embed(ctx context.Context, purpose string, texts []string) ([][]float32, error) {
	if _, err := FormatInput(purpose, ""); err != nil {
		return nil, err
	}
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	h, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer h.CloseIdleConnections()
	vectors := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += MaxBatch {
		end := min(start+MaxBatch, len(texts))
		out, err := c.request(ctx, h, "/embed", workerRequest{Signature: c.cfg.ModelSignature, Purpose: purpose, Texts: texts[start:end]})
		if err != nil {
			return nil, err
		}
		if len(out.Vectors) != end-start {
			return nil, unavailable("worker returned wrong vector count")
		}
		for _, v := range out.Vectors {
			if err := normalize(v); err != nil {
				return nil, err
			}
		}
		vectors = append(vectors, out.Vectors...)
	}
	return vectors, nil
}

func (c *client) request(ctx context.Context, h *http.Client, path string, in workerRequest) (workerResponse, error) {
	var res workerResponse
	if err := post(ctx, h, path, in, &res); err != nil {
		return res, err
	}
	if res.Code != "" {
		if res.Code == "invalid_argument" {
			return res, invalid("invalid embedding input")
		}
		if res.Code == "asset_invalid" {
			return res, assetInvalid("embedding worker identity mismatch")
		}
		return res, unavailable("embedding worker inference failed")
	}
	return res, nil
}

func pause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
