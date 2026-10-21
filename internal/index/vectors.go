package index

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"math"
	"strings"
	"time"

	"recall/internal/embedding"
	"recall/internal/model"
)

const JobLease = 60 * time.Second

// Chunk retains exact original body slices. Payload is inference-only, not
// persisted: it also includes the token-capped title and no purpose prefix.
type Chunk struct {
	Text, Payload                          string
	StartByte, EndByte, StartLine, EndLine int
}

type EmbeddingJob struct {
	Document                      model.Document
	Fingerprint, Signature, Lease string
	Generation                    int64
}

func (s *Store) vectorWrite(ctx context.Context, fn func(*sql.Conn) error) error {
	c, err := s.db.Conn(ctx)
	if err != nil {
		return unavailable(ctx, err)
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return unavailable(ctx, err)
	}
	defer c.ExecContext(context.Background(), "ROLLBACK")
	if err = fn(c); err != nil {
		return err
	}
	_, err = c.ExecContext(ctx, "COMMIT")
	return unavailable(ctx, err)
}

// Reset is source-preserving. The monotonically increasing generation and new
// random claim tokens prevent pre-reset workers from publishing after repopulation.
// The caller holds the shared source lock across Reset and Apply.
func (s *Store) Reset(ctx context.Context) error {
	return s.vectorWrite(ctx, func(c *sql.Conn) error {
		_, err := c.ExecContext(ctx, `UPDATE vector_meta SET generation=generation+1;
		DELETE FROM documents; DELETE FROM document_fts; DELETE FROM diagnostics;`)
		return unavailable(ctx, err)
	})
}

func (s *Store) EnsureSignature(ctx context.Context, signature string) error {
	if signature == "" {
		return invalid("vector signature must not be empty")
	}
	return s.vectorWrite(ctx, func(c *sql.Conn) error {
		var old string
		if err := c.QueryRowContext(ctx, "SELECT signature FROM vector_meta").Scan(&old); err != nil {
			return unavailable(ctx, err)
		}
		if old == signature {
			return nil
		}
		if _, err := c.ExecContext(ctx, `UPDATE vector_meta SET signature=?, generation=generation+1;
		DELETE FROM chunks; DELETE FROM embedding_jobs;`, signature); err != nil {
			return unavailable(ctx, err)
		}
		_, err := c.ExecContext(ctx, `INSERT INTO embedding_jobs(document_id,fingerprint,signature,generation)
		SELECT d.id,d.semantic_fingerprint,m.signature,m.generation FROM documents d,vector_meta m`)
		return unavailable(ctx, err)
	})
}

func (s *Store) ClaimJob(ctx context.Context, f model.Filter, signature string, clock func() time.Time) (*EmbeddingJob, error) {
	where, args, err := filterSQL(f)
	if err != nil {
		return nil, err
	}
	var job *EmbeddingJob
	err = s.vectorWrite(ctx, func(c *sql.Conn) error {
		now := clock()
		j := new(EmbeddingJob)
		params := append(args, signature, stamp(now))
		err := c.QueryRowContext(ctx, `SELECT d.id,d.path,d.title,d.body,d.raw,d.revision,j.fingerprint,j.signature,j.generation
		FROM embedding_jobs j JOIN documents d ON d.id=j.document_id JOIN vector_meta m ON m.signature=j.signature AND m.generation=j.generation
		WHERE `+where+` AND j.signature=? AND j.lease_until<=? ORDER BY d.id LIMIT 1`, params...).Scan(
			&j.Document.ID, &j.Document.Path, &j.Document.Title, &j.Document.Body, &j.Document.Raw, &j.Document.Revision, &j.Fingerprint, &j.Signature, &j.Generation)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return unavailable(ctx, err)
		}
		var token [16]byte
		rand.Read(token[:])
		j.Lease = hex.EncodeToString(token[:])
		_, err = c.ExecContext(ctx, "UPDATE embedding_jobs SET lease=?,lease_until=? WHERE document_id=?", j.Lease, stamp(now.Add(JobLease)), j.Document.ID)
		if err != nil {
			return unavailable(ctx, err)
		}
		job = j
		return nil
	})
	return job, err
}

