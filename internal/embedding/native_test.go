package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"recall/assets"
	"recall/internal/bootstrap"
	"recall/internal/config"
	"recall/internal/model"
)

func TestEmbeddingPrefixes(t *testing.T) {
	for _, tt := range []struct{ purpose, text, want string }{
		{"query", "where is the guide?", "search_query: where is the guide?"},
		{"document", "the guide", "search_document: the guide"},
		{"query", "search_query: the guide", "search_query: the guide"},
		{"document", "search_document: the guide", "search_document: the guide"},
	} {
		got, err := FormatInput(tt.purpose, tt.text)
		if err != nil || got != tt.want {
			t.Fatalf("%q %v", got, err)
		}
	}
	if _, err := FormatInput("other", "secret"); err == nil {
		t.Fatal("invalid purpose accepted")
	}
}

func testNative(t *testing.T, handler http.HandlerFunc) *native {
	t.Helper()
	dir := shortHome(t)
	socket := filepath.Join(dir, "n.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	s := &http.Server{Handler: handler}
	go s.Serve(l)
	t.Cleanup(func() { s.Close() })
	return newNative(socket)
}

func TestNativePrefixesTokenBudgetBatchAndNormalization(t *testing.T) {
	var batches []int
	n := testNative(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tokenize":
			var b struct {
				Content    string `json:"content"`
				AddSpecial bool   `json:"add_special"`
			}
			json.NewDecoder(r.Body).Decode(&b)
			if !strings.HasPrefix(b.Content, "search_document: ") || !b.AddSpecial {
				t.Error("prefix/special tokens omitted")
			}
			json.NewEncoder(w).Encode(map[string]any{"tokens": []int{1, 2, 3}})
		case "/v1/embeddings":
			var b struct {
				Input []string `json:"input"`
			}
			json.NewDecoder(r.Body).Decode(&b)
			batches = append(batches, len(b.Input))
			var data []any
			for i := len(b.Input) - 1; i >= 0; i-- {
				v := make([]float32, 768)
				v[i] = 3
				v[767] = 4
				data = append(data, map[string]any{"index": i, "embedding": v})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": data})
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	})
	v, err := n.Embed(context.Background(), "document", []string{"a", "b", "c", "d", "e"})
	if err != nil || len(v) != 5 {
		t.Fatalf("%v %v", v, err)
	}
	if len(batches) != 2 || batches[0] != 4 || batches[1] != 1 {
		t.Fatalf("batches %v", batches)
	}
	for _, x := range v {
		var norm float64
		for _, f := range x {
			norm += float64(f) * float64(f)
		}
		if len(x) != 768 || math.Abs(norm-1) > 1e-6 {
			t.Fatalf("invalid normalized vector: %d %f", len(x), norm)
		}
	}
}

func TestNativeRejectsInvalidProtocolWithoutLeakingText(t *testing.T) {
	for _, tt := range []struct{ name, body string }{
		{"malformed", "{"}, {"nan", `{"data":[{"index":0,"embedding":[NaN]}]}`},
		{"dimension", `{"data":[{"index":0,"embedding":[1,2]}]}`},
		{"missing", `{"data":[]}`}, {"index", `{"data":[{"index":9,"embedding":[]}]}`},
		{"overflow", `{"data":[{"index":0,"embedding":[1e100]}]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			n := testNative(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/tokenize" {
					w.Write([]byte(`{"tokens":[1]}`))
					return
				}
				w.Write([]byte(tt.body))
			})
			_, err := n.Embed(context.Background(), "query", []string{"secret prompt"})
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe/absent error: %v", err)
			}
		})
	}
	for _, count := range []int{256, 257} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			n := testNative(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/tokenize" {
					json.NewEncoder(w).Encode(map[string]any{"tokens": make([]int, count)})
					return
				}
				if count == 257 {
					t.Error("over-budget input reached inference")
				}
				v := make([]float32, 768)
				v[0] = 1
				json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": v}}})
			})
			if count == 257 {
				if _, err := n.Embed(context.Background(), "query", []string{"secret"}); err == nil {
					t.Fatal("over-budget accepted")
				}
			} else {
				vectors, err := n.Embed(context.Background(), "query", []string{"a"})
				if err != nil || len(vectors) != 1 {
					t.Fatal(vectors, err)
				}
			}
		})
	}
}

func TestNativeRejectsOversizedResponseAfterValidJSON(t *testing.T) {
	n := testNative(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tokens":[1]}` + strings.Repeat(" ", 1<<20) + "trailing data"))
	})
	if _, err := n.Tokenize(context.Background(), "private"); err == nil {
		t.Fatal("oversized response tail ignored")
	}
}

