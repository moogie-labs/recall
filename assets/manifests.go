// Package assets embeds the release locks. On-disk manifests are never trusted
// to select downloadable or executable assets.
package assets

import _ "embed"

//go:embed model.lock.json
var modelLock string

//go:embed runtime.lock.json
var runtimeLock string

// ModelLock returns an immutable copy of the compiled-in model lock.
func ModelLock() string { return modelLock }

// RuntimeLock returns an immutable copy of the compiled-in runtime lock.
func RuntimeLock() string { return runtimeLock }
