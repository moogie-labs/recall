package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

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
	// Always derive layout from the collection root, never caller-supplied paths.
	resolved, err := config.Resolve(cfg.Root)
	if err != nil {
		return ready, err
	}
	configPath := filepath.Join(resolved.Root, "config.json")
	if _, err := os.Lstat(configPath); errors.Is(err, os.ErrNotExist) {
		resolved.Editor, resolved.Timezone = cfg.Editor, cfg.Timezone
		resolved.ModelID, resolved.ModelSHA256 = m.ID, m.SHA256
		resolved.ModelSignature, resolved.RuntimeID = "sha256:"+m.SHA256, rt.ID
		if err := config.Prepare(resolved); err != nil {
			return ready, err
		}
	} else if err != nil {
		return ready, err
	}
	// An existing configuration is never rewritten during retries, including
	// unknown keys and formatting. Conflicting identities require explicit action.
	store, err := fsstore.Open(resolved)
	if err != nil {
		return ready, err
	}
	if err := store.Close(); err != nil {
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
