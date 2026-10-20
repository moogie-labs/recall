package search

import (
	"context"
	"time"

	"recall/internal/document"
	"recall/internal/fsstore"
	"recall/internal/index"
	"recall/internal/model"
)

func (e *Engine) process(ctx context.Context, j index.EmbeddingJob) error {
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		e.index.ReleaseJob(cleanup, j)
	}()
	// Renewal continues through tokenization, native startup and embedding. No
	// source lock is held; a lost or expired lease cancels work, never publishes.
	done := make(chan error, 1)
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(e.renewEvery)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				done <- nil
				return
			case <-work.Done():
				done <- nil
				return
			case <-ticker.C:
				select {
				case <-stop:
					done <- nil
					return
				default:
				}
				ok, err := e.index.RenewJob(work, j, e.now)
				if err != nil || !ok {
					if err == nil {
						err = indexing()
					}
					done <- err
					cancel()
					return
				}
			}
		}
	}()
	stopped := false
	var renewErr error
	stopRenewal := func() error {
		if !stopped {
			close(stop)
			renewErr = <-done
			stopped = true
		}
		return renewErr
	}
	var passages []index.Chunk
	var vectors [][]float32
	err := func() error {
		if e.provider == nil {
			return runtimeUnavailable()
		}
		var err error
		passages, err = chunks(work, e.provider, j.Document)
		if err != nil {
			return err
		}
		for start := 0; start < len(passages); start += 4 {
			end := min(start+4, len(passages))
			payloads := make([]string, end-start)
			for i := start; i < end; i++ {
				payloads[i-start] = passages[i].Payload
			}
			batch, err := e.provider.Embed(work, "document", payloads)
			if err != nil {
				return err
			}
			if len(batch) != len(payloads) {
				return runtimeUnavailable()
			}
			for _, v := range batch {
				if !validVector(v) {
					return runtimeUnavailable()
				}
			}
			vectors = append(vectors, batch...)
		}
		return nil
	}()
	if err == nil {
		err = e.source.WithLock(work, func() error {
			// Resolve by ID so a parent/path-only move can reuse this inference,
			// and a newly duplicated identity cannot silently pick the old path.
			current, err := e.source.Read(work, model.Ref{ID: j.Document.ID})
			if err != nil {
				if model.ErrorCode(err) == "not_found" {
					return indexing()
				}
				return err
			}
			if current.ID != j.Document.ID || current.Title != j.Document.Title || current.Body != j.Document.Body {
				return indexing()
			}
			// Join renewal before deleting its job. Otherwise an in-flight renew
			// can mistake our successful publication for lease loss. We continue
			// renewing while waiting for the source lock and checking existence.
			if err := stopRenewal(); err != nil {
				return err
			}
			ok, err := e.index.Publish(work, j, passages, vectors, e.now)
			if err != nil {
				return err
			}
			if !ok {
				return indexing()
			}
			return nil
		})
	}
	stopRenewal()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if renewErr != nil {
		return renewErr
	}
	return err
}

// storeSource reads confined source bytes directly, without an app read/sync or
// a nested lock. The engine owns WithLock for final validation/publication.
type storeSource struct{ store *fsstore.Store }

func NewStoreSource(store *fsstore.Store) Source { return &storeSource{store: store} }
func (s *storeSource) WithLock(ctx context.Context, fn func() error) error {
	return s.store.WithLock(ctx, fn)
}
func (s *storeSource) Read(ctx context.Context, ref model.Ref) (model.Document, error) {
	if (ref.ID == "") == (ref.Path == "") {
		return model.Document{}, invalid("supply exactly one document ID or path")
	}
	read := func(path string) (model.Document, error) {
		raw, err := s.store.Read(ctx, path)
		if err != nil {
			return model.Document{}, err
		}
		p, err := document.Parse(path, raw)
		return p.Document, err
	}
	if ref.Path != "" {
		return read(ref.Path)
	}
	entries, err := s.store.Walk(ctx)
	if err != nil {
		return model.Document{}, err
	}
	var found *model.Document
	for _, entry := range entries {
		if entry.IsDir || entry.IsSymlink {
			continue
		}
		if _, err := document.KindFromPath(entry.Path); err != nil {
			continue
		}
		d, err := read(entry.Path)
		if err != nil {
			continue
		}
		if d.ID != ref.ID {
			continue
		}
		if found != nil {
			return model.Document{}, &model.Error{Code: "duplicate_id", Message: "ID is used by multiple source files"}
		}
		found = &d
	}
	if found == nil {
		return model.Document{}, &model.Error{Code: "not_found", Message: "document not found"}
	}
	return *found, nil
}
