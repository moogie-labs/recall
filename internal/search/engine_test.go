package search

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"recall/internal/embedding"
	"recall/internal/index"
	"recall/internal/model"
)

type memorySource struct {
	mu   sync.Mutex
	docs []model.Document
}

func (s *memorySource) WithLock(_ context.Context, fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn()
}
func (s *memorySource) Read(_ context.Context, r model.Ref) (model.Document, error) {
	for _, d := range s.docs {
		if r.Path == d.Path || r.ID == d.ID {
			return d, nil
		}
	}
	return model.Document{}, &model.Error{Code: "not_found"}
}

type fakeProvider struct {
	mu               sync.Mutex
	calls            int
	fail             bool
	started, release chan struct{}
	once             sync.Once
}

func (p *fakeProvider) Tokenize(_ context.Context, s string) ([]int, error) {
	if p.fail {
		return nil, &model.Error{Code: "runtime_unavailable"}
	}
	return make([]int, len(strings.Fields(s))+2), nil
}
func (p *fakeProvider) Embed(ctx context.Context, purpose string, texts []string) ([][]float32, error) {
	if p.fail {
		return nil, &model.Error{Code: "runtime_unavailable"}
	}
	if purpose == "document" {
		p.mu.Lock()
		p.calls += len(texts)
		p.mu.Unlock()
		if p.started != nil {
			p.once.Do(func() { close(p.started) })
			select {
			case <-p.release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	var out [][]float32
	for _, s := range texts {
		if strings.HasPrefix(s, "search_document: ") || strings.HasPrefix(s, "search_query: ") {
			return nil, fmt.Errorf("payload was prefixed before Embed")
		}
		v := make([]float32, embedding.Dimensions)
		v[0] = 1
		if strings.Contains(s, "other") {
			v[0] = 0
			v[1] = 1
		}
		out = append(out, v)
	}
	return out, nil
}
func searchFixture(t *testing.T, docs ...model.Document) (*Engine, *memorySource, *index.Store, *fakeProvider) {
	t.Helper()
	idx, err := index.Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	for i := range docs {
		if docs[i].Path == "" {
			docs[i].Path = docs[i].ID + ".note.md"
		}
		if docs[i].Kind == "" {
			docs[i].Kind = model.Note
		}
		if docs[i].Raw == "" {
			docs[i].Raw = docs[i].Body
		}
		docs[i].Revision = fmt.Sprint(i, docs[i].Title, docs[i].Body)
	}
	s := &memorySource{docs: docs}
	if _, err = idx.Apply(t.Context(), model.Snapshot{Documents: docs}); err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{}
	return New(s, idx, p), s, idx, p
}
func TestReciprocalRankFusion(t *testing.T) {
	got := Fuse([]model.Hit{{ID: "a"}, {ID: "b"}}, []model.Hit{{ID: "b"}}, 2)
	if len(got) != 2 || got[0].ID != "b" {
		t.Fatalf("%+v", got)
	}
}
func TestHybridProcessesPendingFiltersAndSignatureMismatch(t *testing.T) {
	e, _, idx, p := searchFixture(t, model.Document{ID: "a", Title: "Needle", Body: strings.Repeat("界", 2000)}, model.Document{ID: "b", Title: "other", Body: "other", Path: "sub/b.note.md"})
	h, err := e.Search(t.Context(), model.SearchQuery{Text: "Needle", Mode: "keyword"})
	if err != nil || len(h) != 1 || p.calls != 0 {
		t.Fatalf("keyword: %v %v calls=%d", h, err, p.calls)
	}
	h, err = e.Search(t.Context(), model.SearchQuery{Text: "concept", Mode: "semantic", Filter: model.Filter{Folder: ".", Recursive: false}})
	if err != nil || len(h) == 0 || h[0].ID != "a" {
		t.Fatalf("semantic: %+v %v", h, err)
	}
	for _, hit := range h {
		if hit.IndexState != "current" || len(hit.Snippet) > 1024 || hit.ID != "a" {
			t.Fatalf("%+v", hit)
		}
	}
	h, err = e.Search(t.Context(), model.SearchQuery{Text: "Needle", Mode: "hybrid", Filter: model.Filter{Folder: ".", Recursive: false}})
	if err != nil || len(h) != 1 || h[0].ID != "a" || h[0].Score != 2.0/61.0 {
		t.Fatalf("hybrid did not fuse keyword and semantic evidence: %+v %v", h, err)
	}
	before := p.calls
	e.signature = "different-preprocessing-and-model"
	if err = e.RunPending(t.Context()); err != nil || p.calls <= before {
		t.Fatalf("signature mismatch not rebuilt: %v calls=%d", err, p.calls)
	}
	if n, err := idx.Pending(t.Context(), model.Filter{}, e.signature); err != nil || n != 0 {
		t.Fatalf("pending=%d %v", n, err)
	}
}
func TestSemanticFailureIsExplicitAndKeywordRemainsUsable(t *testing.T) {
	e, _, _, p := searchFixture(t, model.Document{ID: "a", Title: "needle", Body: "needle"})
	p.fail = true
	for _, mode := range []string{"semantic", "hybrid", ""} {
		if h, err := e.Search(t.Context(), model.SearchQuery{Text: "needle", Mode: mode}); model.ErrorCode(err) != "runtime_unavailable" || h != nil {
			t.Fatalf("%s: %+v %v", mode, h, err)
		}
	}
	if h, err := e.Search(t.Context(), model.SearchQuery{Text: "needle", Mode: "keyword"}); err != nil || len(h) != 1 {
		t.Fatalf("keyword %+v %v", h, err)
	}
}
func TestFusionCapsPassagesAndStableTies(t *testing.T) {
	h := []model.Hit{{ID: "a", StartLine: 1}, {ID: "a", StartLine: 2}, {ID: "a", StartLine: 3}, {ID: "b", StartLine: 1}}
	got := Fuse(nil, h, 20)
	if len(got) != 3 || got[2].ID != "b" {
		t.Fatalf("%+v", got)
	}
}

func TestSemanticLimitsRankingAndInvalidQueries(t *testing.T) {
	var docs []model.Document
	for i := 0; i < 105; i++ {
		docs = append(docs, model.Document{ID: fmt.Sprintf("%03d", i), Title: "same", Body: "body"})
	}
	e, _, _, _ := searchFixture(t, docs...)
	for _, tc := range []struct{ limit, want int }{{0, 20}, {100, 50}} {
		h, err := e.Search(t.Context(), model.SearchQuery{Text: "concept", Mode: "semantic", Limit: tc.limit})
		if err != nil || len(h) != tc.want {
			t.Fatalf("limit=%d hits=%d %v", tc.limit, len(h), err)
		}
		for i, hit := range h {
			if hit.ID != fmt.Sprintf("%03d", i) {
				t.Fatalf("unstable tie: %+v", h)
			}
		}
	}
	for _, q := range []model.SearchQuery{{Mode: "bad"}, {Limit: 101}, {Limit: -1}, {Filter: model.Filter{States: []model.State{"bad"}}}, {Text: strings.Repeat("x", 8193)}, {Text: strings.Repeat("x ", 257)}} {
		if _, err := e.Search(t.Context(), q); model.ErrorCode(err) != "invalid_argument" {
			t.Fatalf("invalid query accepted: %v", err)
		}
	}
}

func TestBusyPendingNeverReturnsStaleOrKeywordFallback(t *testing.T) {
	e, _, idx, _ := searchFixture(t, model.Document{ID: "a", Title: "needle", Body: "needle"})
	if err := idx.EnsureSignature(t.Context(), e.signature); err != nil {
		t.Fatal(err)
	}
	if j, err := idx.ClaimJob(t.Context(), model.Filter{}, e.signature, e.now); err != nil || j == nil {
		t.Fatalf("claim %v %v", j, err)
	}
	for _, mode := range []string{"semantic", "hybrid"} {
		if h, err := e.Search(t.Context(), model.SearchQuery{Text: "needle", Mode: mode}); model.ErrorCode(err) != "indexing" || h != nil {
			t.Fatalf("%s busy result=%+v %v", mode, h, err)
		}
	}
	if h, err := e.Search(t.Context(), model.SearchQuery{Text: "needle", Mode: "keyword"}); err != nil || len(h) != 1 {
		t.Fatalf("keyword %+v %v", h, err)
	}
}

func TestExactCosineOutranksStableIDAndBoundsSemanticPreview(t *testing.T) {
	body := "needle" + strings.Repeat("界", 2000)
	e, s, _, _ := searchFixture(t, model.Document{ID: "a", Title: "other", Body: "other"}, model.Document{ID: "z", Title: "Relevant", Body: body})
	h, err := e.Search(t.Context(), model.SearchQuery{Text: "concept", Mode: "semantic"})
	if err != nil || len(h) != 2 || h[0].ID != "z" || h[1].ID != "a" {
		t.Fatalf("cosine ranking: %+v %v", h, err)
	}
	if h[0].Snippet != body[:1020]+"…" || len(h[0].Snippet) > 1024 || !utf8.ValidString(h[0].Snippet) {
		t.Fatalf("invalid semantic preview: %d bytes", len(h[0].Snippet))
	}
	if s.docs[1].Body != body {
		t.Fatal("preview changed authoritative body")
	}
}

// Ascending IDs stream increasingly good candidates: the final best vector
// arrives after the heap has filled, requiring 30 actual evictions, not ties.
func TestSemanticHeapEvictsWorseCandidatesForLateBestVector(t *testing.T) {
	var docs []model.Document
	for i := 0; i < 80; i++ {
		docs = append(docs, model.Document{ID: fmt.Sprintf("%03d", i), Body: "body"})
	}
	e, _, idx, _ := searchFixture(t, docs...)
	if err := idx.EnsureSignature(t.Context(), e.signature); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 80; i++ {
		j, err := idx.ClaimJob(t.Context(), model.Filter{}, e.signature, time.Now)
		if err != nil || j == nil || j.Document.ID != fmt.Sprintf("%03d", i) {
			t.Fatalf("claim %d: %+v %v", i, j, err)
		}
		v := make([]float32, embedding.Dimensions)
		x := float64(i+1) / 80
		v[0], v[1] = float32(x), float32(math.Sqrt(1-x*x))
		ok, err := idx.Publish(t.Context(), *j, []index.Chunk{{Text: "body", EndByte: 4, StartLine: 1, EndLine: 1}}, [][]float32{v}, time.Now)
		if err != nil || !ok {
			t.Fatalf("publish: %v %v", ok, err)
		}
	}
	h, err := e.Search(t.Context(), model.SearchQuery{Text: "concept", Mode: "semantic", Limit: 100})
	if err != nil || len(h) != 50 {
		t.Fatalf("hits=%d %v", len(h), err)
	}
	for i, hit := range h {
		if hit.ID != fmt.Sprintf("%03d", 79-i) {
			t.Fatalf("rank %d: %s, want %03d", i, hit.ID, 79-i)
		}
	}
}
