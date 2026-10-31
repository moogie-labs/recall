package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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
	if a.Root != filepath.Join(wantHome, "Documents", "recall") {
		t.Fatalf("root=%q", a.Root)
	}
	if _, err := os.Stat(a.Root); !os.IsNotExist(err) {
		t.Fatalf("Resolve created root: %v", err)
	}
}

func TestNewDefaultLeavesLegacyCollectionUntouched(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("RECALL_HOME", "")
	legacy := filepath.Join(home, ".local", "share", "recall")
	if err := os.MkdirAll(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(legacy, "config.json")
	if err := os.WriteFile(path, []byte("legacy collection sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Resolve("")
	if err != nil || filepath.Base(filepath.Dir(cfg.Root)) != "Documents" {
		t.Fatalf("new default: %+v %v", cfg, err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "legacy collection sentinel" {
		t.Fatalf("legacy collection changed: %s %v", data, err)
	}
	if _, err := os.Stat(cfg.Root); !os.IsNotExist(err) {
		t.Fatalf("resolution created new default: %v", err)
	}
}

func TestPrepareIfAbsentCreatesSelectedIdentities(t *testing.T) {
	cfg, err := Resolve(filepath.Join(t.TempDir(), "collection"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ModelID, cfg.ModelSHA256, cfg.ModelSignature, cfg.RuntimeID = "model", "digest", "signature", "runtime"
	got, err := PrepareIfAbsent(cfg)
	if err != nil || !reflect.DeepEqual(got, cfg) {
		t.Fatalf("initial publication: %+v %v; want %+v", got, err, cfg)
	}
}

func TestPrepareIfAbsentCompetingNewRoots(t *testing.T) {
	for range 8 {
		cfg := Config{Root: filepath.Join(t.TempDir(), "new-collection"), Editor: []string{"nvim"}, Timezone: "Local"}
		start := make(chan struct{})
		errs := make(chan error, 16)
		var wg sync.WaitGroup
		for range cap(errs) {
			wg.Go(func() {
				<-start
				_, err := PrepareIfAbsent(cfg)
				errs <- err
			})
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("competing initializer could not prepare a newly created root: %v", err)
			}
		}
	}
}

func TestPrepareIfAbsentRejectsDanglingRootSymlink(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root-link")
	if err := os.Symlink(filepath.Join(dir, "missing-target"), root); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareIfAbsent(Config{Root: root}); err == nil {
		t.Fatal("dangling root symlink accepted as an uninitialized directory")
	}
	if _, err := os.Lstat(filepath.Join(dir, "missing-target")); !os.IsNotExist(err) {
		t.Fatalf("created collection through a dangling root link: %v", err)
	}
}

func TestPrepareIfAbsentReloadsWinnerAndPreservesUnknownBytes(t *testing.T) {
	stale, err := Resolve(filepath.Join(t.TempDir(), "collection"))
	if err != nil {
		t.Fatal(err)
	}
	stale.ModelID, stale.RuntimeID = "losing-model", "losing-runtime"
	if err := Prepare(stale); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(stale.Root, "config.json")
	winner := []byte("{\n  \"model_id\": \"winning-model\", \"runtime_id\": \"winning-runtime\",\n  \"editor\": [\"user-editor\", \"--keep\"], \"timezone\": \"America/New_York\",\n  \"future_user_settings\": {\"preserve\": true}\n}\n")
	if err := os.WriteFile(configPath, winner, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(stale.Content, "user.note.md"), stale.Database} {
		if err := os.WriteFile(path, []byte("existing user bytes"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Force the publication path after an absent-config snapshot became stale,
	// independent of scheduling or an early existence check in the public helper.
	if err := prepare(stale, false); err != nil {
		t.Fatal(err)
	}
	got, err := PrepareIfAbsent(stale)
	if err != nil || got.ModelID != "winning-model" || got.RuntimeID != "winning-runtime" || got.Timezone != "America/New_York" || !reflect.DeepEqual(got.Editor, []string{"user-editor", "--keep"}) {
		t.Fatalf("winner not reloaded: %+v %v", got, err)
	}
	for path, want := range map[string][]byte{configPath: winner, filepath.Join(stale.Content, "user.note.md"): []byte("existing user bytes"), stale.Database: []byte("existing user bytes")} {
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != string(want) {
			t.Fatalf("competing publication changed %s: %q %v", path, raw, err)
		}
	}
	entries, err := os.ReadDir(stale.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".config-") {
			t.Fatalf("configuration stage leaked: %s", entry.Name())
		}
	}
}

func TestPrepareIfAbsentPreservesInvalidWinner(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	winner := []byte("user's incomplete edit")
	if err := os.WriteFile(path, winner, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareIfAbsent(Config{Root: root}); model.ErrorCode(err) != "invalid_argument" {
		t.Fatalf("invalid winner not reported: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != string(winner) {
		t.Fatalf("invalid winner overwritten: %q %v", raw, err)
	}
}

func TestPrepareStillReplacesSavedPreferences(t *testing.T) {
	cfg, err := Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Editor, cfg.Timezone = []string{"replacement-editor"}, "America/New_York"
	if err := Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(cfg.Root)
	if err != nil || !reflect.DeepEqual(got, cfg) {
		t.Fatalf("intentional Prepare replacement changed: %+v %v", got, err)
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
