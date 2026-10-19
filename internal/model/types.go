package model

import "time"

type Kind string

const (
	Note Kind = "note"
	Task Kind = "task"
)

type State string

const (
	Planned    State = "planned"
	InProgress State = "in_progress"
	Stalled    State = "stalled"
	Completed  State = "completed"
)

type Event struct {
	State State     `json:"state"`
	At    time.Time `json:"at"`
}

// Ref identifies a document by exactly one of ID or Path.
type Ref struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

type Document struct {
	ID       string  `json:"id"`
	Path     string  `json:"path"`
	Title    string  `json:"title"`
	Kind     Kind    `json:"kind"`
	State    State   `json:"state"`
	History  []Event `json:"history"`
	Body     string  `json:"body"`
	Raw      string  `json:"raw"`
	Revision string  `json:"revision"` // SHA-256 of the raw source bytes, in lowercase hex.
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

// DateField is "created", "state_changed", or "transition".
type DateField string

type Filter struct {
	Folder     string     `json:"folder"`
	Recursive  bool       `json:"recursive"`
	Kinds      []Kind     `json:"kinds"`
	States     []State    `json:"states"`
	DateField  DateField  `json:"date_field"`
	EventState State      `json:"event_state"`
	From       *time.Time `json:"from"`
	Until      *time.Time `json:"until"`
}

type TaskQuery struct {
	Filter Filter `json:"filter"`
	Sort   string `json:"sort"`
	Desc   bool   `json:"desc"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

type Page[T any] struct {
	Items      []T  `json:"items"`
	NextOffset *int `json:"next_offset"`
}

type SearchQuery struct {
	Filter Filter `json:"filter"`
	Text   string `json:"text"`
	Mode   string `json:"mode"`
	Limit  int    `json:"limit"`
}

type Hit struct {
	ID         string  `json:"id"`
	Path       string  `json:"path"`
	Title      string  `json:"title"`
	Snippet    string  `json:"snippet"`
	IndexState string  `json:"index_state"`
	Kind       Kind    `json:"kind"`
	State      State   `json:"state"`
	StartLine  int     `json:"start_line"`
	EndLine    int     `json:"end_line"`
	Score      float64 `json:"score"`
}

type SyncReport struct {
	Added       int          `json:"added"`
	Updated     int          `json:"updated"`
	Removed     int          `json:"removed"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}
