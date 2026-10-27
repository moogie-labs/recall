package desktop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"recall/internal/app"
	"recall/internal/config"
	"recall/internal/model"
	"testing"
)

func TestRootPreferences(t *testing.T) {
	t.Setenv("RECALL_HOME", filepath.Join(t.TempDir(), "env"))
	prefs := FilePreferences{Directory: t.TempDir()}
	if root, err := prefs.LoadRoot(); err != nil || root != "" {
		t.Fatal("missing prefs must be empty", err)
	}
	saved := filepath.Join(t.TempDir(), "saved")
	if err := prefs.SaveRoot(saved); err != nil {
		t.Fatal(err)
	}
	loaded, err := prefs.LoadRoot()
	if err != nil || loaded != saved {
		t.Fatal(loaded, err)
	}
	info, _ := os.Stat(filepath.Join(prefs.Directory, "desktop.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("preferences permissions")
	}
	var roots []string
	s := NewService(func(_ context.Context, c config.Config) (Application, error) {
		roots = append(roots, c.Root)
		return nil, &os.PathError{Op: "test-open", Path: c.Root, Err: os.ErrNotExist}
	}, prefs)
	s.OpenCollection("")
	s.SetInitialRoot(filepath.Join(t.TempDir(), "flag"))
	s.OpenCollection("")
	explicit := filepath.Join(t.TempDir(), "picker")
	s.OpenCollection(explicit)
	canonicalSaved, _ := filepath.EvalSymlinks(filepath.Dir(saved))
	canonicalExplicit, _ := filepath.EvalSymlinks(filepath.Dir(explicit))
	if roots[0] != filepath.Join(canonicalSaved, "saved") || roots[1] == roots[0] || filepath.Base(roots[1]) != "flag" || roots[2] != filepath.Join(canonicalExplicit, "picker") {
		t.Fatal("precedence", roots)
	}
	os.WriteFile(filepath.Join(prefs.Directory, "desktop.json"), []byte("not JSON"), 0600)
	if _, err := prefs.LoadRoot(); err == nil {
		t.Fatal("invalid prefs silently ignored")
	}
}

type unwritablePreferences struct{}

func (unwritablePreferences) LoadRoot() (string, error) { return "", nil }
func (unwritablePreferences) SaveRoot(string) error {
	return errors.New("preferences directory is read-only")
}
func TestPreferenceFailurePreservesActiveCollection(t *testing.T) {
	s, a, _, session := fixture(t)
	d, err := a.Create(t.Context(), app.CreateInput{Kind: model.Note, Name: "keep", Body: "current"})
	if err != nil {
		t.Fatal(err)
	}
	next, err := config.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = config.Prepare(next); err != nil {
		t.Fatal(err)
	}
	s.preferences = unwritablePreferences{}
	s.open = func(ctx context.Context, cfg config.Config) (Application, error) { return app.Open(ctx, cfg) }
	reply := s.OpenCollection(next.Root)
	if reply.Problem == nil {
		t.Fatal("preference failure was hidden")
	}
	if current := s.OpenDocument(session, d.ID); current.Problem != nil || current.Document.Body != "current" {
		t.Fatal("preference failure invalidated active collection", current)
	}
}
