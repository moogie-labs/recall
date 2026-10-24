package model

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// Budgets measure compact encoding/json data, before CLI/MCP envelopes and the
// SDK's duplicated text/structured content. Source and explicit gets are exempt.
const (
	TitlePreviewBytes = 256
	PreviewRowBytes   = 4 * 1024
	PreviewDataBytes  = 32 * 1024
)

// ClipPreview repairs display text only, including the ellipsis in the budget.
// Never apply it to a reference, stored source, fingerprint or inference input.
func ClipPreview(text string, budget int) (string, bool) {
	text = strings.ToValidUTF8(text, "�")
	if len(text) <= budget {
		return text, false
	}
	end := budget - len("…")
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + "…", true
}

func outputTooLarge() *Error {
	// No authored values: even an unrepresentable/huge reference is safe here.
	return &Error{Code: "output_too_large", Message: "Preview or error output cannot fit the byte budget with exact references; reduce limit, narrow scope, or explicitly get full content by path"}
}

func checkEncoded(value any, budget int) error {
	b, err := json.Marshal(value)
	if err != nil || len(b) > budget {
		return outputTooLarge()
	}
	return nil
}

// CheckPreviewRow never clips identities. JSON would silently replace invalid
// UTF-8, so reject it rather than handing a client a different usable reference.
func CheckPreviewRow(row any, identities ...string) error {
	for _, identity := range identities {
		if !utf8.ValidString(identity) {
			return outputTooLarge()
		}
	}
	return checkEncoded(row, PreviewRowBytes)
}

// CheckPreviewData rejects a whole page; it never removes continuation items or
// auxiliary arrays. Callers retain their existing ranking and cursor semantics.
func CheckPreviewData(data any) error { return checkEncoded(data, PreviewDataBytes) }

func PreviewHit(hit Hit) (Hit, error) {
	var truncated bool
	hit.Title, truncated = ClipPreview(hit.Title, TitlePreviewBytes)
	hit.TitleTruncated = hit.TitleTruncated || truncated
	return hit, CheckPreviewRow(hit, hit.ID, hit.Path, hit.Revision)
}

func Summaries(documents []Document) ([]DocumentSummary, error) {
	rows := make([]DocumentSummary, len(documents))
	for n, document := range documents {
		rows[n] = Summarize(document)
		if err := CheckPreviewRow(rows[n], document.ID, document.Path, document.Revision); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

func CheckDiagnostics(diagnostics []Diagnostic) error {
	for _, d := range diagnostics {
		if err := CheckPreviewRow(d, d.Path); err != nil {
			return err
		}
	}
	return nil
}

func CheckFolders(folders []string) error {
	for _, folder := range folders {
		if err := CheckPreviewRow(folder, folder); err != nil {
			return err
		}
	}
	return nil
}
