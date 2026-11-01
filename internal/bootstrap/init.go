package bootstrap

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	"recall/assets"
	"recall/internal/config"
	"recall/internal/fsstore"
	"recall/internal/model"
)

type Readiness struct {
	MetadataReady bool `json:"metadata_ready"`
	ModelReady    bool `json:"model_ready"`
	RuntimeReady  bool `json:"runtime_ready"`
}

// Initialize prepares metadata before attempting assets. ModelReady and
// RuntimeReady mean verified installed bytes only, NOT semantic readiness.
// The caller must perform a real inference smoke test before advertising that.
func Initialize(ctx context.Context, cfg config.Config, assetsDir string, progress func(done, total int64)) (Readiness, error) {
	m, rt, err := pinnedManifests()
	if err != nil {
		return Readiness{}, err
	}
	return initialize(ctx, cfg, assetsDir, progress, m, rt, nil, runtime.GOOS+"-"+runtime.GOARCH)
}

func initialize(ctx context.Context, cfg config.Config, assetsDir string, progress func(done, total int64), m, rt Manifest, client *http.Client, platform string) (Readiness, error) {
	var ready Readiness
	if err := ctx.Err(); err != nil {
		return ready, err
	}
	if !filepath.IsAbs(cfg.Root) {
		return ready, &model.Error{Code: "invalid_argument", Message: "initialization requires an absolute collection root"}
	}
	resolved, err := prepareMetadata(cfg, m, rt)
	if err != nil {
		return ready, err
	}
	ready.MetadataReady = true
	if err := supportedPlatform(platform); err != nil {
		return ready, err
	}
	if resolved.ModelID != m.ID || resolved.ModelSHA256 != m.SHA256 || resolved.ModelSignature == "" || resolved.RuntimeID != rt.ID {
		return ready, invalidAsset("existing configuration has missing or conflicting asset identities; configuration preserved")
	}
	if err := installRuntime(ctx, resolved, filepath.Join(assetsDir, "runtime", "darwin-arm64", rt.Filename), rt); err != nil {
		return ready, err
	}
	ready.RuntimeReady = true
	if err := ensureModel(ctx, resolved, m, client, progress); err != nil {
		return ready, err
	}
	ready.ModelReady = true
	return ready, nil
}

// PrepareMetadata creates (or reloads) the collection layout and configuration
// with the pinned asset identities. It is local and fast: no runtime extraction,
// download or inference. Notes, tasks and keyword search work afterwards;
// Initialize later completes the semantic assets for the same collection.
func PrepareMetadata(ctx context.Context, cfg config.Config) (config.Config, error) {
	if err := ctx.Err(); err != nil {
		return config.Config{}, err
	}
	if !filepath.IsAbs(cfg.Root) {
		return config.Config{}, &model.Error{Code: "invalid_argument", Message: "initialization requires an absolute collection root"}
	}
	m, rt, err := pinnedManifests()
	if err != nil {
		return config.Config{}, err
	}
	return prepareMetadata(cfg, m, rt)
}

func prepareMetadata(cfg config.Config, m, rt Manifest) (config.Config, error) {
	// Prepare only if absent, atomically publish without replacement, then reload
	// the winner. Layout paths are derived from the root, not caller-supplied paths.
	cfg.ModelID, cfg.ModelSHA256 = m.ID, m.SHA256
	cfg.ModelSignature, cfg.RuntimeID = "sha256:"+m.SHA256, rt.ID
	resolved, err := config.PrepareIfAbsent(cfg)
	if err != nil {
		return config.Config{}, err
	}
	// An existing configuration is never rewritten during retries, including
	// unknown keys and formatting. Conflicting identities require explicit action.
	store, err := fsstore.Open(resolved)
	if err != nil {
		return config.Config{}, err
	}
	if err := store.Close(); err != nil {
		return config.Config{}, err
	}
	return resolved, nil
}

// AssetsPresent is a cheap readiness hint for UIs: the collection pins this
// build's identities, the model has its pinned size and this build's runtime
// executable is installed. It never hashes; the inference worker independently
// rehashes the model and runtime payload before every launch.
func AssetsPresent(cfg config.Config) bool {
	m, rt, err := pinnedManifests()
	if err != nil || cfg.ModelID != m.ID || cfg.ModelSHA256 != m.SHA256 || cfg.RuntimeID != rt.ID {
		return false
	}
	directory, err := assets.RuntimeInstallationDirectory(rt.ID, rt.SHA256, rt.InstallationID)
	if err != nil {
		return false
	}
	r, err := os.OpenRoot(cfg.Root)
	if err != nil {
		return false
	}
	defer r.Close()
	model, err := r.Lstat(filepath.Join("models", m.Filename))
	if err != nil || !model.Mode().IsRegular() || model.Size() != m.Size {
		return false
	}
	server, err := r.Lstat(filepath.Join("runtime", directory, "llama-server"))
	return err == nil && server.Mode().IsRegular() && server.Mode().Perm()&0111 != 0
}
