package app

import (
	"context"

	"recall/internal/document"
	"recall/internal/model"
)

// ReadSource verifies an explicitly known typed source independently of the
// rebuildable index. It does not adopt metadata or resolve ambiguous IDs.
// Callers must compare its identity/revision with the operation being verified.
func (a *App) ReadSource(ctx context.Context, path string) (model.Document, error) {
	if err := validatePath(path); err != nil {
		return model.Document{}, err
	}
	var result model.Document
	err := a.store.WithLock(ctx, func() error {
		raw, err := a.store.Read(ctx, path)
		if err != nil {
			return err
		}
		parsed, err := document.Parse(path, raw)
		if err != nil {
			return err
		}
		result = parsed.Document
		return nil
	})
	return result, err
}
