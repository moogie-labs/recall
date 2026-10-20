package model

import "time"

// DocumentSummary is the bounded metadata projection used by list adapters.
// Full source and event history remain available through explicit retrieval.
type DocumentSummary struct {
	ID             string     `json:"id"`
	Path           string     `json:"path"`
	Title          string     `json:"title"`
	Kind           Kind       `json:"kind"`
	State          State      `json:"state"`
	Revision       string     `json:"revision"`
	CreatedAt      *time.Time `json:"created_at,omitempty"`
	StateChangedAt *time.Time `json:"state_changed_at,omitempty"`
}

func Summarize(document Document) DocumentSummary {
	summary := DocumentSummary{ID: document.ID, Path: document.Path, Title: document.Title, Kind: document.Kind, State: document.State, Revision: document.Revision}
	if document.Kind == Task && len(document.History) > 0 {
		created, changed := document.History[0].At, document.History[len(document.History)-1].At
		summary.CreatedAt, summary.StateChangedAt = &created, &changed
	}
	return summary
}
