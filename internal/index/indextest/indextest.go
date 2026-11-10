// Package indextest reads a derived index directly, without the reconciliation
// that App reads perform, so tests can observe what was actually published.
package indextest

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"recall/internal/model"
)

// Documents returns the indexed documents in ID order.
func Documents(t testing.TB, database string) []model.Document {
	t.Helper()
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT id, path, title, body, raw, revision FROM documents ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	documents := []model.Document{}
	for rows.Next() {
		var d model.Document
		if err := rows.Scan(&d.ID, &d.Path, &d.Title, &d.Body, &d.Raw, &d.Revision); err != nil {
			t.Fatal(err)
		}
		documents = append(documents, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return documents
}
