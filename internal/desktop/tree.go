package desktop

import (
	"path/filepath"
	"recall/internal/app"
	"recall/internal/model"
	"sort"
	"strings"
)

func entry(d model.Document) TreeEntry {
	v := view(d)
	return TreeEntry{ID: v.ID, Path: v.Path, Name: v.Name, Kind: v.Kind}
}
func (s *Service) Tree(session string) TreeReply {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p := s.check(session); p != nil {
		return TreeReply{Problem: p}
	}
	snapshot, err := s.application.List(s.ctx, "", true)
	if err != nil {
		return TreeReply{Problem: problem(err)}
	}
	tree := &TreeSnapshot{Session: session, Entries: []TreeEntry{}, Diagnostics: []Problem{}}
	for _, path := range snapshot.Folders {
		if path != "." && path != "" {
			tree.Entries = append(tree.Entries, TreeEntry{ID: "folder:" + path, Path: path, Name: filepath.Base(path), Kind: "folder"})
		}
	}
	for _, d := range snapshot.Documents {
		tree.Entries = append(tree.Entries, entry(d))
	}
	for _, d := range snapshot.Diagnostics {
		tree.Diagnostics = append(tree.Diagnostics, Problem{Code: d.Code, Message: d.Message, Path: d.Path})
	}
	sort.Slice(tree.Entries, func(i, j int) bool {
		a, b := tree.Entries[i], tree.Entries[j]
		if (a.Kind == "folder") != (b.Kind == "folder") {
			return a.Kind == "folder"
		}
		an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if an != bn {
			return an < bn
		}
		return a.Path < b.Path
	})
	return TreeReply{Tree: tree}
}
func validComponent(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\x00")
}
func (s *Service) Create(in CreateInput) CreateReply {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p := s.check(in.Session); p != nil {
		return CreateReply{Problem: p}
	}
	if !validComponent(in.Name) {
		return CreateReply{Problem: invalid("Name must be a single nonempty filename component")}
	}
	if in.Kind == "folder" {
		path := in.Name
		if in.Parent != "" {
			path = in.Parent + "/" + in.Name
		}
		err := s.application.CreateFolder(s.ctx, path)
		if err != nil {
			return CreateReply{Problem: problem(err)}
		}
		return CreateReply{Entry: &TreeEntry{ID: "folder:" + path, Path: path, Name: in.Name, Kind: "folder"}}
	}
	d, err := s.application.Create(s.ctx, app.CreateInput{Kind: model.Kind(in.Kind), Folder: in.Parent, Name: in.Name})
	if err != nil && d.ID == "" {
		return CreateReply{Problem: problem(err)}
	}
	e := entry(d)
	return CreateReply{Entry: &e, Document: view(d), IndexingWarning: problem(err)}
}
func (s *Service) Delete(in DeleteInput) OperationReply {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p := s.check(in.Session); p != nil {
		return OperationReply{Problem: p}
	}
	if strings.HasPrefix(in.ID, "folder:") {
		if in.Path == "" || in.Path == "." || in.ID != "folder:"+in.Path {
			return OperationReply{Problem: invalid("The collection root cannot be deleted")}
		}
		return OperationReply{Problem: problem(s.application.DeleteFolder(s.ctx, in.Path, in.Recursive))}
	}
	if in.ExpectedRevision == "" {
		return OperationReply{Problem: invalid("A current source revision is required to delete a document")}
	}
	return OperationReply{Problem: problem(s.application.Delete(s.ctx, model.Ref{ID: in.ID}, in.ExpectedRevision))}
}
