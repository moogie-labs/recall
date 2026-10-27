package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type FilePreferences struct{ Directory string }

func DefaultPreferences() (FilePreferences, error) {
	dir, err := os.UserConfigDir()
	return FilePreferences{Directory: filepath.Join(dir, "recall")}, err
}
func (p FilePreferences) LoadRoot() (string, error) {
	file := filepath.Join(p.Directory, "desktop.json")
	info, err := os.Lstat(file)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("desktop preferences must be a regular file")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	var saved struct {
		Root string `json:"root"`
	}
	if err = json.Unmarshal(data, &saved); err != nil {
		return "", fmt.Errorf("invalid desktop preferences: %w", err)
	}
	if saved.Root != "" && !filepath.IsAbs(saved.Root) {
		return "", fmt.Errorf("saved collection root must be absolute")
	}
	return saved.Root, nil
}
func (p FilePreferences) SaveRoot(root string) error {
	if !filepath.IsAbs(root) {
		return fmt.Errorf("collection root must be absolute")
	}
	if err := os.MkdirAll(p.Directory, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(struct {
		Root string `json:"root"`
	}{root})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(p.Directory, ".desktop-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(p.Directory, "desktop.json"))
}
