package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"recall/internal/model"
)

func TestResolveIgnoresWorkingDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("RECALL_HOME", "")
	projectA, projectB := t.TempDir(), t.TempDir()
	t.Chdir(projectA)
	a, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(projectB)
	b, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("cwd changed configuration: %+v != %+v", a, b)
	}
	wantHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	if a.Root != filepath.Join(wantHome, ".local", "share", "recall") {
		t.Fatalf("root=%q", a.Root)
	}
	if _, err := os.Stat(a.Root); !os.IsNotExist(err) {
		t.Fatalf("Resolve created root: %v", err)
	}
}

func TestRootPrecedence(t *testing.T) {
	home, env, explicit := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("RECALL_HOME", env)
	for _, tc := range []struct{ arg, want string }{{explicit, explicit}, {"", env}} {
		want, err := filepath.EvalSymlinks(tc.want)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Resolve(tc.arg)
		if err != nil || got.Root != want {
			t.Fatalf("root=%q want=%q err=%v", got.Root, want, err)
		}
	}
	t.Setenv("RECALL_HOME", "relative/collection")
	if _, err := Resolve(""); model.ErrorCode(err) != "invalid_argument" {
		t.Fatalf("relative env: %v", err)
	}
	if _, err := Resolve(explicit); err != nil {
		t.Fatalf("explicit must ignore invalid env: %v", err)
	}
}

func TestPreparePersistsConfiguration(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Recall collection with spaces")
	cfg, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Timezone != "Local" || !reflect.DeepEqual(cfg.Editor, []string{"nvim"}) {
		t.Fatalf("defaults: %+v", cfg)
	}
	cfg.ModelID, cfg.ModelSHA256, cfg.ModelSignature = "model-id", "model-sha256", "model-signature"
	cfg.RuntimeID, cfg.Timezone, cfg.Editor = "runtime-id", "America/New_York", []string{"nvim", "--clean"}
	if err := Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{cfg.Content, cfg.RunDir, cfg.ModelsDir, cfg.RuntimeDir} {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			t.Fatalf("layout %q: %v", path, err)
		}
	}
	if cfg.Database != filepath.Join(cfg.Root, "index.sqlite") {
		t.Fatalf("database=%q", cfg.Database)
	}
	if info, err := os.Stat(cfg.Database); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("database placeholder: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(cfg.Root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk map[string]any
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	if disk["model_id"] != "model-id" || disk["runtime_id"] != "runtime-id" {
		t.Fatalf("identities not persisted: %s", raw)
	}
	got, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, cfg) {
		t.Fatalf("round trip: %+v != %+v", got, cfg)
	}
	if err := Prepare(got); err != nil {
		t.Fatalf("retry initialization: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(cfg.Root, "config.json"))
	if err != nil || string(after) != string(raw) {
		t.Fatalf("retry changed config: %s, %v", after, err)
	}
}

func TestPrepareRejectsSymlinkedLayout(t *testing.T) {
	for _, name := range []string{"content", "run", "models", "runtime", "config.json", "index.sqlite"} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Resolve(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			if err := os.Symlink(outside, filepath.Join(cfg.Root, name)); err != nil {
				t.Fatal(err)
			}
			if err := Prepare(cfg); model.ErrorCode(err) != "outside_root" {
				t.Fatalf("symlink: %v", err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("wrote outside root: %v %v", entries, err)
			}
		})
	}
}

func TestPreparePreservesExistingDatabase(t *testing.T) {
	cfg, err := Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.Database, []byte("existing derived database"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cfg.Database)
	if err != nil || string(raw) != "existing derived database" {
		t.Fatalf("database truncated: %q %v", raw, err)
	}
}

func TestResolveCanonicalizesRootWithoutShellExpansion(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	cfg, err := Resolve(filepath.Join(link, "$HOME", "~"))
	if err != nil {
		t.Fatal(err)
	}
	real, err = filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Root != filepath.Join(real, "$HOME", "~") {
		t.Fatalf("root=%q", cfg.Root)
	}
}

func TestResolveRejectsMalformedConfiguration(t *testing.T) {
	for _, raw := range []string{`not json`, `{"editor":[]}`, `{"timezone":"Not/A_Zone"}`, `{"timezone":"Local"} trailing garbage`, `{"timezone":"Local"} {}`} {
		t.Run(raw, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Resolve(root); model.ErrorCode(err) != "invalid_argument" {
				t.Fatalf("malformed configuration: %v", err)
			}
		})
	}
}
