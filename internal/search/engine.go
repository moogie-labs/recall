// Package search supplies incremental, offline semantic and hybrid retrieval.
package search

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"recall/assets"
	"recall/internal/embedding"
	"recall/internal/index"
	"recall/internal/model"
)

type Source interface {
	Read(context.Context, model.Ref) (model.Document, error)
	WithLock(context.Context, func() error) error
}

type Engine struct {
	source     Source
	index      *index.Store
	provider   embedding.Provider
	signature  string
	now        func() time.Time
	renewEvery time.Duration
}

func New(source Source, idx *index.Store, provider embedding.Provider) *Engine {
	return &Engine{source: source, index: idx, provider: provider, signature: pinnedSignature(), now: time.Now, renewEvery: index.JobLease / 3}
}

// Config.ModelSignature is weights-only. This separate versioned identity also
// covers every transformation that can change stored vectors or passage layout.
func pinnedSignature() string {
	var manifest struct {
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal([]byte(assets.ModelLock()), &manifest); err != nil || len(manifest.SHA256) != 64 {
		panic("invalid embedded model manifest")
	}
	docPrefix, _ := embedding.FormatInput("document", "")
	queryPrefix, _ := embedding.FormatInput("query", "")
	policy := fmt.Sprintf("recall-vector-v1\nsha256:%s\n%s\n%s\npooling=mean\nnormalization=l2\ndimensions=%d\ntokens=%d,title=64,overlap=32\nraw=%d,title=%d,overlap=%d\noriginal-utf8-binary-slices;title-newline-newline-body;special-tokens;source-lines", manifest.SHA256, docPrefix, queryPrefix, embedding.Dimensions, embedding.MaxTokens, maxInputBytes, maxTitleBytes, maxOverlapBytes)
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(policy)))
}

func (e *Engine) RunPending(ctx context.Context) error { return e.runPending(ctx, model.Filter{}) }
func (e *Engine) runPending(ctx context.Context, f model.Filter) error {
	if err := e.index.EnsureSignature(ctx, e.signature); err != nil {
		return err
	}
	for {
		j, err := e.index.ClaimJob(ctx, f, e.signature, e.now)
		if err != nil {
			return err
		}
		if j == nil {
			break
		}
		if err = e.process(ctx, *j); err != nil {
			return err
		}
	}
	n, err := e.index.Pending(ctx, f, e.signature)
	if err != nil {
		return err
	}
	if n != 0 {
		return indexing()
	}
	return nil
}

func (e *Engine) Search(ctx context.Context, q model.SearchQuery) ([]model.Hit, error) {
	if q.Limit < 0 || q.Limit > 100 {
		return nil, invalid("search limit outside supported range")
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.Mode == "keyword" {
		return e.index.Keyword(ctx, q)
	}
	if q.Mode != "" && q.Mode != "hybrid" && q.Mode != "semantic" {
		return nil, invalid("unsupported search mode")
	}
	// Validate filters even for an empty query, without starting inference.
	if _, err := e.index.Pending(ctx, q.Filter, e.signature); err != nil {
		return nil, err
	}
	if strings.TrimSpace(q.Text) == "" {
		return []model.Hit{}, nil
	}
	formatted, _ := embedding.FormatInput("query", q.Text)
	if len(formatted) > maxInputBytes {
		return nil, invalid("search query exceeds input byte budget")
	}
	if e.provider == nil {
		return nil, runtimeUnavailable()
	}
	if err := e.runPending(ctx, q.Filter); err != nil {
		return nil, err
	}
	tokens, err := e.provider.Tokenize(ctx, formatted)
	if err != nil {
		return nil, err
	}
	if len(tokens) > embedding.MaxTokens {
		return nil, invalid("search query exceeds 256 model tokens")
	}
	vectors, err := e.provider.Embed(ctx, "query", []string{q.Text})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 || !validVector(vectors[0]) {
		return nil, runtimeUnavailable()
	}
	var result []model.Hit
	err = e.source.WithLock(ctx, func() error {
		if n, err := e.index.Pending(ctx, q.Filter, e.signature); err != nil {
			return err
		} else if n != 0 {
			return indexing()
		}
		semantic, err := e.semantic(ctx, q.Filter, vectors[0])
		if err != nil {
			return err
		}
		var keyword []model.Hit
		if q.Mode != "semantic" {
			kq := q
			kq.Mode = "keyword"
			kq.Limit = 50
			keyword, err = e.index.Keyword(ctx, kq)
			if err != nil {
				return err
			}
		}
		result = Fuse(keyword, semantic, q.Limit)
		// No inference remains. Verify visible source revisions under the same
		// lock as publication; an external edit may have bypassed app sync while
		// query inference was running. Never mark such an excerpt "current".
		checked := map[string]bool{}
		for _, hit := range result {
			if checked[hit.ID] {
				continue
			}
			d, err := e.source.Read(ctx, model.Ref{Path: hit.Path})
			if err != nil {
				if model.ErrorCode(err) == "not_found" {
					return indexing()
				}
				return err
			}
			if d.ID != hit.ID || d.Revision != hit.Revision {
				return indexing()
			}
			current, err := e.index.SourceCurrent(ctx, d)
			if err != nil {
				return err
			}
			if !current {
				return indexing()
			}
			checked[hit.ID] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func invalid(message string) error { return &model.Error{Code: "invalid_argument", Message: message} }
func indexing() error {
	return &model.Error{Code: "indexing", Message: "semantic index is updating; retry or use keyword mode"}
}
func runtimeUnavailable() error {
	return &model.Error{Code: "runtime_unavailable", Message: "local embedding provider unavailable; use keyword mode"}
}
