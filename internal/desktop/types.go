// Package desktop adapts the shared application to a narrow desktop boundary.
package desktop

type Problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path"`
}
type OperationReply struct {
	Problem *Problem `json:"problem"`
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
