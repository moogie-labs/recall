package model

// DocumentSummary is the display projection used by list adapters. Summaries
// also validates exact references/serialized rows before exposing them publicly.
// Full source remains available through explicit retrieval.
type DocumentSummary struct {
	ID             string `json:"id"`
	Path           string `json:"path"`
	Title          string `json:"title"`
	TitleTruncated bool   `json:"title_truncated" jsonschema:"True when title is a display preview; explicitly get the document for its full title"`
	Revision       string `json:"revision"`
}

func Summarize(document Document) DocumentSummary {
	summary := DocumentSummary{ID: document.ID, Path: document.Path, Title: document.Title, Revision: document.Revision}
	summary.Title, summary.TitleTruncated = ClipPreview(document.Title, TitlePreviewBytes)
	return summary
}