func TestNativeCancellationAndHTTPFailure(t *testing.T) {
	n := testNative(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "secret", 500) })
	if _, err := n.Tokenize(context.Background(), "secret"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := n.Tokenize(ctx, "a"); err != context.Canceled {
		t.Fatalf("lost cancellation: %v", err)
	}
}

func TestCancellationWhileReadingResponsePreservesContext(t *testing.T) {
	started := make(chan struct{})
	n := testNative(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("{"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	base := n.http.Transport
	n.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		res, err := base.RoundTrip(r)
		if err == nil {
			res.Body = &signalBody{ReadCloser: res.Body, started: started}
		}
		return res, err
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := n.Tokenize(ctx, "private"); done <- err }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("response cancellation lost: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type signalBody struct {
	io.ReadCloser
	started chan struct{}
	once    sync.Once
}

func (b *signalBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	return b.ReadCloser.Read(p)
}

func TestNativeRejectsZeroVectorsDuplicateIndicesAndInvalidTokens(t *testing.T) {
	for _, tt := range []struct {
		name      string
		duplicate bool
	}{{"zero_norm", false}, {"duplicate_indices", true}} {
		t.Run(tt.name, func(t *testing.T) {
			n := testNative(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/tokenize" {
					w.Write([]byte(`{"tokens":[1]}`))
					return
				}
				v := make([]float32, 768)
				if tt.duplicate {
					v[0] = 1
				}
				data := []any{map[string]any{"index": 0, "embedding": v}}
				if tt.duplicate {
					data = append(data, map[string]any{"index": 0, "embedding": v})
				}
				json.NewEncoder(w).Encode(map[string]any{"data": data})
			})
			texts := []string{"a"}
			if tt.duplicate {
				texts = append(texts, "b")
			}
			if _, err := n.Embed(context.Background(), "query", texts); err == nil {
				t.Fatal("invalid vectors accepted")
			}
		})
	}
	for _, body := range []string{`{}`, `{"tokens":[-1]}`, `{"tokens":[1.2]}`, `{"tokens":[1]} {}`} {
		t.Run(body, func(t *testing.T) {
			n := testNative(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) })
			if _, err := n.Tokenize(context.Background(), "secret"); err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatal(err)
			}
		})
	}
}

