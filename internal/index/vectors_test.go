package index

import (
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"recall/internal/embedding"
	"recall/internal/model"
)

func fixedClock(at time.Time) func() time.Time { return func() time.Time { return at } }

func TestConcurrentClaimsExpiredLeaseAndPublication(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "index.sqlite")
	s, err := Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	second, err := Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	d := fixture(t, "a", "a.note.md", "Title", "body")
	apply(t, s, model.Snapshot{Documents: []model.Document{d}})
	if err := s.EnsureSignature(t.Context(), "sig"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	jobs := make(chan *EmbeddingJob, 2)
	var wg sync.WaitGroup
	for _, claimer := range []*Store{s, second} {
		wg.Go(func() {
			j, err := claimer.ClaimJob(t.Context(), model.Filter{}, "sig", fixedClock(now))
			if err != nil {
				t.Error(err)
			}
			jobs <- j
		})
	}
	wg.Wait()
	close(jobs)
	var old *EmbeddingJob
	count := 0
	for j := range jobs {
		if j != nil {
			old = j
			count++
		}
	}
	if count != 1 {
		t.Fatalf("claims=%d", count)
	}
	if j, err := s.ClaimJob(t.Context(), model.Filter{}, "sig", fixedClock(now.Add(59*time.Second))); err != nil || j != nil {
		t.Fatalf("early claim %+v %v", j, err)
	}
	j, err := s.ClaimJob(t.Context(), model.Filter{}, "sig", fixedClock(now.Add(61*time.Second)))
	if err != nil || j == nil || j.Lease == old.Lease {
		t.Fatalf("reclaim %+v %v", j, err)
	}
	v := make([]float32, embedding.Dimensions)
	v[0] = 1
	cs := []Chunk{{Text: "body", Payload: "Title\n\nbody", EndByte: 4, StartLine: 7, EndLine: 7}}
	if ok, err := s.Publish(t.Context(), *old, cs, [][]float32{v}, fixedClock(now.Add(62*time.Second))); err != nil || ok {
		t.Fatalf("old publication=%v %v", ok, err)
	}
	if ok, err := s.RenewJob(t.Context(), *j, fixedClock(now.Add(100*time.Second))); err != nil || !ok {
		t.Fatalf("renew=%v %v", ok, err)
	}
	if ok, err := s.Publish(t.Context(), *j, cs, [][]float32{v}, fixedClock(now.Add(122*time.Second))); err != nil || !ok {
		t.Fatalf("publish=%v %v", ok, err)
	}
	n := 0
	if err = s.StreamVectors(t.Context(), model.Filter{}, "sig", func(h model.Hit, got []float32) error {
		n++
		if len(got) != 768 || got[0] != 1 || h.StartLine != 7 || h.Snippet != "body" {
			t.Fatalf("%+v %v", h, got)
		}
		return nil
	}); err != nil || n != 1 {
		t.Fatalf("stream=%d %v", n, err)
	}
	apply(t, s, model.Snapshot{})
	for _, table := range []string{"documents", "document_fts", "chunks", "embeddings", "embedding_jobs"} {
		var n int
		if err = s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("live %s count=%d %v", table, n, err)
		}
	}
}
func TestVectorValidationAndSignatureInvalidation(t *testing.T) {
	s := openTestStore(t)
	d := fixture(t, "a", "a.note.md", "Title", "body")
	apply(t, s, model.Snapshot{Documents: []model.Document{d}})
	s.EnsureSignature(t.Context(), "one")
	now := time.Now()
	j, err := s.ClaimJob(t.Context(), model.Filter{}, "one", fixedClock(now))
	if err != nil || j == nil {
		t.Fatal(err)
	}
	for _, v := range [][]float32{{1}, make([]float32, 768)} {
		if _, err := s.Publish(t.Context(), *j, []Chunk{{Text: "body"}}, [][]float32{v}, fixedClock(now)); err == nil {
			t.Fatal("accepted invalid vector")
		}
	}
	v := make([]float32, 768)
	v[0] = float32(math.NaN())
	if _, err := s.Publish(t.Context(), *j, []Chunk{{Text: "body"}}, [][]float32{v}, fixedClock(now)); err == nil {
		t.Fatal("accepted NaN")
	}
	s.EnsureSignature(t.Context(), "two")
	v[0] = 1
	if ok, err := s.Publish(t.Context(), *j, []Chunk{{Text: "body"}}, [][]float32{v}, fixedClock(now)); err != nil || ok {
		t.Fatalf("signature stale publication=%v %v", ok, err)
	}
}

