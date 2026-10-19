package index

const schemaVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS documents (
    id TEXT PRIMARY KEY NOT NULL,
    path TEXT NOT NULL,
    folder TEXT NOT NULL,
    title TEXT NOT NULL,
    kind TEXT NOT NULL,
    state TEXT NOT NULL,
    body TEXT NOT NULL,
    raw TEXT NOT NULL,
    revision TEXT NOT NULL,
    semantic_fingerprint TEXT NOT NULL,
    created_at TEXT,
    state_changed_at TEXT
);
CREATE INDEX IF NOT EXISTS documents_folder ON documents(folder);
CREATE TABLE IF NOT EXISTS state_events (
    document_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    state TEXT NOT NULL,
    at TEXT NOT NULL,
    PRIMARY KEY(document_id, ordinal)
);
CREATE INDEX IF NOT EXISTS state_events_transition ON state_events(state, at, document_id);
CREATE TABLE IF NOT EXISTS diagnostics (
    ordinal INTEGER PRIMARY KEY,
    code TEXT NOT NULL,
    path TEXT NOT NULL,
    message TEXT NOT NULL
);
CREATE VIRTUAL TABLE IF NOT EXISTS document_fts USING fts5(
    id UNINDEXED, title, path, kind, state, body
);
CREATE TRIGGER IF NOT EXISTS documents_insert AFTER INSERT ON documents BEGIN
    INSERT INTO document_fts(id, title, path, kind, state, body)
    VALUES (new.id, new.title, new.path, new.kind, new.state, new.body);
END;
CREATE TRIGGER IF NOT EXISTS documents_delete AFTER DELETE ON documents BEGIN
    DELETE FROM document_fts WHERE id = old.id;
END;
CREATE TRIGGER IF NOT EXISTS documents_update AFTER UPDATE ON documents BEGIN
    DELETE FROM document_fts WHERE id = old.id;
    INSERT INTO document_fts(id, title, path, kind, state, body)
    VALUES (new.id, new.title, new.path, new.kind, new.state, new.body);
END;
PRAGMA user_version = 1;
`
