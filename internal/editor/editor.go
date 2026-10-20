// Package editor builds external-editor handoffs for the actual source file.
// It never exports database content or invokes a shell.
package editor

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"

	"recall/internal/model"
)

// Command appends absolutePath as one argument to a copy of the configured argv.
// The caller must resolve a managed document through the application/store and
// join its confined path to the configured content root; an absolute path alone
// is not a confinement check. Command does not create, copy, or write the file.
// The caller owns terminal attachment, process execution, and cancellation.
func Command(argv []string, absolutePath string) (*exec.Cmd, error) {
	if len(argv) == 0 || argv[0] == "" || !filepath.IsAbs(absolutePath) || strings.ContainsRune(absolutePath, 0) {
		return nil, invalid()
	}
	for _, arg := range argv {
		if strings.ContainsRune(arg, 0) {
			return nil, invalid()
		}
	}
	args := make([]string, len(argv)+1)
	copy(args, argv)
	args[len(argv)] = absolutePath
	return exec.Command(args[0], args[1:]...), nil
}

// Complete is called after Run/Wait or a TUI's external-process completion
// callback. A failing editor can still have saved content, so always reconcile,
// preserving both errors. The callback should wrap App.Sync, not replay an old
// document snapshot. Cancellation is passed through; a canceled sync leaves the
// saved source for startup/periodic recovery rather than overwriting it.
func Complete(ctx context.Context, exitErr error, reconcile func(context.Context) error) error {
	if reconcile == nil {
		return errors.Join(exitErr, invalid())
	}
	return errors.Join(exitErr, reconcile(ctx))
}

func invalid() error {
	return &model.Error{Code: "invalid_argument", Message: "editor argv, absolute source path, and reconciliation callback are required"}
}