func TestExpiredLeaseCannotRenewOrPublish(t *testing.T) {
	s := openTestStore(t)
	d := fixture(t, "a", "a.note.md", "Title", "body")
	apply(t, s, model.Snapshot{Documents: []model.Document{d}})
	if err := s.EnsureSignature(t.Context(), "sig"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	j, err := s.ClaimJob(t.Context(), model.Filter{}, "sig", fixedClock(now))
	if err != nil || j == nil {
		t.Fatalf("%+v %v", j, err)
	}
	if ok, err := s.RenewJob(t.Context(), *j, fixedClock(now.Add(60*time.Second))); err != nil || ok {
		t.Fatalf("expired renewal=%v %v", ok, err)
	}
	v := make([]float32, 768)
	v[0] = 1
	if ok, err := s.Publish(t.Context(), *j, []Chunk{{Text: "body"}}, [][]float32{v}, fixedClock(now.Add(60*time.Second))); err != nil || ok {
		t.Fatalf("expired publish=%v %v", ok, err)
	}
}

func TestLeaseClockIsSampledAfterAcquiringTransaction(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "index.sqlite")
	s, err := Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	second, err := Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	d := fixture(t, "a", "a.note.md", "Title", "body")
	apply(t, s, model.Snapshot{Documents: []model.Document{d}})
	if err := s.EnsureSignature(t.Context(), "sig"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	j, err := s.ClaimJob(t.Context(), model.Filter{}, "sig", fixedClock(now))
	if err != nil || j == nil {
		t.Fatalf("%+v %v", j, err)
	}
	// Attempt another write inside the clock using an independent connection:
	// BEGIN IMMEDIATE must already exclude that writer. A time
	// sampled before transaction acquisition could authorize an expired lease.
	c, err := second.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.ExecContext(t.Context(), "PRAGMA busy_timeout=1"); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time {
		if _, err := c.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err == nil {
			c.ExecContext(t.Context(), "ROLLBACK")
			t.Error("lease clock sampled without holding the transaction write lock")
		}
		return now.Add(61 * time.Second)
	}
	v := make([]float32, 768)
	v[0] = 1
	if ok, err := s.Publish(t.Context(), *j, []Chunk{{Text: "body"}}, [][]float32{v}, clock); err != nil || ok {
		t.Fatalf("expired publication %v %v", ok, err)
	}
}

func TestSemanticMigrationFromV1PreservesSourceProjection(t *testing.T) {
	s := openTestStore(t)
	d := fixture(t, "a", "a.note.md", "Title", "body")
	apply(t, s, model.Snapshot{Documents: []model.Document{d}})
	_, err := s.db.Exec(`DROP TRIGGER documents_embedding_insert; DROP TRIGGER documents_embedding_update;
	DROP TABLE embedding_jobs; DROP TABLE embeddings; DROP TABLE chunks; DROP TABLE vector_meta; PRAGMA user_version=1;`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.initialize(); err != nil {
		t.Fatal(err)
	}
	if h, err := s.Keyword(t.Context(), model.SearchQuery{Text: "body"}); err != nil || len(h) != 1 || h[0].ID != "a" {
		t.Fatalf("migration lost metadata/FTS %+v %v", h, err)
	}
	if err = s.EnsureSignature(t.Context(), "sig"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Pending(t.Context(), model.Filter{}, "sig"); err != nil || n != 1 {
		t.Fatalf("migrated pending=%d %v", n, err)
	}
}
