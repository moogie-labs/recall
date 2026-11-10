package scanner

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"recall/internal/config"
	"recall/internal/document"
	"recall/internal/fsstore"
	"recall/internal/model"
)

func newCollection(t *testing.T) (*fsstore.Store, config.Config) {
	t.Helper()
	cfg, err := config.Resolve(filepath.Join(t.TempDir(), "Recall collection with spaces"))
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	s, err := fsstore.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s, cfg
}

func saveExternal(t *testing.T, cfg config.Config, path, raw string) {
	t.Helper()
	// Real atomic replacement models an editor that does not take Recall's lock.
	name := filepath.Join(cfg.Content, path)
	tmp, err := os.CreateTemp(filepath.Dir(name), ".editor-*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmp.WriteString(raw); err != nil {
		t.Fatal(err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp.Name(), name); err != nil {
		t.Fatal(err)
	}
}

func reconcileLocked(t *testing.T, store *fsstore.Store, scanner *Scanner) model.Snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var got model.Snapshot
	err := store.WithLock(ctx, func() error {
		var err error
		got, err = scanner.Reconcile(ctx)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func readSource(t *testing.T, cfg config.Config, path string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(cfg.Content, path))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestMarkdownAdoptionAndUnmanagedFiles(t *testing.T) {
	s, cfg := newCollection(t)
	unmanaged := "---\ntype: note\n---\nold café\r\n"
	saveExternal(t, cfg, "old.txt", unmanaged)
	saveExternal(t, cfg, "other.MD", unmanaged)
	saveExternal(t, cfg, "old.md", "old café\r\n---\r\n")
	scanner := New(s, func() string { return "fixed-id" })
	got := reconcileLocked(t, s, scanner)
	if len(got.Documents) != 1 || got.Documents[0].Path != "old.md" || got.Documents[0].ID != "fixed-id" || got.Documents[0].Body != "old café\r\n---\r\n" {
		t.Fatalf("adoption: %+v", got)
	}
	for _, path := range []string{"old.txt", "other.MD"} {
		if readSource(t, cfg, path) != unmanaged {
			t.Fatalf("%s was rewritten", path)
		}
	}
	if len(got.Diagnostics) != 2 || got.Diagnostics[0].Path != "old.txt" || got.Diagnostics[1].Path != "other.MD" {
		t.Fatalf("unmanaged file is invisible: %+v", got.Diagnostics)
	}
	assertProjection(t, cfg, got.Documents[0])
	first := readSource(t, cfg, "old.md")
	again := reconcileLocked(t, s, scanner)
	if readSource(t, cfg, "old.md") != first || !reflect.DeepEqual(got, again) {
		t.Fatalf("repeat scan changed source/snapshot: %+v", again)
	}
}

func assertProjection(t *testing.T, cfg config.Config, d model.Document) {
	t.Helper()
	raw := readSource(t, cfg, d.Path)
	if d.Raw != raw || d.Revision != fmt.Sprintf("%x", sha256.Sum256([]byte(raw))) {
		t.Fatalf("stale raw/revision: %+v", d)
	}
	p, err := document.Parse(d.Path, []byte(raw))
	if err != nil || !reflect.DeepEqual(p.Document, d) {
		t.Fatalf("snapshot not projected from saved source: %+v %v", d, err)
	}
}

func TestRapidSavesKeepFinalBody(t *testing.T) {
	s, cfg := newCollection(t)
	scanner := New(s, func() string { return "fixed-id" })
	saveExternal(t, cfg, "work.md", "---\nid: fixed-id\n---\ninitial\n")
	reconcileLocked(t, s, scanner)
	for _, save := range []string{"one", "two", "completed"} {
		saveExternal(t, cfg, "work.md", "---\nid: fixed-id\ncustom: {latest: café}\n---\nfinal "+save+"\r\n---\r\n")
	}
	got := reconcileLocked(t, s, scanner)
	if len(got.Documents) != 1 || got.Documents[0].Body != "final completed\r\n---\r\n" {
		t.Fatalf("latest save lost: %+v", got)
	}
	raw := readSource(t, cfg, "work.md")
	if !strings.Contains(raw, "latest: café") {
		t.Fatalf("unrelated metadata lost: %q", raw)
	}
	assertProjection(t, cfg, got.Documents[0])
}

func TestLeftoverDottedSuffixesAreOrdinaryNotes(t *testing.T) {
	s, cfg := newCollection(t)
	sources := map[string]string{
		"x.note.md":    "---\nid: left-note\ntype: note\n---\nnote body\n",
		"work.task.md": "---\nid: left-task\ntype: task\nstate: planned\n---\ntask body\n",
	}
	for path, raw := range sources {
		saveExternal(t, cfg, path, raw)
	}
	got := reconcileLocked(t, s, New(s, func() string { t.Fatal("existing ID replaced"); return "" }))
	if len(got.Documents) != 2 || len(got.Diagnostics) != 0 {
		t.Fatalf("leftovers: %+v", got)
	}
	titles := map[string]string{"x.note.md": "x.note", "work.task.md": "work.task"}
	for _, d := range got.Documents {
		if d.Title != titles[d.Path] || readSource(t, cfg, d.Path) != sources[d.Path] {
			t.Fatalf("%s: title %q source %q", d.Path, d.Title, readSource(t, cfg, d.Path))
		}
	}
}

func TestHiddenPathsAreDiagnosedAndNeverAdopted(t *testing.T) {
	s, cfg := newCollection(t)
	paths := []string{".hidden.md", ".git/README.md", ".obsidian/sub/x.md", "visible/.trash/y.md"}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(cfg.Content, path)), 0700); err != nil {
			t.Fatal(err)
		}
		saveExternal(t, cfg, path, "no frontmatter\n")
	}
	got := reconcileLocked(t, s, New(s, func() string { t.Fatal("hidden file adopted"); return "" }))
	if len(got.Documents) != 0 || len(got.Diagnostics) != len(paths) {
		t.Fatalf("hidden paths: %+v", got)
	}
	for _, d := range got.Diagnostics {
		if d.Code != "invalid_document" {
			t.Fatalf("diagnostic: %+v", d)
		}
	}
	for _, path := range paths {
		if readSource(t, cfg, path) != "no frontmatter\n" {
			t.Fatalf("%s rewritten", path)
		}
	}
}

