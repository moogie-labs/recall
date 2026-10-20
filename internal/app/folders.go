package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"recall/internal/model"
)

func (a *App) CreateFolder(ctx context.Context, path string) error {
	if err := validatePath(path); err != nil {
		return err
	}
	_, err := a.mutate(ctx, func(model.Snapshot) (model.Document, error) { return model.Document{}, a.store.Mkdir(ctx, path) })
	return err
}

func (a *App) MoveFolder(ctx context.Context, from, to string) error {
	if err := validatePath(from); err != nil {
		return err
	}
	if err := validatePath(to); err != nil {
		return err
	}
	if strings.HasPrefix(to, from+"/") {
		return appError("invalid_argument", to, "cannot move a folder into itself")
	}
	_, err := a.mutate(ctx, func(snapshot model.Snapshot) (model.Document, error) {
		if err := requireFolder(snapshot, from); err != nil {
			return model.Document{}, err
		}
		return model.Document{}, a.store.Move(ctx, from, to)
	})
	return err
}

func (a *App) DeleteFolder(ctx context.Context, path string, recursive bool) error {
	if err := validatePath(path); err != nil {
		return err
	}
	_, err := a.mutate(ctx, func(snapshot model.Snapshot) (model.Document, error) {
		if err := requireFolder(snapshot, path); err != nil {
			return model.Document{}, err
		}
		return model.Document{}, a.store.Remove(ctx, path, recursive)
	})
	return err
}

func (a *App) Folders(ctx context.Context) ([]string, error) {
	var folders []string
	err := a.store.WithLock(ctx, func() error {
		snapshot, _, err := a.syncLocked(ctx)
		folders = snapshot.Folders
		return err
	})
	return folders, err
}

// List returns folder contents, including diagnostics and child folders. An
// empty folder means global; "." explicitly selects content-root children.
func (a *App) List(ctx context.Context, folder string, recursive bool) (model.Snapshot, error) {
	if folder != "" && folder != "." {
		if err := validatePath(folder); err != nil {
			return model.Snapshot{}, err
		}
	}
	var result model.Snapshot
	err := a.store.WithLock(ctx, func() error {
		snapshot, _, err := a.syncLocked(ctx)
		if err != nil {
			return err
		}
		if folder != "" {
			if err := requireFolder(snapshot, folder); err != nil {
				return err
			}
		}
		for _, d := range snapshot.Documents {
			if inFolder(d.Path, folder, recursive) {
				result.Documents = append(result.Documents, d)
			}
		}
		for _, d := range snapshot.Diagnostics {
			if inFolder(d.Path, folder, recursive) {
				result.Diagnostics = append(result.Diagnostics, d)
			}
		}
		for _, path := range snapshot.Folders {
			if folder == "" || path != "." && path != folder && inFolder(path, folder, recursive) {
				result.Folders = append(result.Folders, path)
			}
		}
		return nil
	})
	return result, err
}

func inFolder(path, folder string, recursive bool) bool {
	if folder == "" {
		return true
	}
	parent := filepath.Dir(path)
	return parent == folder || recursive && (folder == "." || strings.HasPrefix(parent, folder+"/"))
}

func requireFolder(snapshot model.Snapshot, path string) error {
	if slices.Contains(snapshot.Folders, path) {
		return nil
	}
	for _, d := range snapshot.Diagnostics {
		if d.Code == "outside_root" && (path == d.Path || strings.HasPrefix(path, d.Path+"/")) {
			return appError(d.Code, d.Path, d.Message)
		}
		if path == d.Path {
			return appError("invalid_argument", path, "a folder is required")
		}
	}
	for _, d := range snapshot.Documents {
		if path == d.Path {
			return appError("invalid_argument", path, "a folder is required")
		}
	}
	return appError("not_found", path, "folder not found")
}
