package bootstrap

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"recall/internal/config"
	"recall/internal/model"
)

func fixtureManifest(raw []byte) Manifest {
	return Manifest{ID: "fixture", Filename: "fixture.gguf", Size: int64(len(raw)), SHA256: fmt.Sprintf("%x", sha256.Sum256(raw)), License: "Apache-2.0"}
}

func writeFile(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(want) {
		t.Fatalf("%s: got %q, %v; want %q", path, got, err, want)
	}
}

func TestFetchRejectsWrongChecksumAndPreservesInstalledFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "bad") }))
	defer server.Close()
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "model.gguf")
			if existing {
				writeFile(t, dest, []byte("previous verified version"))
			}
			m := fixtureManifest([]byte("new"))
			m.URL = server.URL
			if err := Fetch(t.Context(), server.Client(), m, dest, nil); model.ErrorCode(err) != "asset_invalid" {
				t.Fatalf("expected asset_invalid, got %v", err)
			}
			if existing {
				assertFile(t, dest, []byte("previous verified version"))
			} else if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid asset installed: %v", err)
			}
			entries, _ := os.ReadDir(filepath.Dir(dest))
			for _, entry := range entries {
				if entry.Name() != "model.gguf" {
					t.Fatalf("stage leaked: %s", entry.Name())
				}
			}
		})
	}
}

func TestFetchInterruptedThenSafeRestartWithServerIgnoringRange(t *testing.T) {
	raw := []byte("verified model fixture")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(raw)))
		if calls.Add(1) == 1 {
			w.Write(raw[:4]) // The declared body is interrupted.
			return
		}
		w.Write(raw) // Always 200, even if a client asks for Range.
	}))
	defer server.Close()
	dest := filepath.Join(t.TempDir(), "model.gguf")
	m := fixtureManifest(raw)
	m.URL = server.URL
	if err := Fetch(t.Context(), server.Client(), m, dest, nil); err == nil {
		t.Fatal("interrupted response succeeded")
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial download published: %v", err)
	}
	// A leftover partial must never be blindly appended to a full 200 response.
	writeFile(t, dest+".part", raw[:4])
	var done, total int64
	if err := Fetch(t.Context(), server.Client(), m, dest, func(d, n int64) { done, total = d, n }); err != nil {
		t.Fatal(err)
	}
	assertFile(t, dest, raw)
	if done != int64(len(raw)) || total != int64(len(raw)) {
		t.Fatalf("progress: %d/%d", done, total)
	}
	server.Close()
	if err := Fetch(t.Context(), server.Client(), m, dest, nil); err != nil {
		t.Fatalf("verified cache required network: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("unexpected network calls: %d", calls.Load())
	}
}

func TestFetchCancellationAndSizeValidation(t *testing.T) {
	for _, body := range []string{"tiny", strings.Repeat("x", 100)} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
			defer server.Close()
			m := fixtureManifest([]byte("12345678"))
			m.URL = server.URL
			dest := filepath.Join(t.TempDir(), "model.gguf")
			if err := Fetch(t.Context(), server.Client(), m, dest, nil); model.ErrorCode(err) != "asset_invalid" {
				t.Fatalf("wrong size accepted: %v", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := Fetch(ctx, server.Client(), m, dest, nil); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
}

func TestFetchRejectsSymlinkDestination(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	writeFile(t, outside, []byte("unchanged"))
	dest := filepath.Join(dir, "model.gguf")
	if err := os.Symlink(outside, dest); err != nil {
		t.Fatal(err)
	}
	m := fixtureManifest([]byte("unchanged"))
	if err := Fetch(t.Context(), nil, m, dest, nil); err == nil {
		t.Fatal("symlink accepted as a cached model")
	}
	assertFile(t, outside, []byte("unchanged"))
}

func TestFetchCancellationDuringDownloadCleansStageAndCanRetry(t *testing.T) {
	raw := []byte(strings.Repeat("model", 2048))
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(raw)))
		if calls.Add(1) == 1 {
			w.Write(raw[:4])
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		w.Write(raw)
	}))
	defer server.Close()
	m := fixtureManifest(raw)
	m.URL = server.URL
	dest := filepath.Join(t.TempDir(), "model.gguf")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := Fetch(ctx, server.Client(), m, dest, func(done, total int64) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("active cancellation lost: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled download left partial files: %v %v", entries, err)
	}
	if err := Fetch(t.Context(), server.Client(), m, dest, nil); err != nil {
		t.Fatal(err)
	}
	assertFile(t, dest, raw)
}

func TestEnsureModelRechecksCacheAndIgnoresCallerLayoutPaths(t *testing.T) {
	raw := []byte("good model")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(raw) }))
	defer server.Close()
	m := fixtureManifest(raw)
	m.URL = server.URL
	cfg := preparedConfig(t)
	outside := t.TempDir()
	cfg.ModelsDir = outside
	dest := filepath.Join(cfg.Root, "models", m.Filename)
	writeFile(t, dest, []byte("bad! model")) // Same length; cache must be hashed.
	if err := ensureModel(t.Context(), cfg, m, server.Client(), nil); err != nil {
		t.Fatal(err)
	}
	assertFile(t, dest, raw)
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("caller-supplied ModelsDir redirected the download")
	}
	cfg.ModelSHA256 = strings.Repeat("0", 64)
	if err := EnsureModel(t.Context(), cfg, nil); model.ErrorCode(err) != "asset_invalid" {
		t.Fatalf("public EnsureModel ignored pinned identity: %v", err)
	}
}

func TestEnsureModelRejectsSymlinkedAssetDirectory(t *testing.T) {
	for _, target := range []string{"outside", "content"} {
		t.Run(target, func(t *testing.T) {
			cfg := preparedConfig(t)
			modelsDir := filepath.Join(cfg.Root, "models")
			if err := os.Remove(modelsDir); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			link := outside
			if target == "content" {
				link = "content"
			}
			if err := os.Symlink(link, modelsDir); err != nil {
				t.Fatal(err)
			}
			if err := EnsureModel(t.Context(), config.Config{Root: cfg.Root}, nil); model.ErrorCode(err) != "asset_invalid" {
				t.Fatalf("symlinked asset directory accepted: %v", err)
			}
		})
	}
}

func TestAssetDirectorySwapCannotRedirectIntoContent(t *testing.T) {
	cfg := preparedConfig(t)
	r, err := os.OpenRoot(cfg.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	info, err := r.Lstat("models")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Rename("models", "original-models"); err != nil {
		t.Fatal(err)
	}
	if err := r.Symlink("content", "models"); err != nil {
		t.Fatal(err)
	}
	opened, err := openCheckedRoot(r, "models", info)
	if opened != nil {
		opened.Close()
	}
	if model.ErrorCode(err) != "asset_invalid" {
		t.Fatalf("layout swap could redirect asset writes into source content: %v", err)
	}
}
