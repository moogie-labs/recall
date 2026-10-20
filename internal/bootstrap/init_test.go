package bootstrap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	"recall/assets"
	"recall/internal/app"
	"recall/internal/config"
	"recall/internal/model"
)

func TestPinnedManifestsUseImmutableReleaseIdentities(t *testing.T) {
	m, rt, err := pinnedManifests()
	if err != nil {
		t.Fatal(err)
	}
	wantModel := Manifest{
		ID:       "nomic-embed-text-v1.5-f16@0188c9bf409793f810680a5a431e7b899c46104c",
		URL:      "https://huggingface.co/nomic-ai/nomic-embed-text-v1.5-GGUF/resolve/0188c9bf409793f810680a5a431e7b899c46104c/nomic-embed-text-v1.5.f16.gguf",
		SHA256:   "f7af6f66802f4df86eda10fe9bbcfc75c39562bed48ef6ace719a251cf1c2fdb",
		Filename: "nomic-embed-text-v1.5.f16.gguf", Size: 274290560, License: "Apache-2.0",
	}
	wantRuntime := Manifest{
		ID: "b11669", URL: "https://github.com/ggml-org/llama.cpp/releases/download/b11669/llama-b11669-bin-macos-arm64.tar.gz",
		SHA256:   "dbed55493d72bc1ba2babc8770d669c2289b5bc4ee9fb2a4dfc158d45b7d13fd",
		Filename: "llama-b11669-bin-macos-arm64.tar.gz", Size: 12191582, License: "MIT",
	}
	if m != wantModel || rt != wantRuntime {
		t.Fatalf("embedded release locks differ: model=%+v runtime=%+v", m, rt)
	}
	var version struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(assets.RuntimeLock()), &version); err != nil || version.Version != "v0.7.0" {
		t.Fatalf("runtime release version: %+v %v", version, err)
	}
}

func TestInitializeRejectsRelativeRootWithoutPreparingIt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-created")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, root)
	if err != nil {
		t.Fatal(err)
	}
	m := fixtureManifest([]byte("fixture"))
	_, rt := runtimeArchive(t)
	ready, err := initialize(t.Context(), config.Config{Root: relative}, t.TempDir(), nil, m, rt, nil, "linux-amd64")
	if model.ErrorCode(err) != "invalid_argument" || ready.MetadataReady {
		t.Fatalf("relative root accepted: %+v %v", ready, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("invalid root was prepared: %v", err)
	}
}

func TestInitializeDoesNotClaimMetadataReadyForBrokenExistingLayout(t *testing.T) {
	cfg := preparedConfig(t)
	if err := os.Remove(cfg.Content); err != nil {
		t.Fatal(err)
	}
	m := fixtureManifest([]byte("fixture"))
	_, rt := runtimeArchive(t)
	ready, err := initialize(t.Context(), cfg, t.TempDir(), nil, m, rt, nil, "linux-amd64")
	if err == nil || ready.MetadataReady {
		t.Fatalf("broken metadata layout marked ready: %+v %v", ready, err)
	}
}

func TestInitializeCannotSelectAssetsFromExternalLocks(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("public installer explicitly rejects other platforms")
	}
	cfg, err := config.Resolve(filepath.Join(t.TempDir(), "collection"))
	if err != nil {
		t.Fatal(err)
	}
	assetsDir := t.TempDir()
	raw, rt := runtimeArchive(t, tarEntry{name: "llama-b11669/llama-server", body: "untrusted identity"})
	archive := filepath.Join(assetsDir, "runtime", "darwin-arm64", rt.Filename)
	if err := os.MkdirAll(filepath.Dir(archive), 0700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, archive, raw)
	lock, _ := json.Marshal(rt)
	writeFile(t, filepath.Join(assetsDir, "runtime.lock.json"), lock)
	ready, err := Initialize(t.Context(), cfg, assetsDir, nil)
	if model.ErrorCode(err) != "asset_invalid" || !ready.MetadataReady || ready.RuntimeReady {
		t.Fatalf("external lock selected unpinned archive: %+v %v", ready, err)
	}
	if err := InstallRuntime(t.Context(), cfg, archive); model.ErrorCode(err) != "asset_invalid" {
		t.Fatalf("public runtime installer accepted an unpinned archive: %v", err)
	}
}

