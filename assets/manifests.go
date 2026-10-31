// Package assets embeds the release locks. On-disk manifests are never trusted
// to select downloadable or executable assets.
package assets

import (
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
)

//go:embed model.lock.json
var modelLock string

//go:embed runtime.lock.json
var runtimeLock string

// ModelLock returns an immutable copy of the compiled-in model lock.
func ModelLock() string { return modelLock }

// RuntimeLock returns an immutable copy of the compiled-in runtime lock.
func RuntimeLock() string { return runtimeLock }

// RuntimeInstallationDirectory keeps logical runtime identity stable while
// allowing differently signed, pinned releases to coexist without replacement.
// Legacy locks without an installation ID continue to use their original path.
func RuntimeInstallationDirectory(id, checksum, installationID string) (string, error) {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id || !filepath.IsLocal(id) {
		return "", errors.New("invalid runtime identity")
	}
	if installationID == "" {
		return id, nil
	}
	digest, err := hex.DecodeString(checksum)
	if err != nil || len(digest) != 32 || installationID != id+"-"+checksum {
		return "", errors.New("runtime installation identity must match pinned archive checksum")
	}
	return installationID, nil
}

// RuntimeDirectory selects the directory from the immutable embedded runtime lock.
func RuntimeDirectory() (string, error) {
	var m struct {
		ID             string `json:"id"`
		SHA256         string `json:"sha256"`
		InstallationID string `json:"installation_id"`
	}
	if err := json.Unmarshal([]byte(runtimeLock), &m); err != nil {
		return "", err
	}
	return RuntimeInstallationDirectory(m.ID, m.SHA256, m.InstallationID)
}
