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

var scanTime = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

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

func TestFilenameGatedAdoption(t *testing.T) {
	s, cfg := newCollection(t)
	unmanaged := "---\ntype: note\n---\nold café\r\n"
	saveExternal(t, cfg, "old.md", unmanaged)
	saveExternal(t, cfg, "old.note.md", "old café\r\n---\r\n")
	scanner := New(s, func() time.Time { return scanTime }, func() string { return "fixed-id" })
	got := reconcileLocked(t, s, scanner)
	if len(got.Documents) != 1 || got.Documents[0].Path != "old.note.md" || got.Documents[0].ID != "fixed-id" || got.Documents[0].Body != "old café\r\n---\r\n" {
		t.Fatalf("adoption: %+v", got)
	}
	if readSource(t, cfg, "old.md") != unmanaged {
		t.Fatal("untyped file was rewritten")
	}
	if len(got.Diagnostics) != 1 || got.Diagnostics[0].Path != "old.md" {
		t.Fatalf("unmanaged file is invisible: %+v", got.Diagnostics)
	}
	assertProjection(t, cfg, got.Documents[0])
	first := readSource(t, cfg, "old.note.md")
	again := reconcileLocked(t, s, scanner)
	if readSource(t, cfg, "old.note.md") != first || !reflect.DeepEqual(got, again) {
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

func TestDirectSavedStateChangeAppendsOnlyOneObservedEvent(t *testing.T) {
	s, cfg := newCollection(t)
	saveExternal(t, cfg, "work.task.md", "description\n")
	scanner := New(s, func() time.Time { return scanTime }, func() string { return "fixed-id" })
	initial := reconcileLocked(t, s, scanner)
	if len(initial.Documents) != 1 || initial.Documents[0].State != model.Planned || len(initial.Documents[0].History) != 1 || !initial.Documents[0].History[0].At.Equal(scanTime) {
		t.Fatalf("initial task: %+v", initial)
	}
	raw := readSource(t, cfg, "work.task.md")
	raw = strings.Replace(raw, "state: planned", "state: completed", 1)
	saveExternal(t, cfg, "work.task.md", raw)
	got := reconcileLocked(t, s, scanner)
	if len(got.Documents) != 1 || got.Documents[0].State != model.Completed || len(got.Documents[0].History) != 2 || got.Documents[0].History[1].State != model.Completed || !got.Documents[0].History[1].At.Equal(scanTime) {
		t.Fatalf("observed transition: %+v", got)
	}
	assertProjection(t, cfg, got.Documents[0])
	again := reconcileLocked(t, s, scanner)
	if !reflect.DeepEqual(got, again) {
		t.Fatalf("repeat appended an event: %+v", again)
	}
}

func TestRapidSavesKeepFinalBody(t *testing.T) {
	s, cfg := newCollection(t)
	scanner := New(s, func() time.Time { return scanTime }, func() string { return "fixed-id" })
	saveExternal(t, cfg, "work.task.md", "---\nid: fixed-id\ntype: task\nstate: planned\nstate_history: [{state: planned, at: 2026-10-17T10:00:00Z}]\n---\ninitial\n")
	reconcileLocked(t, s, scanner)
	for _, state := range []string{"in_progress", "stalled", "completed"} {
		saveExternal(t, cfg, "work.task.md", "---\nid: fixed-id\ntype: task\nstate: "+state+"\ncustom: {latest: café}\nstate_history: [{state: planned, at: 2026-10-17T10:00:00Z, extra: keep}]\n---\nfinal "+state+"\r\n---\r\n")
	}
	got := reconcileLocked(t, s, scanner)
	if len(got.Documents) != 1 || got.Documents[0].Body != "final completed\r\n---\r\n" || got.Documents[0].State != model.Completed || len(got.Documents[0].History) != 2 {
		t.Fatalf("latest save lost: %+v", got)
	}
	raw := readSource(t, cfg, "work.task.md")
	if !strings.Contains(raw, "latest: café") || !strings.Contains(raw, "extra: keep") {
		t.Fatalf("unrelated metadata lost: %q", raw)
	}
	assertProjection(t, cfg, got.Documents[0])
}

func TestMalformedHistoryIsNotReplaced(t *testing.T) {
	s, cfg := newCollection(t)
	scanner := New(s, func() time.Time { return scanTime }, func() string { return "fixed-id" })
	saveExternal(t, cfg, "work.task.md", "description\n")
	reconcileLocked(t, s, scanner)
	bad := "---\nstate: completed\nstate_history: [{state: planned, at: SECRET}]\ncustom: keep\n---\nlatest body\n"
	saveExternal(t, cfg, "work.task.md", bad)
	got := reconcileLocked(t, s, scanner)
	if len(got.Documents) != 0 || len(got.Diagnostics) != 1 || got.Diagnostics[0].Code != "invalid_document" || got.Diagnostics[0].Path != "work.task.md" || strings.Contains(got.Diagnostics[0].Message, "SECRET") {
		t.Fatalf("bad history not diagnosed: %+v", got)
	}
	if readSource(t, cfg, "work.task.md") != bad {
		t.Fatal("malformed history overwritten with saved or invented history")
	}
}

func TestDuplicateIDsDoNotRewriteEitherFile(t *testing.T) {
	s, cfg := newCollection(t)
	a := "---\nid: duplicate\n---\nA\n"
	b := "---\nid: duplicate\nstate: completed\n---\nB\n"
	saveExternal(t, cfg, "a.note.md", a)
	saveExternal(t, cfg, "b.task.md", b)
	saveExternal(t, cfg, "c.note.md", "C\n")
	scanner := New(s, func() time.Time { return scanTime }, func() string {
		if readSource(t, cfg, "a.note.md") != a || readSource(t, cfg, "b.task.md") != b {
			t.Fatal("duplicate source changed before ID assignment")
		}
		return "new-id"
	})
	got := reconcileLocked(t, s, scanner)
	if len(got.Documents) != 1 || got.Documents[0].Path != "c.note.md" || len(got.Diagnostics) != 2 {
		t.Fatalf("duplicates must be excluded, not reassigned: %+v", got)
	}
	for i, path := range []string{"a.note.md", "b.task.md"} {
		if got.Diagnostics[i].Code != "duplicate_id" || got.Diagnostics[i].Path != path {
			t.Fatalf("duplicate diagnostic: %+v", got.Diagnostics)
		}
	}
	if readSource(t, cfg, "a.note.md") != a || readSource(t, cfg, "b.task.md") != b {
		t.Fatal("duplicate source rewritten")
	}
}

func TestEditDuringNormalizeRetriesLatestBytes(t *testing.T) {
	s, cfg := newCollection(t)
	saveExternal(t, cfg, "work.task.md", "stale body\n")
	edited := false
	scanner := New(s, func() time.Time { return scanTime }, func() string {
		if !edited {
			edited = true
			saveExternal(t, cfg, "work.task.md", "---\nstate: completed\ncustom: latest\nstate_history: [{state: stalled, at: 2026-10-17T10:00:00Z}]\n---\nlatest body café\r\n---\r\n")
		}
		return "fixed-id"
	})
	got := reconcileLocked(t, s, scanner)
	if len(got.Documents) != 1 || got.Documents[0].Body != "latest body café\r\n---\r\n" || got.Documents[0].State != model.Completed || len(got.Documents[0].History) != 2 || got.Documents[0].History[0].State != model.Stalled {
		t.Fatalf("stale normalization overwrote editor save: %+v", got)
	}
	if !strings.Contains(readSource(t, cfg, "work.task.md"), "custom: latest") {
		t.Fatal("latest unknown metadata lost")
	}
	assertProjection(t, cfg, got.Documents[0])
	again := reconcileLocked(t, s, scanner)
	if !reflect.DeepEqual(got, again) {
		t.Fatalf("retry produced duplicate history: %+v", again)
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
		return s.Write(ctx, "nested/a.note.md", "", []byte("nested body"))
	}); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.note.md")
	if err := os.WriteFile(victim, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cfg.Content, "link-folder")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(cfg.Content, "link.note.md")); err != nil {
		t.Fatal(err)
	}
	saveExternal(t, cfg, "mismatch.task.md", "---\ntype: note\n---\nkeep\n")
	scanner := New(s, func() time.Time { return scanTime }, func() string { return "fixed-id" })
	got := reconcileLocked(t, s, scanner)
	if !reflect.DeepEqual(got.Folders, []string{".", "empty folder", "nested", "nested/empty"}) || len(got.Documents) != 1 || got.Documents[0].Path != "nested/a.note.md" {
		t.Fatalf("actual folder discovery: %+v", got)
	}
	if len(got.Diagnostics) != 3 || got.Diagnostics[0].Code != "outside_root" || got.Diagnostics[1].Code != "outside_root" || got.Diagnostics[2].Code != "type_mismatch" {
		t.Fatalf("unsafe/mismatched sources invisible: %+v", got.Diagnostics)
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
	scanner := New(s, func() time.Time { return scanTime }, func() string { return "fixed-id" })
	var got model.Snapshot
	err := s.WithLock(ctx, func() error {
		if err := s.Mkdir(ctx, "folder"); err != nil {
			return err
		}
		if err := s.Write(ctx, "folder/work.task.md", "", []byte("body")); err != nil {
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
	if err != nil || len(got.Documents) != 1 || got.Documents[0].State != model.Planned {
		t.Fatalf("caller-owned lock integration: %+v %v", got, err)
	}
}

func TestDeleteDuringNormalizeNeverResurrectsSource(t *testing.T) {
	s, cfg := newCollection(t)
	saveExternal(t, cfg, "old.note.md", "old body")
	scanner := New(s, func() time.Time { return scanTime }, func() string {
		if err := os.Remove(filepath.Join(cfg.Content, "old.note.md")); err != nil {
			t.Fatal(err)
		}
		return "fixed-id"
	})
	got := reconcileLocked(t, s, scanner)
	if len(got.Documents) != 0 {
		t.Fatalf("deleted source retained: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(cfg.Content, "old.note.md")); !os.IsNotExist(err) {
		t.Fatalf("deleted source resurrected: %v", err)
	}
}

func TestReconcileDefaultIDAndCancellation(t *testing.T) {
	s, cfg := newCollection(t)
	saveExternal(t, cfg, "old.note.md", "body")
	scanner := New(s, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.WithLock(context.Background(), func() error {
		_, err := scanner.Reconcile(ctx)
		return err
	})
	if !errors.Is(err, context.Canceled) || readSource(t, cfg, "old.note.md") != "body" {
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
			saveExternal(t, cfg, "old.note.md", "---\n"+tt.metadata+"---\ncafé\r\n---\r\n")
			scanner := New(s, func() time.Time { return scanTime }, func() string { return "fixed-id" })
			got := reconcileLocked(t, s, scanner)
			if len(got.Documents) != 1 || len(got.Diagnostics) != 0 || got.Documents[0].ID != "fixed-id" || got.Documents[0].Body != "café\r\n---\r\n" {
				t.Fatalf("valid YAML blocked adoption: %+v", got)
			}
			p, err := document.Parse("old.note.md", []byte(readSource(t, cfg, "old.note.md")))
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
	raw := "---\r\nid : 'existing'\r\ntype: task\r\nstate: stalled\r\nstate_history: [ {state: stalled, at: 2026-10-17T10:00:00+02:00} ]\r\ncustom: [  one, two  ]\r\n---\r\n"
	saveExternal(t, cfg, "work.task.md", raw)
	before, err := os.Stat(filepath.Join(cfg.Content, "work.task.md"))
	if err != nil {
		t.Fatal(err)
	}
	scanner := New(s, func() time.Time { return scanTime }, func() string { t.Fatal("existing ID replaced"); return "" })
	got := reconcileLocked(t, s, scanner)
	after, err := os.Stat(filepath.Join(cfg.Content, "work.task.md"))
	if err != nil || !os.SameFile(before, after) || readSource(t, cfg, "work.task.md") != raw || len(got.Documents) != 1 {
		t.Fatalf("unchanged file rewritten: %+v %v", got, err)
	}
	assertProjection(t, cfg, got.Documents[0])
}

func TestManuallyReplacedHistoryRemainsAuthoritative(t *testing.T) {
	s, cfg := newCollection(t)
	scanner := New(s, func() time.Time { return scanTime }, func() string { return "fixed-id" })
	saveExternal(t, cfg, "work.task.md", "description\n")
	reconcileLocked(t, s, scanner)
	saveExternal(t, cfg, "work.task.md", "---\nid: fixed-id\ntype: task\nstate_history: [{state: completed, at: 2026-10-16T11:00:00Z, custom: revised}]\n---\nrevised body")
	got := reconcileLocked(t, s, scanner)
	if len(got.Documents) != 1 || got.Documents[0].State != model.Completed || len(got.Documents[0].History) != 1 || !got.Documents[0].History[0].At.Equal(time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)) || got.Documents[0].Body != "revised body" {
		t.Fatalf("old history restored: %+v", got)
	}
}

func TestGeneratedIDCollisionsDoNotRewriteSources(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			s, cfg := newCollection(t)
			a := "body A"
			if existing {
				a = "---\nid: fixed-id\n---\nbody A"
			}
			saveExternal(t, cfg, "a.note.md", a)
			saveExternal(t, cfg, "b.note.md", "body B")
			scanner := New(s, func() time.Time { return scanTime }, func() string { return "fixed-id" })
			got := reconcileLocked(t, s, scanner)
			if len(got.Documents) != 0 || len(got.Diagnostics) != 2 || readSource(t, cfg, "a.note.md") != a || readSource(t, cfg, "b.note.md") != "body B" {
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
	saveExternal(t, cfg, "old.note.md", "initial")
	saves := 0
	scanner := New(s, func() time.Time { return scanTime }, func() string {
		saves++
		saveExternal(t, cfg, "old.note.md", fmt.Sprintf("editor save %d", saves))
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
	if model.ErrorCode(err) != "conflict" || saves < 2 || len(got.Documents) != 0 || readSource(t, cfg, "old.note.md") != fmt.Sprintf("editor save %d", saves) {
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
	saveExternal(t, cfg, "work.task.md", "---\ntype: task\nid: existing\nstate: completed\nstate_history: [{state: planned, at: 2026-10-17T10:00:00Z}]\n---\nbody\n")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := make(chan struct{})
	errCh := make(chan error, 2)
	for _, store := range []*fsstore.Store{s, other} {
		go func() {
			<-start
			scanner := New(store, func() time.Time { return scanTime }, func() string { return "unused" })
			errCh <- store.WithLock(ctx, func() error {
				snapshot, err := scanner.Reconcile(ctx)
				if err != nil {
					return err
				}
				if len(snapshot.Documents) != 1 || len(snapshot.Documents[0].History) != 2 {
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
	p, err := document.Parse("work.task.md", []byte(readSource(t, cfg, "work.task.md")))
	if err != nil || len(p.Document.History) != 2 || p.Document.History[1].State != model.Completed {
		t.Fatalf("concurrent scan lost/duplicated history: %+v %v", p.Document, err)
	}
}