func TestMalformedFrontmatterInPlainMarkdownIsNeverRewritten(t *testing.T) {
	for name, raw := range map[string]string{
		"unterminated": "---\nsome prose after a rule\n",
		"bad-yaml":     "---\ntitle: [unclosed\n---\nSECRET body\n",
		"scalar":       "---\nA horizontal rule then prose\n---\nMore prose\n",
		"bad-id":       "---\nid: 42\n---\nbody\n",
		"bad-title":    "---\ntitle: {a: b}\n---\nbody\n",
		"dup-key":      "---\na: 1\na: 2\n---\nbody\n",
	} {
		t.Run(name, func(t *testing.T) {
			s, cfg := newCollection(t)
			saveExternal(t, cfg, "plain.md", raw)
			got := reconcileLocked(t, s, New(s, func() string { t.Fatal("malformed file adopted"); return "" }))
			if len(got.Documents) != 0 || len(got.Diagnostics) != 1 || got.Diagnostics[0].Path != "plain.md" || got.Diagnostics[0].Code != "invalid_document" || strings.Contains(got.Diagnostics[0].Message, "SECRET") {
				t.Fatalf("malformed file: %+v", got)
			}
			if readSource(t, cfg, "plain.md") != raw {
				t.Fatal("malformed file rewritten")
			}
		})
	}
}

