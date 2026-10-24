package index

import (
	"context"
	"strings"

	"recall/internal/model"
)

// Keyword performs FTS5 retrieval without exposing MATCH's query language.
// Whitespace-separated terms are quoted literal phrases and combined with AND;
// punctuation follows FTS5's unicode61 tokenization, not operators or SQL syntax.
func (s *Store) Keyword(ctx context.Context, query model.SearchQuery) ([]model.Hit, error) {
	hits := []model.Hit{}
	err := s.StreamKeyword(ctx, query, func(hit model.Hit) error {
		hits = append(hits, hit)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := model.CheckPreviewData(hits); err != nil {
		return nil, err
	}
	return hits, nil
}

// StreamKeyword supplies row-bounded evidence in rank order, like StreamVectors.
// Fusion can inspect its fixed candidate pool without treating hidden evidence
// as a public page; the final returned page still needs CheckPreviewData.
func (s *Store) StreamKeyword(ctx context.Context, query model.SearchQuery, visit func(model.Hit) error) error {
	limit, err := queryLimit(query.Limit, 20, 100)
	if err != nil {
		return err
	}
	if query.Mode != "" && query.Mode != "keyword" {
		return invalid("keyword index only supports keyword mode")
	}
	where, args, err := filterSQL(query.Filter)
	if err != nil {
		return err
	}
	terms := strings.Fields(query.Text)
	if len(terms) == 0 {
		return nil
	}
	for i, term := range terms {
		terms[i] = `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
	}
	args = append([]any{strings.Join(terms, " AND ")}, args...)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT d.id, d.path, d.title, d.kind, d.state, d.revision,
		snippet(document_fts, 5, '', '', ' … ', 32),
		-bm25(document_fts, 0, 10, 3, 1, 1, 1)
		FROM document_fts JOIN documents d ON d.id = document_fts.id
		WHERE document_fts MATCH ? AND `+where+`
		ORDER BY bm25(document_fts, 0, 10, 3, 1, 1, 1) ASC, d.id ASC LIMIT ?`, args...)
	if err != nil {
		return unavailable(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		var hit model.Hit
		if err := rows.Scan(&hit.ID, &hit.Path, &hit.Title, &hit.Kind, &hit.State, &hit.Revision, &hit.Snippet, &hit.Score); err != nil {
			return unavailable(ctx, err)
		}
		// Repair only the preview, never the byte-preserved cached source.
		hit.Snippet = preview(hit.Snippet)
		hit.IndexState = "current"
		hit, err = model.PreviewHit(hit)
		if err != nil {
			return err
		}
		// FTS5 snippets may span disjoint passages and metadata-only matches;
		// zero line bounds mean no exact source-line location is available.
		if err := visit(hit); err != nil {
			return err
		}
	}
	return unavailable(ctx, rows.Err())
}
