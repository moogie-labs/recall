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
	os.WriteFile(filepath.Join(cfg.Content, "ordinary.txt"), []byte("ordinary"), 0600)
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
	d := s.Create(CreateInput{Session: session, Kind: "note", Parent: "work", Name: "item"})
	if d.Problem != nil || d.Document.Path != "work/item.md" || d.Entry.Kind != "note" {
		t.Fatal(d)
	}
	for _, in := range []CreateInput{{Kind: "note", Parent: "work", Name: "item"}, {Kind: "task", Parent: "work", Name: "other"}, {Kind: "folder", Name: "../escape"}, {Kind: "note", Name: "a/b"}, {Kind: "folder", Name: "."}} {
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
	os.WriteFile(filepath.Join(cfg.Content, "bad.md"), []byte(raw), 0600)
	r := s.Tree(session)
	if r.Problem != nil || len(r.Tree.Diagnostics) != 1 || len(r.Tree.Entries) != 0 {
		t.Fatal(r)
	}
	b, _ := os.ReadFile(filepath.Join(cfg.Content, "bad.md"))
	if string(b) != raw {
		t.Fatal("malformed source repaired")
	}
}

func TestLeftoverDottedSuffixIsPartOfTheName(t *testing.T) {
	s, _, cfg, session := fixture(t)
	os.WriteFile(filepath.Join(cfg.Content, "x.note.md"), []byte("---\nid: left\n---\nbody\n"), 0600)
	r := s.Tree(session)
	if r.Problem != nil || len(r.Tree.Entries) != 1 || r.Tree.Entries[0].Name != "x.note" || r.Tree.Entries[0].Kind != "note" {
		t.Fatalf("tree %+v", r)
	}
}

func TestMoveNoteIntoFolderKeepsIDAndName(t *testing.T) {
	s, _, cfg, session := fixture(t)
	s.Create(CreateInput{Session: session, Kind: "folder", Name: "archive"})
	d := s.Create(CreateInput{Session: session, Kind: "note", Name: "idea"})
	r := s.Move(MoveInput{Session: session, ID: d.Document.ID, Path: d.Document.Path, Folder: "archive", ExpectedRevision: d.Document.Revision})
	if r.Problem != nil || r.Entry == nil || r.Entry.Path != "archive/idea.md" || r.Entry.ID != d.Document.ID || r.Document == nil || r.Document.Path != "archive/idea.md" {
		t.Fatalf("move %+v", r)
	}
	if _, err := os.Stat(filepath.Join(cfg.Content, "archive", "idea.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Content, "idea.md")); !os.IsNotExist(err) {
		t.Fatal("source left behind")
	}
}
func TestMoveNoteToRootWithoutRevision(t *testing.T) {
	s, _, _, session := fixture(t)
	s.Create(CreateInput{Session: session, Kind: "folder", Name: "inbox"})
	d := s.Create(CreateInput{Session: session, Kind: "note", Parent: "inbox", Name: "idea"})
	r := s.Move(MoveInput{Session: session, ID: d.Document.ID, Path: d.Document.Path, Folder: ""})
	if r.Problem != nil || r.Entry.Path != "idea.md" {
		t.Fatalf("move %+v", r)
	}
}
func TestMoveNoteRejectsStaleRevisionAndCollision(t *testing.T) {
	s, _, _, session := fixture(t)
	s.Create(CreateInput{Session: session, Kind: "folder", Name: "archive"})
	s.Create(CreateInput{Session: session, Kind: "note", Parent: "archive", Name: "idea"})
	d := s.Create(CreateInput{Session: session, Kind: "note", Name: "idea"})
	stale := s.Move(MoveInput{Session: session, ID: d.Document.ID, Path: d.Document.Path, Folder: "", ExpectedRevision: "stale"})
	if stale.Problem == nil || stale.Problem.Code != "conflict" {
		t.Fatalf("stale move %+v", stale)
	}
	clash := s.Move(MoveInput{Session: session, ID: d.Document.ID, Path: d.Document.Path, Folder: "archive", ExpectedRevision: d.Document.Revision})
	if clash.Problem == nil || clash.Problem.Code != "already_exists" {
		t.Fatalf("colliding move %+v", clash)
	}
}
func TestMoveFolderWithContents(t *testing.T) {
	s, _, cfg, session := fixture(t)
	s.Create(CreateInput{Session: session, Kind: "folder", Name: "archive"})
	s.Create(CreateInput{Session: session, Kind: "folder", Name: "work"})
	s.Create(CreateInput{Session: session, Kind: "note", Parent: "work", Name: "item"})
	r := s.Move(MoveInput{Session: session, ID: "folder:work", Path: "work", Folder: "archive"})
	if r.Problem != nil || r.Entry == nil || r.Entry.ID != "folder:archive/work" || r.Entry.Path != "archive/work" || r.Entry.Kind != "folder" || r.Document != nil {
		t.Fatalf("move %+v", r)
	}
	if _, err := os.Stat(filepath.Join(cfg.Content, "archive", "work", "item.md")); err != nil {
		t.Fatal(err)
	}
	back := s.Move(MoveInput{Session: session, ID: "folder:archive/work", Path: "archive/work", Folder: ""})
	if back.Problem != nil || back.Entry.Path != "work" {
		t.Fatalf("move to root %+v", back)
	}
}
func TestMoveFolderRejectsInvalidTargets(t *testing.T) {
	s, _, _, session := fixture(t)
	s.Create(CreateInput{Session: session, Kind: "folder", Name: "work"})
	s.Create(CreateInput{Session: session, Kind: "folder", Parent: "work", Name: "sub"})
	for _, in := range []MoveInput{
		{ID: "folder:work", Path: "work", Folder: "work"},
		{ID: "folder:work", Path: "work", Folder: "work/sub"},
		{ID: "folder:work", Path: "other", Folder: ""},
		{ID: "folder:.", Path: ".", Folder: "work"},
		{ID: "folder:work", Path: "work", Folder: "../escape"},
		{ID: "folder:work/sub", Path: "work/sub", Folder: "work"},
	} {
		in.Session = session
		if s.Move(in).Problem == nil {
			t.Fatalf("invalid move accepted %+v", in)
		}
	}
	if s.Move(MoveInput{Session: "stale", ID: "folder:work", Path: "work", Folder: ""}).Problem == nil {
		t.Fatal("stale session accepted")
	}
}
