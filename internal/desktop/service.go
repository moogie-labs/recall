package desktop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"

	"recall/internal/app"
	"recall/internal/config"
	"recall/internal/model"
)

type Application interface {
	Read(context.Context, model.Ref) (model.Document, error)
	Update(context.Context, model.Ref, string, string) (model.Document, error)
	Create(context.Context, app.CreateInput) (model.Document, error)
	CreateFolder(context.Context, string) error
	Delete(context.Context, model.Ref, string) error
	DeleteFolder(context.Context, string, bool) error
	List(context.Context, string, bool) (model.Snapshot, error)
	Search(context.Context, model.SearchQuery) ([]model.Hit, error)
	Sync(context.Context) (model.SyncReport, error)
	IndexPending(context.Context) error
	Close() error
}
type Preferences interface {
	LoadRoot() (string, error)
	SaveRoot(string) error
}
type Service struct {
	mu          sync.RWMutex
	open        func(context.Context, config.Config) (Application, error)
	preferences Preferences
	application Application
	cfg         config.Config
	session     string
	ctx         context.Context
	cancel      context.CancelFunc
}

func NewService(open func(context.Context, config.Config) (Application, error), preferences Preferences) *Service {
	return &Service{open: open, preferences: preferences, ctx: context.Background()}
}
func problem(err error) *Problem {
	if err == nil {
		return nil
	}
	p := model.PublicError(err)
	return &Problem{Code: p.Code, Message: p.Message, Path: p.Path}
}
func invalid(message string) *Problem { return &Problem{Code: "invalid_argument", Message: message} }
func (s *Service) check(session string) *Problem {
	if session == "" || session != s.session || s.application == nil {
		return &Problem{Code: "conflict", Message: "Collection session changed; reopen the collection"}
	}
	return nil
}
func (s *Service) OpenCollection(root string) CollectionReply {
	s.mu.Lock()
	defer s.mu.Unlock()
	if root == "" && s.preferences != nil {
		var err error
		root, err = s.preferences.LoadRoot()
		if err != nil {
			return CollectionReply{Problem: problem(err)}
		}
	}
	cfg, err := config.Resolve(root)
	if err != nil {
		return CollectionReply{Problem: problem(err)}
	}
	ctx, cancel := context.WithCancel(context.Background())
	a, err := s.open(ctx, cfg)
	if err != nil {
		cancel()
		return CollectionReply{Root: cfg.Root, Problem: problem(err)}
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.application != nil {
		s.application.Close()
	}
	var token [16]byte
	rand.Read(token[:])
	s.session = hex.EncodeToString(token[:])
	s.cfg = cfg
	s.application = a
	s.ctx = ctx
	s.cancel = cancel
	return CollectionReply{Session: s.session, Root: cfg.Root}
}
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	s.session = ""
	if s.application == nil {
		return nil
	}
	err := s.application.Close()
	s.application = nil
	return err
}
