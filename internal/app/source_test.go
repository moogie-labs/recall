package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSourceRequiresConfinedPath(t *testing.T) {
	a, cfg := newTestApp(t)
	defer a.Close()
	d := mustCreate(t, a, CreateInput{Name: "source", Body: "body"})
	source, err := a.ReadSource(t.Context(), d.Path)
	if err != nil || source.Raw != d.Raw || source.ID != d.ID || source.Revision != d.Revision {
		t.Fatal("source identity was not preserved", err)
	}
	for _, path := range []string{"../outside.md", "/absolute.md", "plain.md"} {
		if _, err := a.ReadSource(t.Context(), path); err == nil {
			t.Fatalf("unconfined/untyped source accepted %q", path)
		}
	}
	external := filepath.Join(t.TempDir(), "external.md")
	if err := os.WriteFile(external, []byte(d.Raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(cfg.Content, "link.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReadSource(t.Context(), "link.md"); err == nil {
		t.Fatal("source read followed symlink")
	}
}
