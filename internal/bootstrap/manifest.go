// Package bootstrap prepares metadata and verifies pinned model/runtime assets.
// Asset readiness does not imply that inference has been tested successfully.
package bootstrap

import (
	"encoding/hex"
	"encoding/json"
	"path/filepath"

	"recall/assets"
	"recall/internal/model"
)

type Manifest struct {
	ID             string `json:"id"`
	URL            string `json:"url"`
	SHA256         string `json:"sha256"`
	Filename       string `json:"filename"`
	License        string `json:"license"`
	Size           int64  `json:"size"`
	InstallationID string `json:"installation_id,omitempty"`
	// TreeSHA256 pins the extracted runtime payload (runtimetree.Digest).
	TreeSHA256 string `json:"tree_sha256,omitempty"`
}

func pinnedManifests() (modelManifest, runtimeManifest Manifest, err error) {
	if err = json.Unmarshal([]byte(assets.ModelLock()), &modelManifest); err != nil {
		return
	}
	if err = json.Unmarshal([]byte(assets.RuntimeLock()), &runtimeManifest); err != nil {
		return
	}
	for _, m := range []Manifest{modelManifest, runtimeManifest} {
		if err = validateManifest(m); err != nil {
			return
		}
		if !singleName(m.Filename) || !singleName(m.ID) || m.License == "" {
			err = invalidAsset("invalid embedded asset identity")
			return
		}
	}
	if runtimeManifest.TreeSHA256 == "" {
		err = invalidAsset("embedded runtime lock must pin its extracted payload digest")
	}
	return
}

func validateManifest(m Manifest) error {
	digest, err := hex.DecodeString(m.SHA256)
	if err != nil || len(digest) != 32 || m.Size <= 0 || m.Size == int64(^uint64(0)>>1) {
		return invalidAsset("asset requires a positive size and SHA-256 checksum")
	}
	if m.TreeSHA256 != "" {
		if tree, err := hex.DecodeString(m.TreeSHA256); err != nil || len(tree) != 32 {
			return invalidAsset("runtime payload digest must be a SHA-256 checksum")
		}
	}
	if m.InstallationID != "" {
		if _, err := assets.RuntimeInstallationDirectory(m.ID, m.SHA256, m.InstallationID); err != nil {
			return invalidAsset(err.Error())
		}
	}
	return nil
}

func singleName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && filepath.IsLocal(name)
}

func invalidAsset(message string) error {
	return &model.Error{Code: "asset_invalid", Message: message}
}

func unavailable(message string) error {
	return &model.Error{Code: "runtime_unavailable", Message: message}
}
