package model

// Ref identifies a document by exactly one of ID or Path.
type Ref struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

type Document struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Raw      string `json:"raw"`
	Revision string `json:"revision"` // SHA-256 of the raw source bytes, in lowercase hex.
}

type Diagnostic struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

type Snapshot struct {
	Documents   []Document   `json:"documents"`
	Diagnostics []Diagnostic `json:"diagnostics"`
	Folders     []string     `json:"folders"`
}

type Filter struct {
	Folder    string `json:"folder"`
	Recursive bool   `json:"recursive"`
}

type SearchQuery struct {
	Filter Filter `json:"filter"`
	Text   string `json:"text"`
	Mode   string `json:"mode"`
	Limit  int    `json:"limit"`
}

type Hit struct {
	ID             string  `json:"id"`
	Path           string  `json:"path"`
	Title          string  `json:"title"`
	TitleTruncated bool    `json:"title_truncated" jsonschema:"True when title is a display preview; explicitly get the document for its full title"`
	Snippet        string  `json:"snippet"`
	IndexState     string  `json:"index_state"`
	Revision       string  `json:"revision"` // Raw-source revision captured with this result.
	StartLine      int     `json:"start_line"`
	EndLine        int     `json:"end_line"`
	Score          float64 `json:"score"`
}

type SyncReport struct {
	Added       int          `json:"added"`
	Updated     int          `json:"updated"`
	Removed     int          `json:"removed"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}
