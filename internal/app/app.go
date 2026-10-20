// Package app exposes shared, source-backed collection operations. Inference
// assets are deliberately not opened by the application service constructor.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"recall/internal/config"
	"recall/internal/embedding"
	"recall/internal/fsstore"
	"recall/internal/index"
	"recall/internal/model"
	"recall/internal/scanner"
	"recall/internal/search"
)

type App struct {
	store     *fsstore.Store
	scanner   *scanner.Scanner
	index     *index.Store
	search    *search.Engine
	now       func() time.Time
	newID     func() string
	closeOnce sync.Once
	closeErr  error
}

func Open(ctx context.Context, cfg config.Config) (*App, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store, err := fsstore.Open(cfg)
	if err != nil {
		return nil, err
	}
	a := &App{store: store, now: time.Now, newID: randomID}
	a.scanner = scanner.New(store, func() time.Time { return a.now() }, func() string { return a.newID() })
	err = store.WithLock(ctx, func() error {
		// Never trust caller-supplied layout paths to redirect the derived index.
		filename := filepath.Join(cfg.Root, "index.sqlite")
		info, err := os.Lstat(filename)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return appError("outside_root", "index.sqlite", "symlinks are not allowed")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		a.index, err = index.Open(filename)
		if err != nil {
			return err
		}
		_, _, err = a.syncLocked(ctx)
		return err
	})
	if err != nil {
		a.Close()
		return nil, err
	}
	// Both constructors are lazy: CRUD/keyword use never loads inference assets.
	a.search = search.New(search.NewStoreSource(store), a.index, embedding.NewClient(cfg))
	return a, nil
}

func (a *App) Close() error {
	a.closeOnce.Do(func() {
		if a.index != nil {
			a.closeErr = a.index.Close()
		}
		a.closeErr = errors.Join(a.closeErr, a.store.Close())
	})
	return a.closeErr
}

func randomID() string {
	var id [16]byte
	rand.Read(id[:])
	return hex.EncodeToString(id[:])
}

func appError(code, path, message string) error {
	return &model.Error{Code: code, Path: path, Message: message}
}

// Validate before joining paths: filepath.Join would erase traversal evidence.
// The store remains the descriptor-based, no-follow confinement boundary.
func validatePath(path string) error {
	if path == "" || path == "." || filepath.IsAbs(path) {
		return appError("outside_root", path, "a path beneath content is required")
	}
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".." {
			return appError("outside_root", path, "parent traversal is not allowed")
		}
		if part == "" || part == "." || strings.ContainsRune(part, 0) {
			return appError("invalid_argument", path, "invalid path component")
		}
	}
	return nil
}

func validateRef(ref model.Ref) error {
	if (ref.ID == "") == (ref.Path == "") {
		return appError("invalid_argument", "", "supply exactly one document ID or path")
	}
	if ref.Path != "" {
		return validatePath(ref.Path)
	}
	return nil
}
