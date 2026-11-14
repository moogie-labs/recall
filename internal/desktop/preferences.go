package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// FilePreferences stores desktop settings in <Directory>/desktop.json. Writes
// preserve keys they do not own, so the collection root and the update-check
// setting (and settings from other app versions) never erase each other.
type FilePreferences struct{ Directory string }

func DefaultPreferences() (FilePreferences, error) {
	dir, err := os.UserConfigDir()
	return FilePreferences{Directory: filepath.Join(dir, "recall")}, err
}

func (p FilePreferences) load() (map[string]json.RawMessage, error) {
	file := filepath.Join(p.Directory, "desktop.json")
	info, err := os.Lstat(file)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("desktop preferences must be a regular file")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	values := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("invalid desktop preferences: %w", err)
	}
	if values == nil {
		values = map[string]json.RawMessage{}
	}
	return values, nil
}

func (p FilePreferences) LoadRoot() (string, error) {
	values, err := p.load()
	if err != nil {
		return "", err
	}
	var root string
	if raw, ok := values["root"]; ok {
		if err := json.Unmarshal(raw, &root); err != nil {
			return "", fmt.Errorf("invalid desktop preferences: %w", err)
		}
	}
	if root != "" && !filepath.IsAbs(root) {
		return "", fmt.Errorf("saved collection root must be absolute")
	}
	return root, nil
}

func (p FilePreferences) SaveRoot(root string) error {
	if !filepath.IsAbs(root) {
		return fmt.Errorf("collection root must be absolute")
	}
	return p.save("root", root)
}

// LoadUpdateCheck reports whether to check for updates at launch. A missing
// setting means true; an unreadable file also returns true, with its error.
func (p FilePreferences) LoadUpdateCheck() (bool, error) {
	values, err := p.load()
	if err != nil {
		return true, err
	}
	enabled := true
	if raw, ok := values["checkForUpdates"]; ok {
		if err := json.Unmarshal(raw, &enabled); err != nil {
			return true, fmt.Errorf("invalid desktop preferences: %w", err)
		}
	}
	return enabled, nil
}

func (p FilePreferences) SaveUpdateCheck(enabled bool) error {
	return p.save("checkForUpdates", enabled)
}

// save replaces one key atomically. An unreadable file is replaced, as it was
// before preferences were merged, so choosing a collection can repair it.
func (p FilePreferences) save(key string, value any) error {
	values, err := p.load()
	if err != nil {
		values = map[string]json.RawMessage{}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	values[key] = raw
	data, err := json.Marshal(values)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.Directory, 0700); err != nil {
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
