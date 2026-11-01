package config

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"

	"golang.org/x/sys/unix"
)

// DefaultEditor is used when the per-user settings file selects no editor.
var DefaultEditor = []string{"nvim"}

// UserSettingsPath is Recall's per-user settings file, beside the desktop
// preferences: $HOME/Library/Application Support/recall/config.json on macOS.
func UserSettingsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(dir) {
		return "", invalid("user configuration directory must be absolute")
	}
	return filepath.Join(dir, "recall", "config.json"), nil
}

// UserEditor returns the external-editor argv from per-user settings. It is
// deliberately never read from a collection's config.json: a collection may be
// shared, synced or cloned from someone else, and must not be able to choose a
// program for `recall document edit` to execute.
func UserEditor() ([]string, error) {
	path, err := UserSettingsPath()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return slices.Clone(DefaultEditor), nil
	}
	if errors.Is(err, unix.ELOOP) {
		return nil, invalid(path + " must not be a symlink")
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil {
		return nil, err
	} else if !info.Mode().IsRegular() {
		return nil, invalid(path + " must be a regular file")
	}
	var saved struct {
		Editor []string `json:"editor"`
	}
	decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
	if err := decoder.Decode(&saved); err != nil {
		return nil, invalid("invalid " + path)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, invalid("invalid " + path)
	}
	if saved.Editor == nil {
		return slices.Clone(DefaultEditor), nil
	}
	if len(saved.Editor) == 0 || saved.Editor[0] == "" {
		return nil, invalid("editor in " + path + " must be a nonempty argv array")
	}
	return saved.Editor, nil
}
