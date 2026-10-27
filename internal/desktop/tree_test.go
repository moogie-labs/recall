package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTreeHasManagedEntriesAndEmptyFoldersOnly(t *testing.T) {
	s, _, cfg, session := fixture(t)
	s.Create(CreateInput{Session: session, Kind: "folder", Name: "empty"})
	s.Create(CreateInput{Session: session, Kind: "note", Name: "one"})
	os.WriteFile(filepath.Join(cfg.Content, "ordinary.md"), []byte("ordinary"), 0600)
	r := s.Tree(session)
	if r.Problem != nil || len(r.Tree.Entries) != 2 {
		t.Fatalf("tree %+v", r)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), `"body"`) || strings.Contains(string(b), `"raw"`) {
		t.Fatal("full source leaked into discovery")
	}
}
func TestTreeUnicodeAndDuplicateBasenames(t *testing.T) {
	s, _, _, session := fixture(t)
	for _, name := range []string{"Résumé", "😀", "<b>"} {
		r := s.Create(CreateInput{Session: session, Kind: "folder", Name: name})
		if r.Problem != nil {
			t.Fatal(r.Problem)
		}
		s.Create(CreateInput{Session: session, Kind: "note", Parent: name, Name: "ideas"})
	}
	r := s.Tree(session)
	if r.Problem != nil || len(r.Tree.Entries) != 6 {
		t.Fatal(r)
	}
	ids := map[string]bool{}
	paths := map[string]bool{}
	for i, e := range r.Tree.Entries {
		if i < 3 && e.Kind != "folder" {
			t.Fatal("folders not first")
		}
		if ids[e.ID] || paths[e.Path] {
			t.Fatal("identity collision")
		}
		ids[e.ID] = true
		paths[e.Path] = true
	}
}
func TestCreateAndDelete(t *testing.T) {
	s, _, cfg, session := fixture(t)
	f := s.Create(CreateInput{Session: session, Kind: "folder", Name: "work"})
	if f.Problem != nil {
		t.Fatal(f.Problem)
	}
	d := s.Create(CreateInput{Session: session, Kind: "task", Parent: "work", Name: "task"})
	if d.Problem != nil || d.Document.Path != "work/task.task.md" || d.Document.State != "planned" {
		t.Fatal(d)
	}
	for _, in := range []CreateInput{{Kind: "task", Parent: "work", Name: "task"}, {Kind: "folder", Name: "../escape"}, {Kind: "note", Name: "a/b"}, {Kind: "folder", Name: "."}} {
		in.Session = session
		if s.Create(in).Problem == nil {
			t.Fatalf("invalid creation accepted %+v", in)
		}
	}
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(cfg.Content, "link"))
	if s.Create(CreateInput{Session: session, Kind: "note", Parent: "link", Name: "bad"}).Problem == nil {
		t.Fatal("symlink accepted")
	}
	in := DeleteInput{Session: session, ID: d.Document.ID, Path: d.Document.Path}
	if s.Delete(in).Problem == nil {
		t.Fatal("revisionless delete accepted")
	}
	in.ExpectedRevision = "stale"
	if s.Delete(in).Problem == nil {
		t.Fatal("stale delete accepted")
	}
	if s.Delete(DeleteInput{Session: session, ID: "folder:work", Path: "work"}).Problem == nil {
		t.Fatal("nonempty folder deleted without recursive flag")
	}
	if s.Delete(DeleteInput{Session: session, ID: "folder:.", Path: ".", Recursive: true}).Problem == nil {
		t.Fatal("root deletion")
	}
	in.ExpectedRevision = d.Document.Revision
	if s.Delete(in).Problem != nil {
		t.Fatal("valid delete rejected")
	}
	if s.Delete(DeleteInput{Session: session, ID: "folder:work", Path: "work"}).Problem != nil {
		t.Fatal("empty folder delete rejected")
	}
}
func TestDiagnosticsSurviveDiscovery(t *testing.T) {
	s, _, cfg, session := fixture(t)
	raw := "---\nid: [broken\n---\nbody"
	os.WriteFile(filepath.Join(cfg.Content, "bad.note.md"), []byte(raw), 0600)
	r := s.Tree(session)
	if r.Problem != nil || len(r.Tree.Diagnostics) != 1 || len(r.Tree.Entries) != 0 {
		t.Fatal(r)
	}
	b, _ := os.ReadFile(filepath.Join(cfg.Content, "bad.note.md"))
	if string(b) != raw {
		t.Fatal("malformed source repaired")
	}
}