func TestDuplicateIDsDoNotRewriteEitherFile(t *testing.T) {
	s, cfg := newCollection(t)
	a := "---\nid: duplicate\n---\nA\n"
	b := "---\nid: duplicate\nstate: completed\n---\nB\n"
	saveExternal(t, cfg, "a.md", a)
	saveExternal(t, cfg, "b.md", b)
	saveExternal(t, cfg, "c.md", "C\n")
	scanner := New(s, func() string {
		if readSource(t, cfg, "a.md") != a || readSource(t, cfg, "b.md") != b {
			t.Fatal("duplicate source changed before ID assignment")
		}
		return "new-id"
	})
	got := reconcileLocked(t, s, scanner)
	if len(got.Documents) != 1 || got.Documents[0].Path != "c.md" || len(got.Diagnostics) != 2 {
		t.Fatalf("duplicates must be excluded, not reassigned: %+v", got)
	}
	for i, path := range []string{"a.md", "b.md"} {
		if got.Diagnostics[i].Code != "duplicate_id" || got.Diagnostics[i].Path != path {
			t.Fatalf("duplicate diagnostic: %+v", got.Diagnostics)
		}
	}
	if readSource(t, cfg, "a.md") != a || readSource(t, cfg, "b.md") != b {
		t.Fatal("duplicate source rewritten")
	}
}

func TestEditDuringNormalizeRetriesLatestBytes(t *testing.T) {
	s, cfg := newCollection(t)
	saveExternal(t, cfg, "work.md", "stale body\n")
	edited := false
	scanner := New(s, func() string {
		if !edited {
			edited = true
			saveExternal(t, cfg, "work.md", "---\ncustom: latest\n---\nlatest body café\r\n---\r\n")
		}
		return "fixed-id"
	})
	got := reconcileLocked(t, s, scanner)
	if len(got.Documents) != 1 || got.Documents[0].Body != "latest body café\r\n---\r\n" {
		t.Fatalf("stale normalization overwrote editor save: %+v", got)
	}
	if !strings.Contains(readSource(t, cfg, "work.md"), "custom: latest") {
		t.Fatal("latest unknown metadata lost")
	}
	assertProjection(t, cfg, got.Documents[0])
	again := reconcileLocked(t, s, scanner)
	if !reflect.DeepEqual(got, again) {
		t.Fatalf("retry changed the snapshot: %+v", again)
	}
}

func TestReconcileEnumeratesEmptyFoldersAndRejectsSymlinks(t *testing.T) {
	s, cfg := newCollection(t)
	ctx := context.Background()
	if err := s.WithLock(ctx, func() error {
		for _, folder := range []string{"empty folder", "nested", "nested/empty"} {
			if err := s.Mkdir(ctx, folder); err != nil {
				return err
			}
		}
		return s.Write(ctx, "nested/a.md", "", []byte("nested body"))
	}); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.md")
	if err := os.WriteFile(victim, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cfg.Content, "link-folder")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(cfg.Content, "link.md")); err != nil {
		t.Fatal(err)
	}
	saveExternal(t, cfg, "malformed.md", "---\nid: [broken\n---\nkeep\n")
	scanner := New(s, func() string { return "fixed-id" })
	got := reconcileLocked(t, s, scanner)
	if !reflect.DeepEqual(got.Folders, []string{".", "empty folder", "nested", "nested/empty"}) || len(got.Documents) != 1 || got.Documents[0].Path != "nested/a.md" {
		t.Fatalf("actual folder discovery: %+v", got)
	}
	if len(got.Diagnostics) != 3 || got.Diagnostics[0].Code != "outside_root" || got.Diagnostics[1].Code != "outside_root" || got.Diagnostics[2].Code != "invalid_document" {
		t.Fatalf("unsafe/invalid sources invisible: %+v", got.Diagnostics)
	}
	raw, err := os.ReadFile(victim)
	if err != nil || string(raw) != "external" {
		t.Fatalf("symlink victim changed: %q %v", raw, err)
	}
}

func TestReconcileUsesCallerOwnedLockAcrossMutationAndScan(t *testing.T) {
	s, _ := newCollection(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	scanner := New(s, func() string { return "fixed-id" })
	var got model.Snapshot
	err := s.WithLock(ctx, func() error {
		if err := s.Mkdir(ctx, "folder"); err != nil {
			return err
		}
		if err := s.Write(ctx, "folder/work.md", "", []byte("body")); err != nil {
			return err
		}
		var err error
		got, err = scanner.Reconcile(ctx)
		if err != nil {
			return err
		}
		// The scanner must neither reacquire nor release the caller's lock.
		contender, stop := context.WithTimeout(ctx, 30*time.Millisecond)
		defer stop()
		if err := s.WithLock(contender, func() error { return errors.New("lock released early") }); !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("outer transaction lock not retained: %w", err)
		}
		return nil
	})
	if err != nil || len(got.Documents) != 1 {
		t.Fatalf("caller-owned lock integration: %+v %v", got, err)
	}
}

