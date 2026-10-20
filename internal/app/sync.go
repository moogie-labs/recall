package app

import (
	"context"

	"recall/internal/model"
)

func (a *App) Sync(ctx context.Context) (model.SyncReport, error) {
	var report model.SyncReport
	err := a.store.WithLock(ctx, func() error {
		var err error
		_, report, err = a.syncLocked(ctx)
		return err
	})
	return report, err
}

// syncLocked never acquires the store lock. Apply consumes the ENTIRE snapshot,
// while the caller continues to own the same lock used for source mutation.
func (a *App) syncLocked(ctx context.Context) (model.Snapshot, model.SyncReport, error) {
	snapshot, err := a.scanner.Reconcile(ctx)
	if err != nil {
		return snapshot, model.SyncReport{}, err
	}
	report, err := a.index.Apply(ctx, snapshot)
	return snapshot, report, err
}

// mutate is the single source transaction boundary for document and folder
// operations. Reconcile before editing, but do not let an unavailable index
// prevent the authoritative source write. A failed post-write Apply is explicit;
// it must never roll back, remove, or resurrect source files.
func (a *App) mutate(ctx context.Context, change func(model.Snapshot) (model.Document, error)) (model.Document, error) {
	var result model.Document
	err := a.store.WithLock(ctx, func() error {
		snapshot, err := a.scanner.Reconcile(ctx)
		if err != nil {
			return err
		}
		result, err = change(snapshot)
		if err != nil {
			return err
		}
		_, _, err = a.syncLocked(ctx)
		return err
	})
	return result, err
}
