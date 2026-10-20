package search

import (
	"container/heap"
	"context"
	"math"
	"sort"

	"recall/internal/embedding"
	"recall/internal/model"
)

func validVector(v []float32) bool {
	if len(v) != embedding.Dimensions {
		return false
	}
	var norm float64
	for _, f := range v {
		x := float64(f)
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return false
		}
		norm += x * x
	}
	return norm > 0
}
func cosine(a, b []float32) float64 {
	var dot, aa, bb float64
	for i, x := range a {
		y := float64(b[i])
		v := float64(x)
		dot += v * y
		aa += v * v
		bb += y * y
	}
	return dot / math.Sqrt(aa*bb)
}
func better(a, b model.Hit) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	if a.ID != b.ID {
		return a.ID < b.ID
	}
	if a.StartLine != b.StartLine {
		return a.StartLine < b.StartLine
	}
	if a.EndLine != b.EndLine {
		return a.EndLine < b.EndLine
	}
	return a.Snippet < b.Snippet
}

type hitHeap []model.Hit

func (h hitHeap) Len() int           { return len(h) }
func (h hitHeap) Less(i, j int) bool { return better(h[j], h[i]) }
func (h hitHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *hitHeap) Push(v any)        { *h = append(*h, v.(model.Hit)) }
func (h *hitHeap) Pop() any          { old := *h; v := old[len(old)-1]; *h = old[:len(old)-1]; return v }

func (e *Engine) semantic(ctx context.Context, f model.Filter, q []float32) ([]model.Hit, error) {
	h := &hitHeap{}
	err := e.index.StreamVectors(ctx, f, e.signature, func(hit model.Hit, v []float32) error {
		hit.Score = cosine(q, v)
		if h.Len() < 50 {
			heap.Push(h, hit)
		} else if better(hit, (*h)[0]) {
			heap.Pop(h)
			heap.Push(h, hit)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(*h, func(i, j int) bool { return better((*h)[i], (*h)[j]) })
	return []model.Hit(*h), nil
}

// Fuse aggregates evidence by document. Keyword evidence boosts the best
// semantic passage; additional passages keep their own rank evidence. This
// avoids duplicate uncited keyword hits while retaining two useful citations.
func Fuse(keyword, semantic []model.Hit, limit int) []model.Hit {
	if limit <= 0 {
		return []model.Hit{}
	}
	keywordScores := map[string]float64{}
	keywordHits := map[string]model.Hit{}
	for i, h := range keyword {
		if i >= 50 {
			break
		}
		if _, ok := keywordHits[h.ID]; !ok {
			keywordScores[h.ID] = 1 / float64(60+i+1)
			keywordHits[h.ID] = h
		}
	}
	var all []model.Hit
	seen := map[string]bool{}
	for i, h := range semantic {
		if i >= 50 {
			break
		}
		h.Score = 1 / float64(60+i+1)
		if !seen[h.ID] {
			h.Score += keywordScores[h.ID]
		}
		seen[h.ID] = true
		all = append(all, h)
	}
	for id, h := range keywordHits {
		if !seen[id] {
			h.Score = keywordScores[id]
			all = append(all, h)
		}
	}
	sort.Slice(all, func(i, j int) bool { return better(all[i], all[j]) })
	result := []model.Hit{}
	counts := map[string]int{}
	for _, h := range all {
		if counts[h.ID] >= 2 {
			continue
		}
		counts[h.ID]++
		result = append(result, h)
		if len(result) == limit {
			break
		}
	}
	return result
}
