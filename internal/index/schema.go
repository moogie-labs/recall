package index

const schemaVersion = 2

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

// The v1 metadata projection is retained. Semantic data is always disposable.
const vectorSchema = `
CREATE TABLE IF NOT EXISTS vector_meta (
    singleton INTEGER PRIMARY KEY CHECK(singleton=1),
    signature TEXT NOT NULL,
    generation INTEGER NOT NULL
);
INSERT OR IGNORE INTO vector_meta VALUES (1, '', 1);
CREATE TABLE IF NOT EXISTS chunks (
    document_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    text TEXT NOT NULL,
    start_byte INTEGER NOT NULL,
    end_byte INTEGER NOT NULL,
    start_line INTEGER NOT NULL,
    end_line INTEGER NOT NULL,
    PRIMARY KEY(document_id, ordinal)
);
CREATE TABLE IF NOT EXISTS embeddings (
    document_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    signature TEXT NOT NULL,
    dimensions INTEGER NOT NULL CHECK(dimensions=768),
    vector BLOB NOT NULL CHECK(length(vector)=dimensions*4),
    PRIMARY KEY(document_id, ordinal),
    FOREIGN KEY(document_id, ordinal) REFERENCES chunks(document_id, ordinal) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS embedding_jobs (
    document_id TEXT PRIMARY KEY REFERENCES documents(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    signature TEXT NOT NULL,
    generation INTEGER NOT NULL,
    lease TEXT NOT NULL DEFAULT '',
    lease_until TEXT NOT NULL DEFAULT ''
);
CREATE TRIGGER IF NOT EXISTS documents_embedding_insert AFTER INSERT ON documents BEGIN
    INSERT INTO embedding_jobs(document_id, fingerprint, signature, generation)
    SELECT new.id, new.semantic_fingerprint, signature, generation FROM vector_meta;
END;
CREATE TRIGGER IF NOT EXISTS documents_embedding_update AFTER UPDATE ON documents
WHEN old.semantic_fingerprint != new.semantic_fingerprint BEGIN
    DELETE FROM chunks WHERE document_id=new.id;
    DELETE FROM embedding_jobs WHERE document_id=new.id;
    INSERT INTO embedding_jobs(document_id, fingerprint, signature, generation)
    SELECT new.id, new.semantic_fingerprint, signature, generation FROM vector_meta;
END;
INSERT OR IGNORE INTO embedding_jobs(document_id, fingerprint, signature, generation)
SELECT d.id, d.semantic_fingerprint, m.signature, m.generation FROM documents d, vector_meta m;
PRAGMA user_version = 2;
`