// Every ownership operation checks expiry as well as the unique token/generation.
const ownedJob = `document_id=? AND fingerprint=? AND signature=? AND generation=? AND lease=? AND lease_until>?`

func jobArgs(j EmbeddingJob, now time.Time) []any {
	return []any{j.Document.ID, j.Fingerprint, j.Signature, j.Generation, j.Lease, stamp(now)}
}

func (s *Store) RenewJob(ctx context.Context, j EmbeddingJob, clock func() time.Time) (bool, error) {
	renewed := false
	err := s.vectorWrite(ctx, func(c *sql.Conn) error {
		now := clock()
		args := append([]any{stamp(now.Add(JobLease))}, jobArgs(j, now)...)
		r, err := c.ExecContext(ctx, "UPDATE embedding_jobs SET lease_until=? WHERE "+ownedJob, args...)
		if err != nil {
			return unavailable(ctx, err)
		}
		n, err := r.RowsAffected()
		renewed = n == 1
		return unavailable(ctx, err)
	})
	return renewed, err
}

func (s *Store) ReleaseJob(ctx context.Context, j EmbeddingJob) error {
	_, err := s.db.ExecContext(ctx, `UPDATE embedding_jobs SET lease='',lease_until='' WHERE document_id=? AND lease=? AND generation=?`, j.Document.ID, j.Lease, j.Generation)
	return unavailable(ctx, err)
}

// Publish must be called under the source lock, after reading and verifying the
// real source ID/title/body. Its own transaction independently validates current
// cached content and the live lease/signature/generation before inserting anything.
func (s *Store) Publish(ctx context.Context, j EmbeddingJob, chunks []Chunk, vectors [][]float32, clock func() time.Time) (bool, error) {
	if len(chunks) == 0 || len(chunks) != len(vectors) {
		return false, invalid("invalid embedding passage count")
	}
	blobs := make([][]byte, len(vectors))
	for i, v := range vectors {
		var err error
		blobs[i], err = encodeVector(v)
		if err != nil {
			return false, err
		}
	}
	pub := false
	err := s.vectorWrite(ctx, func(c *sql.Conn) error {
		now := clock()
		var count int
		err := c.QueryRowContext(ctx, `SELECT count(*) FROM embedding_jobs WHERE `+ownedJob+`
		AND EXISTS (SELECT 1 FROM documents d WHERE d.id=embedding_jobs.document_id AND d.semantic_fingerprint=embedding_jobs.fingerprint)
		AND EXISTS (SELECT 1 FROM vector_meta m WHERE m.signature=embedding_jobs.signature AND m.generation=embedding_jobs.generation)`, jobArgs(j, now)...).Scan(&count)
		if err != nil {
			return unavailable(ctx, err)
		}
		if count != 1 {
			return nil
		}
		if _, err = c.ExecContext(ctx, "DELETE FROM chunks WHERE document_id=?", j.Document.ID); err != nil {
			return unavailable(ctx, err)
		}
		for i, ch := range chunks {
			if _, err = c.ExecContext(ctx, `INSERT INTO chunks VALUES(?,?,?,?,?,?,?)`, j.Document.ID, i, ch.Text, ch.StartByte, ch.EndByte, ch.StartLine, ch.EndLine); err != nil {
				return unavailable(ctx, err)
			}
			if _, err = c.ExecContext(ctx, `INSERT INTO embeddings VALUES(?,?,?,?,?)`, j.Document.ID, i, j.Signature, embedding.Dimensions, blobs[i]); err != nil {
				return unavailable(ctx, err)
			}
		}
		if _, err = c.ExecContext(ctx, "DELETE FROM embedding_jobs WHERE document_id=?", j.Document.ID); err != nil {
			return unavailable(ctx, err)
		}
		pub = true
		return nil
	})
	return pub, err
}

