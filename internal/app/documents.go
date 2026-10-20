package app

import (
	"context"
	"path/filepath"
	"strings"

	"recall/internal/document"
	"recall/internal/model"
)

type CreateInput struct {
	Kind   model.Kind  `json:"kind"`
	Folder string      `json:"folder"`
	Name   string      `json:"name"`
	Title  string      `json:"title"`
	Body   string      `json:"body"`
	State  model.State `json:"state"`
}

func (a *App) Create(ctx context.Context, input CreateInput) (model.Document, error) {
	if input.Kind != model.Note && input.Kind != model.Task {
		return model.Document{}, appError("invalid_argument", "", "kind must be note or task")
	}
	if err := validatePath(input.Name); err != nil {
		return model.Document{}, err
	}
	if strings.ContainsRune(input.Name, filepath.Separator) {
		return model.Document{}, appError("invalid_argument", "", "name must be one filename component")
	}
	if input.Folder != "" && input.Folder != "." {
		if err := validatePath(input.Folder); err != nil {
			return model.Document{}, err
		}
	}
	if input.Kind == model.Note && input.State != "" {
		return model.Document{}, appError("type_mismatch", "", "only tasks have states")
	}
	if input.State != "" && input.State != model.Planned && input.State != model.InProgress && input.State != model.Stalled && input.State != model.Completed {
		return model.Document{}, appError("invalid_argument", "", "state must be a supported task state")
	}
	path := filepath.Join(input.Folder, input.Name+"."+string(input.Kind)+".md")
	return a.mutate(ctx, func(snapshot model.Snapshot) (model.Document, error) {
		id := a.newID()
		if id == "" {
			return model.Document{}, appError("invalid_document", path, "generated ID must not be empty")
		}
		if _, err := a.resolve(ctx, snapshot, model.Ref{ID: id}); model.ErrorCode(err) != "not_found" {
			if err != nil && model.ErrorCode(err) != "duplicate_id" {
				return model.Document{}, err
			}
			return model.Document{}, appError("duplicate_id", path, "generated ID already exists")
		}
		title := input.Title
		if title == "" {
			title = input.Name
		}
		p := document.Parsed{Document: model.Document{ID: id, Path: path, Kind: input.Kind, Title: title, State: input.State}, Body: []byte(input.Body)}
		q, _, err := document.Normalize(p, a.now(), a.newID)
		if err != nil {
			return model.Document{}, err
		}
		if err := a.store.Write(ctx, path, "", []byte(q.Document.Raw)); err != nil {
			return model.Document{}, err
		}
		return q.Document, nil
	})
}

func (a *App) Read(ctx context.Context, ref model.Ref) (model.Document, error) {
	if err := validateRef(ref); err != nil {
		return model.Document{}, err
	}
	var result model.Document
	err := a.store.WithLock(ctx, func() error {
		snapshot, _, err := a.syncLocked(ctx)
		if err != nil {
			return err
		}
		result, err = a.resolve(ctx, snapshot, ref)
		return err
	})
	return result, err
}

func (a *App) Update(ctx context.Context, ref model.Ref, expectedRevision, raw string) (model.Document, error) {
	if expectedRevision == "" {
		return model.Document{}, appError("invalid_argument", "", "full-content updates require an expected revision")
	}
	return a.mutateDocument(ctx, ref, expectedRevision, func(current model.Document) (model.Document, error) {
		p, err := document.Parse(current.Path, []byte(raw))
		if err != nil {
			return model.Document{}, err
		}
		if p.Document.ID != current.ID {
			return model.Document{}, appError("invalid_argument", current.Path, "document ID cannot be changed or removed")
		}
		q, _, err := document.Normalize(p, a.now(), a.newID)
		if err != nil {
			return model.Document{}, err
		}
		if err := a.store.Write(ctx, current.Path, current.Revision, []byte(q.Document.Raw)); err != nil {
			return model.Document{}, err
		}
		return q.Document, nil
	})
}

// Move takes a complete content-relative, type-bearing destination filename.
func (a *App) Move(ctx context.Context, ref model.Ref, destination, expectedRevision string) (model.Document, error) {
	if err := validatePath(destination); err != nil {
		return model.Document{}, err
	}
	return a.mutateDocument(ctx, ref, expectedRevision, func(current model.Document) (model.Document, error) {
		kind, err := document.KindFromPath(destination)
		if err != nil {
			return model.Document{}, err
		}
		if kind != current.Kind {
			return model.Document{}, appError("type_mismatch", destination, "destination filename must preserve the document kind")
		}
		if err := a.store.Move(ctx, current.Path, destination); err != nil {
			return model.Document{}, err
		}
		p, err := document.Parse(destination, []byte(current.Raw))
		return p.Document, err
	})
}

func (a *App) Delete(ctx context.Context, ref model.Ref, expectedRevision string) error {
	_, err := a.mutateDocument(ctx, ref, expectedRevision, func(current model.Document) (model.Document, error) {
		return model.Document{}, a.store.Remove(ctx, current.Path, false)
	})
	return err
}

func (a *App) mutateDocument(ctx context.Context, ref model.Ref, expectedRevision string, change func(model.Document) (model.Document, error)) (model.Document, error) {
	if err := validateRef(ref); err != nil {
		return model.Document{}, err
	}
	return a.mutate(ctx, func(snapshot model.Snapshot) (model.Document, error) {
		current, err := a.resolve(ctx, snapshot, ref)
		if err != nil {
			return model.Document{}, err
		}
		if expectedRevision != "" && expectedRevision != current.Revision {
			return model.Document{}, appError("conflict", current.Path, "source revision changed")
		}
		return change(current)
	})
}

// Resolve only an explicit, unambiguous reference, then reread the real source.
// Diagnostic paths cannot masquerade as indexed, valid documents. Duplicate IDs
// are absent from the scanner's documents, so inspect those diagnostic sources
// as well instead of silently choosing one (or reporting a false not_found).
func (a *App) resolve(ctx context.Context, snapshot model.Snapshot, ref model.Ref) (model.Document, error) {
	for _, diagnostic := range snapshot.Diagnostics {
		if ref.Path == diagnostic.Path || ref.Path != "" && diagnostic.Code == "outside_root" && strings.HasPrefix(ref.Path, diagnostic.Path+"/") {
			return model.Document{}, appError(diagnostic.Code, diagnostic.Path, diagnostic.Message)
		}
		if ref.ID != "" && diagnostic.Code == "duplicate_id" {
			raw, err := a.store.Read(ctx, diagnostic.Path)
			if err != nil {
				return model.Document{}, err
			}
			p, err := document.Parse(diagnostic.Path, raw)
			if err != nil {
				return model.Document{}, err
			}
			if p.Document.ID == ref.ID {
				return model.Document{}, appError("duplicate_id", diagnostic.Path, "ID is used by multiple source files")
			}
		}
	}
	for _, d := range snapshot.Documents {
		if ref.ID != "" && ref.ID == d.ID || ref.Path != "" && ref.Path == d.Path {
			raw, err := a.store.Read(ctx, d.Path)
			if err != nil {
				return model.Document{}, err
			}
			p, err := document.Parse(d.Path, raw)
			if err != nil {
				return model.Document{}, err
			}
			if p.Document.ID != d.ID || p.Document.Revision != d.Revision {
				return model.Document{}, appError("conflict", d.Path, "source changed during reconciliation; retry")
			}
			return p.Document, nil
		}
	}
	return model.Document{}, appError("not_found", ref.Path, "document not found")
}
