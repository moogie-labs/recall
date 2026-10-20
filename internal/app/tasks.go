package app

import (
	"context"

	"recall/internal/document"
	"recall/internal/model"
	"recall/internal/tasks"
)

func (a *App) SetState(ctx context.Context, ref model.Ref, state model.State, expectedRevision string) (model.Document, error) {
	return a.mutateDocument(ctx, ref, expectedRevision, func(current model.Document) (model.Document, error) {
		p, err := document.Parse(current.Path, []byte(current.Raw))
		if err != nil {
			return model.Document{}, err
		}
		q, changed, err := tasks.Transition(p, state, a.now())
		if err != nil {
			return model.Document{}, err
		}
		if changed {
			if err := a.store.Write(ctx, current.Path, current.Revision, []byte(q.Document.Raw)); err != nil {
				return model.Document{}, err
			}
		}
		return q.Document, nil
	})
}

func (a *App) Tasks(ctx context.Context, query model.TaskQuery) (model.Page[model.Document], error) {
	var page model.Page[model.Document]
	err := a.store.WithLock(ctx, func() error {
		if _, _, err := a.syncLocked(ctx); err != nil {
			return err
		}
		var err error
		page, err = a.index.Tasks(ctx, query)
		return err
	})
	return page, err
}
