package index

import (
	"database/sql"
	"encoding/binary"
	"math"
	"path/filepath"
	"reflect"
	"testing"

	"recall/internal/embedding"
	"recall/internal/model"
)

// The real shape of a pre-v3 index, including the retired task columns.
const legacySchema = `
CREATE TABLE documents (
    id TEXT PRIMARY KEY NOT NULL, path TEXT NOT NULL, folder TEXT NOT NULL, title TEXT NOT NULL,
    kind TEXT NOT NULL, state TEXT NOT NULL, body TEXT NOT NULL, raw TEXT NOT NULL, revision TEXT NOT NULL,
    semantic_fingerprint TEXT NOT NULL, created_at TEXT, state_changed_at TEXT
);
CREATE INDEX documents_folder ON documents(folder);
CREATE TABLE state_events (
    document_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE, ordinal INTEGER NOT NULL,
    state TEXT NOT NULL, at TEXT NOT NULL, PRIMARY KEY(document_id, ordinal)
);
CREATE INDEX state_events_transition ON state_events(state, at, document_id);
CREATE TABLE diagnostics (ordinal INTEGER PRIMARY KEY, code TEXT NOT NULL, path TEXT NOT NULL, message TEXT NOT NULL);
CREATE VIRTUAL TABLE document_fts USING fts5(id UNINDEXED, title, path, kind, state, body);
CREATE TRIGGER documents_insert AFTER INSERT ON documents BEGIN
    INSERT INTO document_fts(id, title, path, kind, state, body) VALUES (new.id, new.title, new.path, new.kind, new.state, new.body);
END;
CREATE TRIGGER documents_delete AFTER DELETE ON documents BEGIN
    DELETE FROM document_fts WHERE id = old.id;
END;
CREATE TRIGGER documents_update AFTER UPDATE ON documents BEGIN
    DELETE FROM document_fts WHERE id = old.id;
    INSERT INTO document_fts(id, title, path, kind, state, body) VALUES (new.id, new.title, new.path, new.kind, new.state, new.body);
END;
`

func legacyIndex(t *testing.T, version int, note model.Document) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "index.sqlite")
	db, err := sql.Open("sqlite", filename+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(legacySchema); err != nil {
		t.Fatal(err)
	}
	if version == 2 {
		if _, err := db.Exec(vectorSchema); err != nil {
			t.Fatal(err)
		}
	}
	const insert = `INSERT INTO documents(id, path, folder, title, kind, state, body, raw, revision, semantic_fingerprint) VALUES (?, ?, '.', ?, ?, ?, ?, ?, ?, ?)`
	if _, err := db.Exec(insert, note.ID, note.Path, note.Title, "note", "", note.Body, note.Raw, note.Revision, semanticFingerprint(note)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(insert, "old-task", "old.task.md", "Old task", "task", "planned", "retiredword", "raw", "rev", "print"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO state_events VALUES ('old-task', 0, 'planned', '2026-01-01T00:00:00.000000000Z')`); err != nil {
		t.Fatal(err)
	}
	if version == 2 {
		blob := make([]byte, embedding.Dimensions*4)
		binary.LittleEndian.PutUint32(blob, math.Float32bits(1))
		for _, statement := range []string{
			`UPDATE vector_meta SET signature='sig'`,
			`DELETE FROM embedding_jobs WHERE document_id='` + note.ID + `'`,
			`INSERT INTO chunks VALUES ('` + note.ID + `', 0, 'needle', 0, 6, 1, 1)`,
		} {
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(`INSERT INTO embeddings VALUES (?, 0, 'sig', ?, ?)`, note.ID, embedding.Dimensions, blob); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("PRAGMA user_version = " + map[int]string{1: "1", 2: "2"}[version]); err != nil {
		t.Fatal(err)
	}
	return filename
}

func TestLegacyIndexUpgradesInPlaceAndDropsTaskData(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(map[int]string{1: "v1", 2: "v2"}[version], func(t *testing.T) {
			note := fixture(t, "note", "note.md", "Note", "needle")
			filename := legacyIndex(t, version, note)
			s, err := Open(filename)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var got int
			if err := s.db.QueryRow("PRAGMA user_version").Scan(&got); err != nil || got != schemaVersion {
				t.Fatalf("user_version = %d, %v", got, err)
			}
			var columns []string
			rows, err := s.db.Query("SELECT name FROM pragma_table_info('documents') ORDER BY cid")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					t.Fatal(err)
				}
				columns = append(columns, name)
			}
			if want := []string{"id", "path", "folder", "title", "body", "raw", "revision", "semantic_fingerprint"}; !reflect.DeepEqual(columns, want) {
				t.Fatalf("columns = %v", columns)
			}
			var leftovers int
			if err := s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name = 'state_events'").Scan(&leftovers); err != nil || leftovers != 0 {
				t.Fatalf("state_events = %d, %v", leftovers, err)
			}
			// Metadata terms stop matching once the FTS columns are rebuilt.
			if h, err := s.Keyword(t.Context(), model.SearchQuery{Text: "planned"}); err != nil || len(h) != 0 {
				t.Fatalf("retired state still searchable: %+v %v", h, err)
			}
			if h, err := s.Keyword(t.Context(), model.SearchQuery{Text: "needle"}); err != nil || len(h) != 1 || h[0].ID != note.ID {
				t.Fatalf("rebuilt FTS: %+v %v", h, err)
			}
			if version == 2 {
				var embeddings int
				if err := s.db.QueryRow("SELECT count(*) FROM embeddings WHERE document_id = ?", note.ID).Scan(&embeddings); err != nil || embeddings != 1 {
					t.Fatalf("embedding lost by migration: %d %v", embeddings, err)
				}
			}
			// A task row disappears at the next reconciliation, and triggers still work.
			r, err := s.Apply(t.Context(), model.Snapshot{Documents: []model.Document{note}})
			if err != nil || r.Removed != 1 || r.Added != 0 || r.Updated != 0 {
				t.Fatalf("apply after upgrade: %+v %v", r, err)
			}
			if ids := indexedIDs(t, s); !reflect.DeepEqual(ids, []string{note.ID}) {
				t.Fatalf("documents after upgrade: %v", ids)
			}
			if h, err := s.Keyword(t.Context(), model.SearchQuery{Text: "retiredword"}); err != nil || len(h) != 0 {
				t.Fatalf("task still searchable: %+v %v", h, err)
			}
			edited := fixture(t, note.ID, note.Path, note.Title, "replacement")
			if _, err := s.Apply(t.Context(), model.Snapshot{Documents: []model.Document{edited}}); err != nil {
				t.Fatal(err)
			}
			if h, err := s.Keyword(t.Context(), model.SearchQuery{Text: "replacement"}); err != nil || len(h) != 1 {
				t.Fatalf("FTS trigger after upgrade: %+v %v", h, err)
			}
			if err := s.EnsureSignature(t.Context(), "sig"); err != nil {
				t.Fatal(err)
			}
			if n, err := s.Pending(t.Context(), model.Filter{}, "sig"); err != nil || n != 1 {
				t.Fatalf("pending after upgrade = %d, %v", n, err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			// A second open must not rerun the migration.
			again, err := Open(filename)
			if err != nil {
				t.Fatal(err)
			}
			if err := again.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFutureIndexVersionIsRefused(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "index.sqlite")
	s, err := Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filename); model.ErrorCode(err) != "index_unavailable" {
		t.Fatalf("future schema accepted: %v", err)
	}
}
