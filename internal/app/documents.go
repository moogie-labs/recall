package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"recall/internal/attachment"
	"recall/internal/document"
	"recall/internal/model"
)

type CreateInput struct {
	Folder string `json:"folder"`
	Name   string `json:"name"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}

func (a *App) Create(ctx context.Context, input CreateInput) (model.Document, error) {
	if err := validatePath(input.Name); err != nil {
		return model.Document{}, err
	}
	if strings.ContainsRune(input.Name, filepath.Separator) {
		return model.Document{}, appError("invalid_argument", "", "name must be one filename component")
	}
	if strings.HasSuffix(input.Name, document.Suffix) {
		return model.Document{}, appError("invalid_argument", "", "name must not include the .md suffix")
	}
	if input.Folder != "" && input.Folder != "." {
		if err := validatePath(input.Folder); err != nil {
			return model.Document{}, err
		}
	}
	path := filepath.Join(input.Folder, input.Name+document.Suffix)
	if err := reserved(path); err != nil {
		return model.Document{}, err
	}
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
		p := document.Parsed{Document: model.Document{ID: id, Path: path, Title: title}, Body: []byte(input.Body)}
		q, _, err := document.Normalize(p, a.newID)
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
		q, _, err := document.Normalize(p, a.newID)
		if err != nil {
			return model.Document{}, err
		}
		if err := a.store.Write(ctx, current.Path, current.Revision, []byte(q.Document.Raw)); err != nil {
			return model.Document{}, err
		}
		return q.Document, nil
	})
}

// Move takes a complete content-relative .md destination filename. The note's
// attachment folder moves with it; on a rename its body links are repointed.
func (a *App) Move(ctx context.Context, ref model.Ref, destination, expectedRevision string) (model.Document, error) {
	if err := validatePath(destination); err != nil {
		return model.Document{}, err
	}
	if err := reserved(destination); err != nil {
		return model.Document{}, err
	}
	return a.mutateDocument(ctx, ref, expectedRevision, func(current model.Document) (model.Document, error) {
		if err := document.ValidatePath(destination); err != nil {
			return model.Document{}, err
		}
		// Undo moves must run even if the request was cancelled mid-move.
		undoCtx := context.WithoutCancel(ctx)
		from, to := attachment.Folder(current.Path), attachment.Folder(destination)
		carry, err := a.attachmentsToCarry(ctx, from, to)
		if err != nil {
			return model.Document{}, err
		}
		if err := a.store.Move(ctx, current.Path, destination); err != nil {
			return model.Document{}, err
		}
		raw := []byte(current.Raw)
		if carry {
			if err := a.store.Move(ctx, from, to); err != nil {
				// Put the note back rather than separate it from its images.
				return model.Document{}, errors.Join(err, a.store.Move(undoCtx, destination, current.Path))
			}
			// Past this point a failure undoes both moves, newest first.
			undo := func(err error) error {
				folderBack := a.store.Move(undoCtx, to, from)
				return errors.Join(err, folderBack, a.store.Move(undoCtx, destination, current.Path))
			}
			rewritten, changed, err := rewriteAttachmentLinks(destination, raw, filepath.Base(from), filepath.Base(to))
			if err != nil {
				return model.Document{}, undo(err)
			}
			if changed {
				if err := a.store.Write(ctx, destination, current.Revision, rewritten); err != nil {
					return model.Document{}, undo(err)
				}
				raw = rewritten
			}
		}
		p, err := document.Parse(destination, raw)
		return p.Document, err
	})
}

func (a *App) Delete(ctx context.Context, ref model.Ref, expectedRevision string) error {
	if err := validateRef(ref); err != nil {
		return err
	}
	var partial error
	_, err := a.mutate(ctx, func(snapshot model.Snapshot) (model.Document, error) {
		var current model.Document
		var err error
		if ref.Path != "" {
			current, err = a.explicitSource(ctx, ref.Path)
		} else {
			current, err = a.resolve(ctx, snapshot, ref)
		}
		if err != nil {
			return model.Document{}, err
		}
		if expectedRevision != "" && expectedRevision != current.Revision {
			return model.Document{}, appError("conflict", current.Path, "source revision changed")
		}
		if err := a.store.Remove(ctx, current.Path, false); err != nil {
			return model.Document{}, err
		}
		partial = a.removeAttachments(ctx, current.Path)
		return model.Document{}, nil
	})
	if err != nil {
		return err
	}
	return partial
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