func TestVerifyAssetsRejectsConflictingOrTamperedWeights(t *testing.T) {
	cfg := testConfig(t)
	if err := verifyAssets(context.Background(), cfg); model.ErrorCode(err) != "asset_invalid" {
		t.Fatal(err)
	}
	var m, rt bootstrap.Manifest
	json.Unmarshal([]byte(assets.ModelLock()), &m)
	json.Unmarshal([]byte(assets.RuntimeLock()), &rt)
	cfg.ModelID = m.ID
	cfg.ModelSHA256 = m.SHA256
	cfg.ModelSignature = "sha256:" + m.SHA256
	cfg.RuntimeID = rt.ID
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cfg.RuntimeDir, rt.ID), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.RuntimeDir, rt.ID, "llama-server"), []byte("invalid test executable"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.ModelsDir, m.Filename)
	if err := os.WriteFile(path, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyAssets(context.Background(), cfg); model.ErrorCode(err) != "asset_invalid" {
		t.Fatal(err)
	}
	if err := os.Truncate(path, m.Size); err != nil {
		t.Fatal(err)
	}
	if err := verifyAssets(context.Background(), cfg); model.ErrorCode(err) != "asset_invalid" {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := verifyAssets(ctx, cfg); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestNativeLaunchArguments(t *testing.T) {
	cfg := testConfig(t)
	args := nativeArgs(cfg, "/private/native.sock")
	want := []string{"--embedding", "--pooling", "mean", "--ctx-size", "2048", "--batch-size", "2048", "--ubatch-size", "512", "--parallel", "1", "--threads", "4", "--gpu-layers", "99", "--no-webui", "--offline", "--model", filepath.Join(cfg.ModelsDir, "nomic-embed-text-v1.5.f16.gguf"), "--host", "/private/native.sock"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("flags %q", args)
	}
}

// Opt-in acceptance test. Artifacts must already be downloaded; it never
// contacts a service or changes the user's collection, HOME or cache.
func TestPinnedNativeSmoke(t *testing.T) {
	assetDir := os.Getenv("RECALL_NATIVE_ASSETS")
	if assetDir == "" {
		t.Skip("set RECALL_NATIVE_ASSETS to checksum-pinned validation assets")
	}
	assetDir, err := filepath.Abs(assetDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", shortHome(t))
	t.Setenv("RECALL_TEST_REAL", "1")
	cfg, err := config.Resolve(filepath.Join(t.TempDir(), "native-collection"))
	if err != nil {
		t.Fatal(err)
	}
	var m, rt bootstrap.Manifest
	json.Unmarshal([]byte(assets.ModelLock()), &m)
	json.Unmarshal([]byte(assets.RuntimeLock()), &rt)
	cfg.ModelID = m.ID
	cfg.ModelSHA256 = m.SHA256
	cfg.ModelSignature = "sha256:" + m.SHA256
	cfg.RuntimeID = rt.ID
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(assetDir, m.Filename), filepath.Join(cfg.ModelsDir, m.Filename)); err != nil {
		t.Fatal(err)
	}
	bundle := t.TempDir()
	archive := filepath.Join(bundle, "runtime", "darwin-arm64", rt.Filename)
	if err := os.MkdirAll(filepath.Dir(archive), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(assetDir, rt.Filename), archive); err != nil {
		t.Fatal(err)
	}
	ready, err := bootstrap.Initialize(context.Background(), cfg, bundle, nil)
	if err != nil || !ready.MetadataReady || !ready.ModelReady || !ready.RuntimeReady {
		t.Fatal(ready, err)
	}
	bad := cfg
	bad.RuntimeDir = t.TempDir()
	if err := verifyAssets(context.Background(), bad); model.ErrorCode(err) != "asset_invalid" {
		t.Fatal("redirected executable path was not rejected:", err)
	}
	c := testClient(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	started := time.Now()
	q, err := c.Embed(ctx, "query", []string{"how do I reset my account password?"})
	if err != nil {
		t.Fatal("pinned native query inference:", err)
	}
	t.Logf("startup plus first query inference: %s", time.Since(started))
	nativePID := 0
	t.Cleanup(func() {
		stopFixtureWorker(t, cfg)
		if nativePID != 0 && !processGone(nativePID) {
			t.Error("real native not reaped after manager shutdown")
		}
	})
	started = time.Now()
	d, err := c.Embed(ctx, "document", []string{"To reset your account password, open settings and choose Reset password.", "A sourdough bread recipe uses flour, water and salt."})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("warm two-document inference: %s", time.Since(started))
	for _, v := range append(q, d...) {
		var norm float64
		for _, f := range v {
			x := float64(f)
			if math.IsNaN(x) || math.IsInf(x, 0) {
				t.Fatal("nonfinite native vector")
			}
			norm += x * x
		}
		if len(v) != 768 || math.Abs(norm-1) > 1e-5 {
			t.Fatalf("native shape/norm: %d %.9f", len(v), norm)
		}
	}
	dot := func(a, b []float32) float64 {
		var s float64
		for i := range a {
			s += float64(a[i]) * float64(b[i])
		}
		return s
	}
	related, unrelated := dot(q[0], d[0]), dot(q[0], d[1])
	t.Logf("cosine related=%.6f unrelated=%.6f", related, unrelated)
	if related <= unrelated {
		t.Fatal("pinned native model failed relatedness check")
	}
	tokens, err := c.Tokenize(ctx, "search_query: how do I reset my account password?")
	if err != nil || len(tokens) == 0 {
		t.Fatal(tokens, err)
	}
	t.Logf("prefixed query tokens including special tokens: %d", len(tokens))
	ps, err := exec.Command("ps", "-axo", "pid,ppid,rss,args").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(ps), "\n") {
		if strings.Contains(line, filepath.Join(cfg.RuntimeDir, "b11669", "llama-server")) {
			t.Logf("native PID PPID RSS(KiB) ARGV: %s", strings.TrimSpace(line))
			pid, err := strconv.Atoi(strings.Fields(line)[0])
			if err != nil {
				t.Fatal(err)
			}
			nativePID = pid
			// lsof returns status 1 with no output when this exact native PID
			// owns no TCP sockets. Both HTTP transports use Unix sockets only.
			out, err := exec.Command("/usr/sbin/lsof", "-nP", "-a", "-p", strconv.Itoa(pid), "-iTCP", "-Fn").CombinedOutput()
			if len(out) != 0 || err == nil {
				t.Fatalf("native TCP socket check: %s %v", out, err)
			}
			t.Log("native TCP socket check: none")
		}
	}
}
