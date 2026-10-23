package search

import (
	"math"
	"os"
	"testing"
	"time"

	"recall/internal/embedding"
	"recall/internal/index"
	"recall/internal/model"
)

func TestExactSearchTenThousandSyntheticChunks(t *testing.T) {
	if os.Getenv("RECALL_SCALE_TEST") != "1" {
		t.Skip("opt-in synthetic component measurement; not semantic quality or full-app CPU")
	}
	e, _, idx, _ := searchFixture(t, model.Document{ID: "synthetic", Body: "body"})
	if err := idx.EnsureSignature(t.Context(), e.signature); err != nil {
		t.Fatal(err)
	}
	j, err := idx.ClaimJob(t.Context(), model.Filter{}, e.signature, time.Now)
	if err != nil || j == nil {
		t.Fatal(err)
	}
	chunks := make([]index.Chunk, 10000)
	vectors := make([][]float32, 10000)
	for i := range chunks {
		chunks[i] = index.Chunk{Text: "synthetic", StartLine: i + 1, EndLine: i + 1}
		v := make([]float32, embedding.Dimensions)
		x := float64(i+1) / 10000
		v[0], v[1] = float32(x), float32(math.Sqrt(1-x*x))
		vectors[i] = v
	}
	start := time.Now()
	ok, err := idx.Publish(t.Context(), *j, chunks, vectors, time.Now)
	if err != nil || !ok {
		t.Fatalf("publish %v %v", ok, err)
	}
	t.Logf("synthetic 10,000-chunk vector publication: %s", time.Since(start))
	q := make([]float32, embedding.Dimensions)
	q[0] = 1
	for n := 0; n < 5; n++ {
		start = time.Now()
		h, err := e.semantic(t.Context(), model.Filter{}, q)
		elapsed := time.Since(start)
		if err != nil || len(h) != 50 {
			t.Fatalf("scan %d %v", len(h), err)
		}
		for rank, hit := range h {
			if hit.StartLine != 10000-rank {
				t.Fatalf("rank %d: line %d", rank, hit.StartLine)
			}
		}
		t.Logf("exact 10,000 x 768 vector scan+heap+SQLite (run %d): %s", n+1, elapsed)
	}
}
