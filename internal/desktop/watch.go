package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"recall/internal/config"
	"recall/internal/syncer"
)

type CollectionEvent struct {
	Session string   `json:"session"`
	Problem *Problem `json:"problem"`
}

func (s *Service) SetEventSink(sink func(string, any)) {
	s.sinkMu.Lock()
	s.sink = sink
	s.sinkMu.Unlock()
}
func (s *Service) emit(name string, payload any) {
	s.sinkMu.RLock()
	sink := s.sink
	s.sinkMu.RUnlock()
	if sink != nil {
		sink(name, payload)
	}
}
func (s *Service) stopWorkers() {
	s.mu.RLock()
	cancel := s.cancel
	s.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
	s.workers.Wait()
}
func (s *Service) startWorkers(ctx context.Context, a Application, cfg config.Config, session string) {
	s.sinkMu.RLock()
	enabled := s.sink != nil
	s.sinkMu.RUnlock()
	if !enabled {
		return
	}
	work := make(chan struct{}, 1)
	wake := func() {
		select {
		case work <- struct{}{}:
		default:
		}
	}
	s.workers.Add(2)
	go func() {
		defer s.workers.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-work:
				if err := a.IndexPending(ctx); err != nil && ctx.Err() == nil {
					s.emit("desktop:collection-warning", CollectionEvent{Session: session, Problem: problem(err)})
				}
			}
		}
	}()
	go func() {
		defer s.workers.Done()
		var previous [32]byte
		report := func(err error) {
			if err != nil && ctx.Err() == nil {
				s.emit("desktop:collection-warning", CollectionEvent{Session: session, Problem: problem(err)})
			}
		}
		err := syncer.Run(ctx, cfg.Content, func(ctx context.Context) error {
			snapshot, err := a.List(ctx, "", true)
			if err != nil {
				return err
			}
			// Hash identities/revisions, folders and diagnostics, never transfer bodies in events.
			identities := make([][3]string, 0, len(snapshot.Documents))
			for _, d := range snapshot.Documents {
				identities = append(identities, [3]string{d.ID, d.Path, d.Revision})
			}
			bytes, _ := json.Marshal([]any{identities, snapshot.Folders, snapshot.Diagnostics})
			signature := sha256.Sum256(bytes)
			if signature != previous {
				previous = signature
				if ctx.Err() == nil {
					s.emit("desktop:collection-changed", CollectionEvent{Session: session})
					wake()
				}
			}
			return nil
		}, report)
		report(err)
	}()
}
func (s *Service) Refresh(session string) TreeReply { return s.Tree(session) }