func (s *Store) Pending(ctx context.Context, f model.Filter, signature string) (int, error) {
	where, args, err := filterSQL(f)
	if err != nil {
		return 0, err
	}
	args = append(args, signature)
	var n int
	err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM documents d WHERE `+where+` AND
	(NOT EXISTS (SELECT 1 FROM embeddings e WHERE e.document_id=d.id AND e.signature=?)
	OR EXISTS (SELECT 1 FROM embedding_jobs j WHERE j.document_id=d.id))`, args...).Scan(&n)
	return n, unavailable(ctx, err)
}

// SourceCurrent checks a real source read against the cached snapshot. Search
// uses it after query inference, so direct edits during inference cannot return
// an unlabelled stale excerpt. Metadata-only differences need sync, not embedding.
func (s *Store) SourceCurrent(ctx context.Context, d model.Document) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM documents WHERE id=? AND path=? AND revision=?`, d.ID, d.Path, d.Revision).Scan(&n)
	return n == 1, unavailable(ctx, err)
}

// StreamVectors holds a read snapshot, not an inference or source lock. Only
// filtered, current vectors are visible; callbacks must not reenter this Store.
func (s *Store) StreamVectors(ctx context.Context, f model.Filter, signature string, visit func(model.Hit, []float32) error) error {
	where, args, err := filterSQL(f)
	if err != nil {
		return err
	}
	args = append(args, signature)
	rows, err := s.db.QueryContext(ctx, `SELECT d.id,d.path,d.title,d.kind,d.state,d.revision,c.text,c.start_byte,c.end_byte,c.start_line,c.end_line,d.raw,d.body,e.dimensions,e.vector
	FROM embeddings e JOIN chunks c ON c.document_id=e.document_id AND c.ordinal=e.ordinal JOIN documents d ON d.id=e.document_id
	JOIN vector_meta m ON m.signature=e.signature
	WHERE `+where+` AND e.signature=? AND NOT EXISTS (SELECT 1 FROM embedding_jobs j WHERE j.document_id=d.id)
	ORDER BY d.id,c.ordinal`, args...)
	if err != nil {
		return unavailable(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		var h model.Hit
		var start, end, dim int
		var raw, body string
		var blob []byte
		if err = rows.Scan(&h.ID, &h.Path, &h.Title, &h.Kind, &h.State, &h.Revision, &h.Snippet, &start, &end, &h.StartLine, &h.EndLine, &raw, &body, &dim, &blob); err != nil {
			return unavailable(ctx, err)
		}
		v, err := decodeVector(blob, dim)
		if err != nil {
			return err
		}
		// Metadata-only edits can move body line numbers without re-embedding.
		if end > start && start >= 0 && end <= len(body) {
			base := 1
			if strings.HasSuffix(raw, body) {
				base += strings.Count(raw[:len(raw)-len(body)], "\n")
			}
			h.StartLine = base + strings.Count(body[:start], "\n")
			h.EndLine = base + strings.Count(body[:end-1], "\n")
		}
		h.Snippet = preview(h.Snippet)
		h.IndexState = "current"
		if err = visit(h, v); err != nil {
			return err
		}
	}
	return unavailable(ctx, rows.Err())
}

func encodeVector(v []float32) ([]byte, error) {
	if len(v) != embedding.Dimensions {
		return nil, invalid("vector dimension mismatch")
	}
	var norm float64
	b := make([]byte, 4*len(v))
	for i, f := range v {
		x := float64(f)
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, invalid("nonfinite vector")
		}
		norm += x * x
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	if norm == 0 {
		return nil, invalid("zero vector")
	}
	return b, nil
}
func decodeVector(b []byte, dim int) ([]float32, error) {
	if dim != embedding.Dimensions || len(b) != dim*4 {
		return nil, &model.Error{Code: "index_unavailable", Message: "invalid indexed vector dimensions; rebuild the index"}
	}
	v := make([]float32, dim)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	if _, err := encodeVector(v); err != nil {
		return nil, &model.Error{Code: "index_unavailable", Message: "invalid indexed vector; rebuild the index"}
	}
	return v, nil
}