func TestDeleteDuringNormalizeNeverResurrectsSource(t *testing.T) {
	s, cfg := newCollection(t)
	saveExternal(t, cfg, "old.md", "old body")
	scanner := New(s, func() string {
		if err := os.Remove(filepath.Join(cfg.Content, "old.md")); err != nil {
			t.Fatal(err)
		}
		return "fixed-id"
	})
	got := reconcileLocked(t, s, scanner)
	if len(got.Documents) != 0 {
		t.Fatalf("deleted source retained: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(cfg.Content, "old.md")); !os.IsNotExist(err) {
		t.Fatalf("deleted source resurrected: %v", err)
	}
}

func TestReconcileDefaultIDAndCancellation(t *testing.T) {
	s, cfg := newCollection(t)
	saveExternal(t, cfg, "old.md", "body")
	scanner := New(s, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.WithLock(context.Background(), func() error {
		_, err := scanner.Reconcile(ctx)
		return err
	})
	if !errors.Is(err, context.Canceled) || readSource(t, cfg, "old.md") != "body" {
		t.Fatalf("cancellation mutated source: %v", err)
	}
	got := reconcileLocked(t, s, scanner)
	if len(got.Documents) != 1 || len(got.Documents[0].ID) != 32 || strings.Trim(got.Documents[0].ID, "0123456789abcdef") != "" {
		t.Fatalf("default ID is not 16 random bytes in lowercase hex: %+v", got)
	}
}

func TestAdoptionPreservesYAMLReferencesAndTimestampTypes(t *testing.T) {
	for _, tt := range []struct {
		name, metadata, tag string
	}{
		{"alias", "id: &identity ''\ncustom: *identity\n", ""},
		{"implicit timestamp", "custom: {at: 2026-10-17T10:00:00Z}\n", "!!timestamp"},
		{"explicit timestamp", "custom: {at: !!timestamp '2026-10-17T10:00:00Z'}\n", "!!timestamp"},
		{"quoted string", "custom: {at: '2026-10-17T10:00:00Z'}\n", "!!str"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, cfg := newCollection(t)
			saveExternal(t, cfg, "old.md", "---\n"+tt.metadata+"---\ncafé\r\n---\r\n")
			scanner := New(s, func() string { return "fixed-id" })
			got := reconcileLocked(t, s, scanner)
			if len(got.Documents) != 1 || len(got.Diagnostics) != 0 || got.Documents[0].ID != "fixed-id" || got.Documents[0].Body != "café\r\n---\r\n" {
				t.Fatalf("valid YAML blocked adoption: %+v", got)
			}
			p, err := document.Parse("old.md", []byte(readSource(t, cfg, "old.md")))
			if err != nil {
				t.Fatal(err)
			}
			var id, custom *yaml.Node
			for i := 0; i < len(p.Metadata.Content); i += 2 {
				switch p.Metadata.Content[i].Value {
				case "id":
					id = p.Metadata.Content[i+1]
				case "custom":
					custom = p.Metadata.Content[i+1]
				}
			}
			if custom == nil {
				t.Fatal("unknown metadata was removed")
			}
			if tt.name == "alias" {
				if custom.Kind != yaml.AliasNode || custom.Value != "identity" || custom.Alias != id || id.Anchor != "identity" {
					t.Fatalf("alias reference lost: %+v", custom)
				}
			} else if custom.Kind != yaml.MappingNode || len(custom.Content) != 2 || custom.Content[1].Tag != tt.tag || custom.Content[1].Value != "2026-10-17T10:00:00Z" {
				t.Fatalf("unrelated timestamp semantics changed: %+v", custom)
			}
			assertProjection(t, cfg, got.Documents[0])
			again := reconcileLocked(t, s, scanner)
			if !reflect.DeepEqual(got, again) {
				t.Fatalf("repeat adoption changed source: %+v", again)
			}
		})
	}
}

func TestUnchangedManagedSourceIsNotRewritten(t *testing.T) {
	s, cfg := newCollection(t)
	raw := "---\r\nid : 'existing'\r\nstate: stalled\r\nstate_history: [ {state: stalled, at: 2026-10-17T10:00:00+02:00} ]\r\ncustom: [  one, two  ]\r\n---\r\n"
	saveExternal(t, cfg, "work.md", raw)
	before, err := os.Stat(filepath.Join(cfg.Content, "work.md"))
	if err != nil {
		t.Fatal(err)
	}
	scanner := New(s, func() string { t.Fatal("existing ID replaced"); return "" })
	got := reconcileLocked(t, s, scanner)
	after, err := os.Stat(filepath.Join(cfg.Content, "work.md"))
	if err != nil || !os.SameFile(before, after) || readSource(t, cfg, "work.md") != raw || len(got.Documents) != 1 {
		t.Fatalf("unchanged file rewritten: %+v %v", got, err)
	}
	assertProjection(t, cfg, got.Documents[0])
}

func TestGeneratedIDCollisionsDoNotRewriteSources(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			s, cfg := newCollection(t)
			a := "body A"
			if existing {
				a = "---\nid: fixed-id\n---\nbody A"
			}
			saveExternal(t, cfg, "a.md", a)
			saveExternal(t, cfg, "b.md", "body B")
			scanner := New(s, func() string { return "fixed-id" })
			got := reconcileLocked(t, s, scanner)
			if len(got.Documents) != 0 || len(got.Diagnostics) != 2 || readSource(t, cfg, "a.md") != a || readSource(t, cfg, "b.md") != "body B" {
				t.Fatalf("ID collision persisted: %+v", got)
			}
			for _, d := range got.Diagnostics {
				if d.Code != "duplicate_id" {
					t.Fatalf("collision diagnostic: %+v", d)
				}
			}
		})
	}
}

