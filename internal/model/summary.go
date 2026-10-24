package model

import "time"

// DocumentSummary is the display projection used by list adapters. Summaries
// also validates exact references/serialized rows before exposing them publicly.
// Full source and event history remain available through explicit retrieval.
type DocumentSummary struct {
	ID             string     `json:"id"`
	Path           string     `json:"path"`
	Title          string     `json:"title"`
	TitleTruncated bool       `json:"title_truncated" jsonschema:"True when title is a display preview; explicitly get the document for its full title"`
	Kind           Kind       `json:"kind"`
	State          State      `json:"state"`
	Revision       string     `json:"revision"`
	CreatedAt      *time.Time `json:"created_at,omitempty"`
	StateChangedAt *time.Time `json:"state_changed_at,omitempty"`
}

func Summarize(document Document) DocumentSummary {
	summary := DocumentSummary{ID: document.ID, Path: document.Path, Title: document.Title, Kind: document.Kind, State: document.State, Revision: document.Revision}
	summary.Title, summary.TitleTruncated = ClipPreview(document.Title, TitlePreviewBytes)
	if document.Kind == Task && len(document.History) > 0 {
		created, changed := document.History[0].At, document.History[len(document.History)-1].At
		summary.CreatedAt, summary.StateChangedAt = &created, &changed
	}
	return summary
}
