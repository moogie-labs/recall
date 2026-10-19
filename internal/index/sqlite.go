// Package index maintains a disposable projection of authoritative source files.
package index

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"recall/internal/model"
)

type Store struct {
	db *sql.DB
}

// Open creates or opens only the derived database, not a Recall collection.
// Every connection gets the same pragmas, including any replacement connection.
func Open(filename string) (*Store, error) {
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return nil, unavailable(context.Background(), err)
	}
	u := url.URL{Scheme: "file", Path: absolute}
	q := u.Query()
	for _, pragma := range []string{"busy_timeout(5000)", "foreign_keys(1)", "journal_mode(WAL)"} {
		q.Add("_pragma", pragma)
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, unavailable(context.Background(), err)
	}
	// Queries use short read transactions; one connection per Store avoids
	// connection-local pragma drift and still permits independent processes.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db}
	if err := s.initialize(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) initialize() error {
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return unavailable(ctx, err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return unavailable(ctx, err)
	}
	defer conn.ExecContext(ctx, "ROLLBACK")
	var version int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return unavailable(ctx, err)
	}
	if version != 0 && version != schemaVersion {
		return &model.Error{Code: "index_unavailable", Message: "unsupported index schema version; rebuild the derived index"}
	}
	if version == 0 {
		if _, err := conn.ExecContext(ctx, schema); err != nil {
			return unavailable(ctx, err)
		}
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return unavailable(ctx, err)
}

func (s *Store) Close() error { return unavailable(context.Background(), s.db.Close()) }

// Apply replaces the complete current projection. The caller must hold its
// collection lock across scanning and Apply: SQL serialization cannot detect an
// older source snapshot. Documents are already normalized/validated by scanner.
// Metadata, raw bytes and history are caches only; no source is edited here.
func (s *Store) Apply(ctx context.Context, snapshot model.Snapshot) (model.SyncReport, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return model.SyncReport{}, unavailable(ctx, err)
	}
	defer conn.Close()
	// Acquire the write lock before reading to avoid deferred read-to-write
	// upgrades failing with SQLITE_BUSY when two processes synchronize.
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return model.SyncReport{}, unavailable(ctx, err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	report, err := applySnapshot(ctx, conn, snapshot)
	if err != nil {
		return model.SyncReport{}, unavailable(ctx, err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return model.SyncReport{}, unavailable(ctx, err)
	}
	return report, nil
}

func applySnapshot(ctx context.Context, conn *sql.Conn, snapshot model.Snapshot) (model.SyncReport, error) {
	type source struct{ path, revision string }
	previous := make(map[string]source)
	rows, err := conn.QueryContext(ctx, "SELECT id, path, revision FROM documents")
	if err != nil {
		return model.SyncReport{}, err
	}
	for rows.Next() {
		var id string
		var old source
		if err := rows.Scan(&id, &old.path, &old.revision); err != nil {
			rows.Close()
			return model.SyncReport{}, err
		}
		previous[id] = old
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return model.SyncReport{}, err
	}
	report := model.SyncReport{Diagnostics: append([]model.Diagnostic(nil), snapshot.Diagnostics...)}
	seenIDs, seenPaths := make(map[string]bool), make(map[string]bool)
	for _, d := range snapshot.Documents {
		if d.ID == "" || d.Path == "" || seenIDs[d.ID] || seenPaths[d.Path] {
			return model.SyncReport{}, fmt.Errorf("snapshot contains missing or duplicate identities")
		}
		seenIDs[d.ID], seenPaths[d.Path] = true, true
		old, exists := previous[d.ID]
		delete(previous, d.ID)
		if exists && old.path == d.Path && old.revision == d.Revision {
			continue
		}
		if exists {
			report.Updated++
		} else {
			report.Added++
		}
		var created, changed any
		if len(d.History) != 0 {
			created = stamp(d.History[0].At)
			changed = stamp(d.History[len(d.History)-1].At)
		}
		_, err := conn.ExecContext(ctx, `INSERT INTO documents
			(id, path, folder, title, kind, state, body, raw, revision, semantic_fingerprint, created_at, state_changed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET path=excluded.path, folder=excluded.folder,
			title=excluded.title, kind=excluded.kind, state=excluded.state, body=excluded.body,
			raw=excluded.raw, revision=excluded.revision, semantic_fingerprint=excluded.semantic_fingerprint,
			created_at=excluded.created_at, state_changed_at=excluded.state_changed_at`,
			d.ID, d.Path, path.Dir(d.Path), d.Title, d.Kind, d.State, d.Body, d.Raw, d.Revision, semanticFingerprint(d), created, changed)
		if err != nil {
			return model.SyncReport{}, err
		}
		if _, err := conn.ExecContext(ctx, "DELETE FROM state_events WHERE document_id = ?", d.ID); err != nil {
			return model.SyncReport{}, err
		}
		for i, event := range d.History {
			if _, err := conn.ExecContext(ctx, "INSERT INTO state_events(document_id, ordinal, state, at) VALUES (?, ?, ?, ?)", d.ID, i, event.State, stamp(event.At)); err != nil {
				return model.SyncReport{}, err
			}
		}
	}
	for id := range previous {
		if _, err := conn.ExecContext(ctx, "DELETE FROM documents WHERE id = ?", id); err != nil {
			return model.SyncReport{}, err
		}
		report.Removed++
	}
	if _, err := conn.ExecContext(ctx, "DELETE FROM diagnostics"); err != nil {
		return model.SyncReport{}, err
	}
	for i, d := range snapshot.Diagnostics {
		if _, err := conn.ExecContext(ctx, "INSERT INTO diagnostics(ordinal, code, path, message) VALUES (?, ?, ?, ?)", i, d.Code, d.Path, d.Message); err != nil {
			return model.SyncReport{}, err
		}
	}
	return report, nil
}

// Metadata and path-only changes must not invalidate future semantic inputs.
// Length-prefixing raw bytes avoids boundary collisions and preserves even
// non-UTF-8 body bytes (a JSON encoding would silently replace those bytes).
func semanticFingerprint(d model.Document) string {
	hash := sha256.New()
	for _, field := range []string{d.Title, d.Body} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		hash.Write(length[:])
		hash.Write([]byte(field))
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

// A fixed-width UTC representation preserves nanosecond ordering in SQL without
// float conversions or UnixNano's restricted year range.
const timestampLayout = "2006-01-02T15:04:05.000000000Z"

func stamp(t time.Time) string { return t.UTC().Format(timestampLayout) }

func unavailable(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// SQLite errors may contain user data; do not leak them through public errors.
	return &model.Error{Code: "index_unavailable", Message: "derived index unavailable; retry sync or rebuild the index"}
}