func TestContinuousEditsReturnConflictWithoutOverwritingLatestSave(t *testing.T) {
	s, cfg := newCollection(t)
	saveExternal(t, cfg, "old.md", "initial")
	saves := 0
	scanner := New(s, func() string {
		saves++
		saveExternal(t, cfg, "old.md", fmt.Sprintf("editor save %d", saves))
		return "fixed-id"
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var got model.Snapshot
	err := s.WithLock(ctx, func() error {
		var err error
		got, err = scanner.Reconcile(ctx)
		return err
	})
	if model.ErrorCode(err) != "conflict" || saves < 2 || len(got.Documents) != 0 || readSource(t, cfg, "old.md") != fmt.Sprintf("editor save %d", saves) {
		t.Fatalf("continuous edits were overwritten or retry was unbounded: %+v saves=%d err=%v", got, saves, err)
	}
}

func TestConcurrentCallerLockedScansAppendOneEvent(t *testing.T) {
	s, cfg := newCollection(t)
	other, err := fsstore.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := other.Close(); err != nil {
			t.Error(err)
		}
	}()
	saveExternal(t, cfg, "work.md", "body\n")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := make(chan struct{})
	errCh := make(chan error, 2)
	for _, store := range []*fsstore.Store{s, other} {
		go func() {
			<-start
			scanner := New(store, func() string { return "existing" })
			errCh <- store.WithLock(ctx, func() error {
				snapshot, err := scanner.Reconcile(ctx)
				if err != nil {
					return err
				}
				if len(snapshot.Documents) != 1 || snapshot.Documents[0].ID != "existing" {
					return fmt.Errorf("unexpected serialized scan: %+v", snapshot)
				}
				return nil
			})
		}()
	}
	close(start)
	for range 2 {
		if err := <-errCh; err != nil {
			t.Error(err)
		}
	}
	p, err := document.Parse("work.md", []byte(readSource(t, cfg, "work.md")))
	if err != nil || p.Document.ID != "existing" {
		t.Fatalf("concurrent scan lost the assigned ID: %+v %v", p.Document, err)
	}
}
