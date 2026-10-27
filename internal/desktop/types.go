// Package desktop adapts the shared application to a narrow desktop boundary.
package desktop

type Problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path"`
}
type OperationReply struct {
	Problem         *Problem `json:"problem"`
	IndexingWarning *Problem `json:"indexingWarning"`
}

type TreeEntry struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}
type TreeSnapshot struct {
	Session     string      `json:"session"`
	Entries     []TreeEntry `json:"entries"`
	Diagnostics []Problem   `json:"diagnostics"`
}
type TreeReply struct {
	Tree    *TreeSnapshot `json:"tree"`
	Problem *Problem      `json:"problem"`
}
type CreateInput struct {
	Session string `json:"session"`
	Kind    string `json:"kind"`
	Parent  string `json:"parent"`
	Name    string `json:"name"`
}
type CreateReply struct {
	Entry           *TreeEntry    `json:"entry"`
	Document        *DocumentView `json:"document"`
	Problem         *Problem      `json:"problem"`
	IndexingWarning *Problem      `json:"indexingWarning"`
}
type DeleteInput struct {
	Session          string `json:"session"`
	ID               string `json:"id"`
	Path             string `json:"path"`
	ExpectedRevision string `json:"expectedRevision"`
	Recursive        bool   `json:"recursive"`
}

type DocumentView struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Name     string `json:"name"`
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	State    string `json:"state"`
	Body     string `json:"body"`
	Revision string `json:"revision"`
}
type CollectionReply struct {
	Session string   `json:"session"`
	Root    string   `json:"root"`
	Problem *Problem `json:"problem"`
}
type DocumentReply struct {
	Document *DocumentView `json:"document"`
	Problem  *Problem      `json:"problem"`
}
type SaveBodyInput struct {
	Session          string `json:"session"`
	ID               string `json:"id"`
	ExpectedRevision string `json:"expectedRevision"`
	Body             string `json:"body"`
	Generation       uint64 `json:"generation"`
}
type SaveBodyReply struct {
	Document        *DocumentView `json:"document"`
	Generation      uint64        `json:"generation"`
	Saved           bool          `json:"saved"`
	Problem         *Problem      `json:"problem"`
	IndexingWarning *Problem      `json:"indexingWarning"`
}