func TestInitializePreservesButRejectsMissingOrConflictingSavedIdentities(t *testing.T) {
	for _, modelID := range []string{"", "user-selected-model"} {
		t.Run(modelID, func(t *testing.T) {
			cfg := preparedConfig(t)
			cfg.ModelID = modelID
			if err := config.Prepare(cfg); err != nil {
				t.Fatal(err)
			}
			saved, err := os.ReadFile(filepath.Join(cfg.Root, "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			m := fixtureManifest([]byte("fixture"))
			_, rt := runtimeArchive(t)
			ready, err := initialize(t.Context(), cfg, t.TempDir(), nil, m, rt, nil, "darwin-arm64")
			if model.ErrorCode(err) != "asset_invalid" || !ready.MetadataReady || ready.RuntimeReady || ready.ModelReady {
				t.Fatalf("saved identity was not checked before installing assets: %+v %v", ready, err)
			}
			assertFile(t, filepath.Join(cfg.Root, "config.json"), saved)
		})
	}
}

func TestInitializePersistsPinnedIdentitiesBeforeAssetFailure(t *testing.T) {
	cfg, err := config.Resolve(filepath.Join(t.TempDir(), "recall"))
	if err != nil {
		t.Fatal(err)
	}
	ready, err := Initialize(t.Context(), cfg, t.TempDir(), nil)
	if err == nil || !ready.MetadataReady || ready.RuntimeReady || ready.ModelReady {
		t.Fatalf("missing bundled runtime: readiness=%+v err=%v", ready, err)
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		if model.ErrorCode(err) != "unsupported_platform" {
			t.Fatalf("unsupported platform: %v", err)
		}
	}
	saved, err := config.Resolve(cfg.Root)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ModelID != "nomic-embed-text-v1.5-f16@0188c9bf409793f810680a5a431e7b899c46104c" || saved.ModelSHA256 != "f7af6f66802f4df86eda10fe9bbcfc75c39562bed48ef6ace719a251cf1c2fdb" || saved.RuntimeID != "b11669" || saved.ModelSignature == "" {
		t.Fatalf("pinned identities not persisted: %+v", saved)
	}
	// Asset failure must not prevent ordinary metadata operations.
	a, err := app.Open(t.Context(), saved)
	if err != nil {
		t.Fatal(err)
	}
	note, err := a.Create(t.Context(), app.CreateInput{Kind: model.Note, Name: "metadata-only", Body: "asset failure does not block writing"})
	if err != nil || note.Body != "asset failure does not block writing" {
		t.Fatalf("source operation after asset failure: %+v %v", note, err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInitializeRetryPreservesSourcesAndUnknownConfiguration(t *testing.T) {
	modelRaw := []byte("tiny model fixture")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(modelRaw) }))
	defer server.Close()
	m := fixtureManifest(modelRaw)
	m.URL = server.URL
	raw, rt := runtimeArchive(t, tarEntry{name: "llama-b11669/llama-server", body: "not executed by bootstrap"})
	cfg, err := config.Resolve(filepath.Join(t.TempDir(), "collection"))
	if err != nil {
		t.Fatal(err)
	}
	assetsDir := t.TempDir()
	archive := filepath.Join(assetsDir, "runtime", "darwin-arm64", rt.Filename)
	if err := os.MkdirAll(filepath.Dir(archive), 0700); err != nil {
		t.Fatal(err)
	}
	// First attempt prepares usable metadata despite a corrupt bundled archive.
	writeFile(t, archive, []byte("interrupted copy"))
	ready, err := initialize(t.Context(), cfg, assetsDir, nil, m, rt, server.Client(), "darwin-arm64")
	if err == nil || !ready.MetadataReady || ready.RuntimeReady || ready.ModelReady {
		t.Fatalf("first attempt: %+v %v", ready, err)
	}
	document := filepath.Join(cfg.Content, "keep.note.md")
	writeFile(t, document, []byte("---\ntype: note\ncustom: untouched\n---\nuser bytes\r\n"))
	writeFile(t, cfg.Database, []byte("existing derived bytes must not be truncated"))
	configPath := filepath.Join(cfg.Root, "config.json")
	configRaw, _ := os.ReadFile(configPath)
	var settings map[string]any
	if err := json.Unmarshal(configRaw, &settings); err != nil {
		t.Fatal(err)
	}
	settings["editor"], settings["timezone"], settings["future_user_setting"] = []string{"custom-editor", "--flag"}, "America/New_York", map[string]any{"keep": true}
	configRaw, _ = json.MarshalIndent(settings, "", "    ")
	writeFile(t, configPath, configRaw)
	writeFile(t, archive, raw)
	for range 2 {
		ready, err = initialize(t.Context(), cfg, assetsDir, nil, m, rt, server.Client(), "darwin-arm64")
		if err != nil || !ready.MetadataReady || !ready.RuntimeReady || !ready.ModelReady {
			t.Fatalf("retry readiness: %+v %v", ready, err)
		}
		assertFile(t, configPath, configRaw)
		assertFile(t, document, []byte("---\ntype: note\ncustom: untouched\n---\nuser bytes\r\n"))
		assertFile(t, cfg.Database, []byte("existing derived bytes must not be truncated"))
	}
	server.Close()
	if _, err := initialize(t.Context(), cfg, assetsDir, nil, m, rt, server.Client(), "darwin-arm64"); err != nil {
		t.Fatalf("offline cached retry: %v", err)
	}
}

func TestInitializeRetriesModelFailureAfterRuntimeInstallation(t *testing.T) {
	modelRaw := []byte("good model")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Write([]byte("bad! model"))
			return
		}
		w.Write(modelRaw)
	}))
	defer server.Close()
	m := fixtureManifest(modelRaw)
	m.URL = server.URL
	raw, rt := runtimeArchive(t, tarEntry{name: "llama-b11669/llama-server", body: "server"})
	cfg, err := config.Resolve(filepath.Join(t.TempDir(), "collection"))
	if err != nil {
		t.Fatal(err)
	}
	assetsDir := t.TempDir()
	archive := filepath.Join(assetsDir, "runtime", "darwin-arm64", rt.Filename)
	if err := os.MkdirAll(filepath.Dir(archive), 0700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, archive, raw)
	ready, err := initialize(t.Context(), cfg, assetsDir, nil, m, rt, server.Client(), "darwin-arm64")
	if model.ErrorCode(err) != "asset_invalid" || !ready.MetadataReady || !ready.RuntimeReady || ready.ModelReady {
		t.Fatalf("model failure readiness: %+v %v", ready, err)
	}
	ready, err = initialize(t.Context(), cfg, assetsDir, nil, m, rt, server.Client(), "darwin-arm64")
	if err != nil || !ready.MetadataReady || !ready.RuntimeReady || !ready.ModelReady {
		t.Fatalf("model retry readiness: %+v %v", ready, err)
	}
	assertFile(t, filepath.Join(cfg.ModelsDir, m.Filename), modelRaw)
}

func TestInitializeUnsupportedPlatformDoesNotFetchAssets(t *testing.T) {
	cfg, err := config.Resolve(filepath.Join(t.TempDir(), "collection"))
	if err != nil {
		t.Fatal(err)
	}
	m := fixtureManifest([]byte("fixture"))
	_, rt := runtimeArchive(t)
	ready, err := initialize(t.Context(), cfg, t.TempDir(), nil, m, rt, nil, "linux-amd64")
	if model.ErrorCode(err) != "unsupported_platform" || !ready.MetadataReady || ready.ModelReady || ready.RuntimeReady {
		t.Fatalf("unsupported platform: %+v %v", ready, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Initialize(ctx, cfg, t.TempDir(), nil); err == nil {
		t.Fatal("canceled initialization succeeded")
	}
}
