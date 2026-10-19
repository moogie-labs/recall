// Package scanner reconciles filename-gated source documents. The caller MUST
// hold the store's WithLock across Reconcile and application of its snapshot to
// any derived index. Reconcile never acquires or releases that lock itself.
package scanner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"

	"recall/internal/document"
	"recall/internal/fsstore"
	"recall/internal/model"
)

type Scanner struct {
	store *fsstore.Store
	now   func() time.Time
	newID func() string
}

func New(store *fsstore.Store, now func() time.Time, newID func() string) *Scanner {
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = func() string {
			var id [16]byte
			// crypto/rand.Read fills the buffer or terminates on entropy failure.
			rand.Read(id[:])
			return hex.EncodeToString(id[:])
		}
	}
	return &Scanner{store: store, now: now, newID: newID}
}

// Reconcile returns only current valid, uniquely identified documents, together
// with actual folders (including ".") and diagnostics for unmanaged/invalid
// entries. A stale metadata write restarts discovery so duplicate IDs and edits
// in the latest bytes are revalidated. Continuous edits eventually return a
// conflict rather than overwrite source or hold the caller's lock indefinitely.
func (s *Scanner) Reconcile(ctx context.Context) (model.Snapshot, error) {
	for attempt := 0; attempt < 8; attempt++ {
		if err := ctx.Err(); err != nil {
			return model.Snapshot{}, err
		}
		snapshot, err := s.reconcile(ctx)
		if model.ErrorCode(err) == "conflict" || model.ErrorCode(err) == "not_found" {
			continue
		}
		return snapshot, err
	}
	return model.Snapshot{}, &model.Error{Code: "conflict", Message: "sources kept changing during reconciliation; retry sync"}
}

type candidate struct {
	parsed   document.Parsed
	revision string
	changed  bool
}

func (s *Scanner) reconcile(ctx context.Context) (model.Snapshot, error) {
	snapshot := model.Snapshot{Folders: []string{"."}}
	entries, err := s.store.Walk(ctx)
	if err != nil {
		return model.Snapshot{}, err
	}
	var parsed []document.Parsed
	ids := make(map[string]int)
	// Complete discovery and validation before assigning any IDs or writing.
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return model.Snapshot{}, err
		}
		if entry.IsSymlink {
			snapshot.Diagnostics = append(snapshot.Diagnostics, model.Diagnostic{Code: "outside_root", Path: entry.Path, Message: "symlink is unmanaged and will not be followed"})
			continue
		}
		if entry.IsDir {
			snapshot.Folders = append(snapshot.Folders, entry.Path)
			continue
		}
		if _, err := document.KindFromPath(entry.Path); err != nil {
			snapshot.Diagnostics = append(snapshot.Diagnostics, diagnostic(entry.Path, err))
			continue
		}
		raw, err := s.store.Read(ctx, entry.Path)
		if err != nil {
			var sourceError *model.Error
			if model.ErrorCode(err) == "not_found" || !errors.As(err, &sourceError) {
				return model.Snapshot{}, err
			}
			snapshot.Diagnostics = append(snapshot.Diagnostics, diagnostic(entry.Path, err))
			continue
		}
		p, err := document.Parse(entry.Path, raw)
		if err != nil {
			snapshot.Diagnostics = append(snapshot.Diagnostics, diagnostic(entry.Path, err))
			continue
		}
		parsed = append(parsed, p)
		if p.Document.ID != "" {
			ids[p.Document.ID]++
		}
	}
	var candidates []candidate
	now := s.now()
	for _, p := range parsed {
		if err := ctx.Err(); err != nil {
			return model.Snapshot{}, err
		}
		if ids[p.Document.ID] > 1 {
			snapshot.Diagnostics = append(snapshot.Diagnostics, duplicate(p.Document.Path))
			continue
		}
		q, changed, err := document.Normalize(p, now, s.newID)
		if err != nil {
			snapshot.Diagnostics = append(snapshot.Diagnostics, diagnostic(p.Document.Path, err))
			continue
		}
		if p.Document.ID == "" {
			ids[q.Document.ID]++
		}
		candidates = append(candidates, candidate{parsed: q, revision: p.Document.Revision, changed: changed})
	}
	// Also guard injected/generated ID collisions before persisting either file.
	for _, c := range candidates {
		d := c.parsed.Document
		if ids[d.ID] > 1 {
			snapshot.Diagnostics = append(snapshot.Diagnostics, duplicate(d.Path))
			continue
		}
		if c.changed {
			if err := s.store.Write(ctx, d.Path, c.revision, []byte(d.Raw)); err != nil {
				return model.Snapshot{}, err
			}
		}
		snapshot.Documents = append(snapshot.Documents, d)
	}
	slices.Sort(snapshot.Folders)
	slices.SortFunc(snapshot.Diagnostics, func(a, b model.Diagnostic) int { return strings.Compare(a.Path, b.Path) })
	return snapshot, nil
}

func duplicate(path string) model.Diagnostic {
	return model.Diagnostic{Code: "duplicate_id", Path: path, Message: "ID is used by multiple source files; assign distinct IDs explicitly"}
}

func diagnostic(path string, err error) model.Diagnostic {
	d := model.Diagnostic{Code: model.ErrorCode(err), Path: path, Message: "source could not be reconciled"}
	var sourceError *model.Error
	if errors.As(err, &sourceError) {
		d.Message = sourceError.Message
	}
	return d
}
