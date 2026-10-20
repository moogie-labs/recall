package app

import (
	"context"
	"crypto/sha256"
	"fmt"

	"recall/internal/document"
	"recall/internal/model"
)

// EditorPath permits repairing malformed metadata only through an explicit
// recognized typed path. IDs still require an unambiguous valid projection.
func (a *App) EditorPath(ctx context.Context, ref model.Ref) (string, error) {
	if err := validateRef(ref); err != nil {
		return "", err
	}
	var path string
	err := a.store.WithLock(ctx, func() error {
		if ref.Path != "" {
			if _, err := a.typedSource(ctx, ref.Path); err != nil {
				return err
			}
			path = ref.Path
		} else {
			snapshot, _, err := a.syncLocked(ctx)
			if err != nil {
				return err
			}
			d, err := a.resolve(ctx, snapshot, ref)
			if err != nil {
				return err
			}
			path = d.Path
		}
		var err error
		path, err = a.store.SourcePath(ctx, path)
		return err
	})
	return path, err
}

// This is not metadata adoption: return just enough source identity for a
// deliberate explicit-path deletion, including its actual raw-byte revision.
func (a *App) typedSource(ctx context.Context, path string) (model.Document, error) {
	if err := validatePath(path); err != nil {
		return model.Document{}, err
	}
	kind, err := document.KindFromPath(path)
	if err != nil {
		return model.Document{}, err
	}
	raw, err := a.store.Read(ctx, path)
	if err != nil {
		return model.Document{}, err
	}
	return model.Document{Path: path, Kind: kind, Revision: fmt.Sprintf("%x", sha256.Sum256(raw))}, nil
}
