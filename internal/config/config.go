// Package config resolves the central collection without creating it. Explicit
// setup uses Prepare or PrepareIfAbsent to create the collection layout.
package config

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"recall/internal/model"
)

type Config struct {
	Root           string   `json:"root"`
	Content        string   `json:"content"`
	Database       string   `json:"database"`
	RunDir         string   `json:"run_dir"`
	ModelsDir      string   `json:"models_dir"`
	RuntimeDir     string   `json:"runtime_dir"`
	ModelID        string   `json:"model_id"`
	ModelSHA256    string   `json:"model_sha256"`
	ModelSignature string   `json:"model_signature"`
	RuntimeID      string   `json:"runtime_id"`
	Timezone       string   `json:"timezone"`
	Editor         []string `json:"editor"`
}

// Resolve applies --root > absolute RECALL_HOME > $HOME/.local/share/recall.
// Relative explicit roots are made absolute once; defaults never depend on cwd.
// Existing root symlinks are canonicalized, but content entries are not followed.
func Resolve(explicit string) (Config, error) {
	root := explicit
	if root == "" {
		root = os.Getenv("RECALL_HOME")
		if root != "" && !filepath.IsAbs(root) {
			return Config{}, invalid("RECALL_HOME must be absolute")
		}
	}
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Config{}, err
		}
		if !filepath.IsAbs(home) {
			return Config{}, invalid("HOME must be absolute")
		}
		root = filepath.Join(home, ".local", "share", "recall")
	}
	root, err := canonical(root)
	if err != nil {
		return Config{}, err
	}
	cfg := layout(root)
	cfg.Timezone, cfg.Editor = "Local", []string{"nvim"}
	r, err := os.OpenRoot(root)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, err
	}
	defer r.Close()
	if err := noSymlink(r, "config.json"); err != nil {
		return Config{}, err
	}
	f, err := r.OpenFile("config.json", os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Config{}, err
	}
	if !info.Mode().IsRegular() {
		return Config{}, invalid("config.json must be a regular file")
	}
	// Paths in the on-disk file are never trusted: derive them from the root.
	var saved Config
	decoder := json.NewDecoder(f)
	if err := decoder.Decode(&saved); err != nil {
		return Config{}, invalid("invalid config.json")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Config{}, invalid("invalid config.json")
	}
	cfg.ModelID, cfg.ModelSHA256, cfg.ModelSignature = saved.ModelID, saved.ModelSHA256, saved.ModelSignature
	cfg.RuntimeID = saved.RuntimeID
	if saved.Timezone != "" {
		cfg.Timezone = saved.Timezone
	}
	if saved.Editor != nil {
		cfg.Editor = saved.Editor
	}
	return cfg, validate(cfg)
}

// Prepare creates private directories and durably persists the identities and
// preferences supplied by initialization. It does not create a database schema.
func Prepare(cfg Config) error {
	return prepare(cfg, true)
}

// PrepareIfAbsent prepares a new collection without replacing a configuration
// published by another initializer or editor. It always reloads the winning
// configuration, including when publication loses a race. Existing collections
// are only read; Prepare retains its intentional replacement behavior.
func PrepareIfAbsent(cfg Config) (Config, error) {
	if !filepath.IsAbs(cfg.Root) {
		return Config{}, invalid("root must be absolute")
	}
	if _, err := os.Lstat(filepath.Join(cfg.Root, "config.json")); err == nil {
		return Resolve(cfg.Root)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Config{}, err
	}
	if err := prepare(cfg, false); err != nil {
		return Config{}, err
	}
	return Resolve(cfg.Root)
}

func prepare(cfg Config, replace bool) error {
	if !filepath.IsAbs(cfg.Root) {
		return invalid("root must be absolute")
	}
	if cfg.Timezone == "" {
		cfg.Timezone = "Local"
	}
	if cfg.Editor == nil {
		cfg.Editor = []string{"nvim"}
	}
	if err := validate(cfg); err != nil {
		return err
	}
	root, err := canonical(cfg.Root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, name := range []string{"content", "models", "runtime", "run", "config.json", "index.sqlite"} {
		if err := noSymlink(r, name); err != nil {
			return err
		}
	}
	for _, name := range []string{"content", "models", "runtime", "run"} {
		if err := r.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := r.Lstat(name)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return invalid(name + " must be a directory")
		}
	}
	// Reserve the database path without truncating an existing derived index.
	// Its schema remains the responsibility of the index package.
	db, err := r.OpenFile("index.sqlite", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		if err := errors.Join(db.Sync(), db.Close()); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := noSymlink(r, "index.sqlite"); err != nil {
		return err
	}
	info, err := r.Lstat("index.sqlite")
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return invalid("index.sqlite must be a regular file")
	}
	// Only settings are persisted. Relocation cannot redirect content or locks.
	saved := struct {
		ModelID        string   `json:"model_id"`
		ModelSHA256    string   `json:"model_sha256"`
		ModelSignature string   `json:"model_signature"`
		RuntimeID      string   `json:"runtime_id"`
		Timezone       string   `json:"timezone"`
		Editor         []string `json:"editor"`
	}{cfg.ModelID, cfg.ModelSHA256, cfg.ModelSignature, cfg.RuntimeID, cfg.Timezone, cfg.Editor}
	raw, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	name := fmt.Sprintf(".config-%x.tmp", randomID())
	f, err := r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer r.Remove(name)
	_, writeErr := f.Write(append(raw, '\n'))
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	if err := noSymlink(r, "config.json"); err != nil {
		return err
	}
	if replace {
		if err := r.Rename(name, "config.json"); err != nil {
			return err
		}
	} else {
		// A hard link atomically publishes complete, already-synced bytes only
		// if the name is absent. An existence check followed by rename could
		// clobber a winner; O_EXCL directly on config.json exposes partial JSON.
		if err := r.Link(name, "config.json"); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := r.Remove(name); err != nil {
			return err
		}
	}
	dir, err := r.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func layout(root string) Config {
	return Config{Root: root, Content: filepath.Join(root, "content"), Database: filepath.Join(root, "index.sqlite"), RunDir: filepath.Join(root, "run"), ModelsDir: filepath.Join(root, "models"), RuntimeDir: filepath.Join(root, "runtime")}
}

func validate(cfg Config) error {
	if _, err := time.LoadLocation(cfg.Timezone); err != nil {
		return invalid("invalid timezone")
	}
	if len(cfg.Editor) == 0 || cfg.Editor[0] == "" {
		return invalid("editor must be a nonempty argv array")
	}
	return nil
}

func invalid(message string) error { return &model.Error{Code: "invalid_argument", Message: message} }

func noSymlink(r *os.Root, name string) error {
	info, err := r.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return &model.Error{Code: "outside_root", Path: name, Message: "symlinks are not allowed"}
	}
	return nil
}

// canonical resolves even an uninitialized root by resolving its nearest
// existing ancestor. A dangling symlink is not mistaken for a missing directory.
func canonical(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if info, statErr := os.Lstat(path); statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", err
			}
			// Another initializer may have created this real path after the
			// failed EvalSymlinks. Retry instead of returning its stale ENOENT.
			continue
		}
		suffix = append(suffix, filepath.Base(path))
		path = filepath.Dir(path)
	}
}

func randomID() []byte {
	b := make([]byte, 16)
	// crypto/rand.Read is guaranteed to fill b or terminate the program.
	_, _ = rand.Read(b)
	return b
}
