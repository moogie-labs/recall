package cli

import (
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"recall/internal/app"
	"recall/internal/bootstrap"
	"recall/internal/config"
	"recall/internal/embedding"
	"recall/internal/model"
)

func (i *invocation) initCommand() *cobra.Command {
	return i.command("init", "Initialize retryably, verify assets, and test real semantic inference", noArgs, func(*cobra.Command, []string) (any, error) {
		cfg, err := i.config()
		if err != nil {
			return nil, err
		}
		assets := os.Getenv("RECALL_ASSETS_DIR")
		if assets != "" {
			if !filepath.IsAbs(assets) {
				return nil, invalid("RECALL_ASSETS_DIR must be absolute")
			}
		} else {
			executable, err := i.runner.executable()
			if err != nil {
				return nil, err
			}
			assets = filepath.Join(filepath.Dir(executable), "assets")
		}
		last := -10
		ready, err := i.runner.initialize(i.ctx, cfg, assets, func(done, total int64) {
			if total <= 0 {
				return
			}
			percent := int(float64(done) / float64(total) * 100)
			if percent >= last+10 || percent == 100 && last != 100 {
				fmt.Fprintf(i.stderr, "Downloading model: %d%%\n", percent)
				last = percent
			}
		})
		if err != nil {
			return nil, err
		}
		if !ready.MetadataReady || !ready.ModelReady || !ready.RuntimeReady {
			return nil, &model.Error{Code: "asset_invalid", Message: "initialization did not verify all required assets"}
		}
		// Bootstrap publishes identities and validates assets, not inference or
		// the SQLite schema. Reload the saved winner before constructing either.
		cfg, err = config.Resolve(cfg.Root)
		if err != nil {
			return nil, err
		}
		i.cfg = &cfg
		return i.withApp(func(*app.App) (any, error) {
			provider := i.runner.provider(cfg)
			if provider == nil {
				return nil, smokeFailure()
			}
			vectors, err := provider.Embed(i.ctx, "query", []string{"Recall semantic readiness check."})
			if err != nil {
				return nil, err
			}
			if len(vectors) != 1 || !validSmokeVector(vectors[0]) {
				return nil, smokeFailure()
			}
			return struct {
				bootstrap.Readiness
				SemanticReady bool   `json:"semantic_ready"`
				Root          string `json:"root"`
			}{ready, true, cfg.Root}, nil
		})
	})
}

func smokeFailure() error {
	return &model.Error{Code: "runtime_unavailable", Message: "semantic readiness check failed; initialization is safe to retry"}
}

func validSmokeVector(vector []float32) bool {
	if len(vector) != embedding.Dimensions {
		return false
	}
	norm := float64(0)
	for _, v := range vector {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return false
		}
		norm += float64(v) * float64(v)
	}
	return math.Abs(norm-1) < 1e-4
}

func (i *invocation) syncCommand() *cobra.Command {
	var rebuild, wait bool
	c := i.command("sync", "Reconcile sources; optionally rebuild derived state and finish embeddings", noArgs, func(*cobra.Command, []string) (any, error) {
		return i.withApp(func(a *app.App) (any, error) {
			var report model.SyncReport
			var err error
			if rebuild {
				report, err = a.Rebuild(i.ctx)
			} else {
				report, err = a.Sync(i.ctx)
			}
			if err == nil && wait {
				err = a.IndexPending(i.ctx)
			}
			return report, err
		})
	})
	c.Flags().BoolVar(&rebuild, "rebuild", false, "Clear only derived state under the shared lock and reread sources")
	c.Flags().BoolVar(&wait, "wait", false, "Complete pending semantic embeddings before returning")
	return c
}

func (i *invocation) statusCommand() *cobra.Command {
	return i.command("status", "Show metadata status without starting inference", noArgs, func(*cobra.Command, []string) (any, error) {
		return i.withApp(func(a *app.App) (any, error) {
			snapshot, err := a.List(i.ctx, "", true)
			if err != nil {
				return nil, err
			}
			tasks := 0
			for _, d := range snapshot.Documents {
				if d.Kind == model.Task {
					tasks++
				}
			}
			return struct {
				Root          string             `json:"root"`
				Timezone      string             `json:"timezone"`
				MetadataReady bool               `json:"metadata_ready"`
				Documents     int                `json:"documents"`
				Tasks         int                `json:"tasks"`
				Folders       int                `json:"folders"`
				Diagnostics   []model.Diagnostic `json:"diagnostics"`
			}{i.cfg.Root, i.cfg.Timezone, true, len(snapshot.Documents), tasks, len(snapshot.Folders), snapshot.Diagnostics}, nil
		})
	})
}
