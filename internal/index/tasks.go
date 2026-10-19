package index

import (
	"context"
	"database/sql"
	"path"
	"strings"
	"time"

	"recall/internal/model"
)

// Tasks returns current source-backed documents, with history in source order.
// Empty sort defaults to title ascending. Ties always use ascending stable IDs.
func (s *Store) Tasks(ctx context.Context, query model.TaskQuery) (model.Page[model.Document], error) {
	page := model.Page[model.Document]{Items: []model.Document{}}
	limit, err := queryLimit(query.Limit, 50, 200)
	if err != nil {
		return page, err
	}
	if query.Offset < 0 {
		return page, invalid("offset must be nonnegative")
	}
	column := map[string]string{"": "d.title", "title": "d.title", "created": "d.created_at", "state_changed": "d.state_changed_at"}[query.Sort]
	if column == "" {
		return page, invalid("unsupported task sort")
	}
	where, args, err := filterSQL(query.Filter)
	if err != nil {
		return page, err
	}
	direction := " ASC"
	if query.Desc {
		direction = " DESC"
	}
	args = append(args, limit+1, query.Offset)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, unavailable(ctx, err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT d.id, d.path, d.title, d.kind, d.state, d.body, d.raw, d.revision
		FROM documents d WHERE d.kind = 'task' AND `+where+` ORDER BY `+column+direction+`, d.id ASC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return page, unavailable(ctx, err)
	}
	for rows.Next() {
		var d model.Document
		if err := rows.Scan(&d.ID, &d.Path, &d.Title, &d.Kind, &d.State, &d.Body, &d.Raw, &d.Revision); err != nil {
			rows.Close()
			return page, unavailable(ctx, err)
		}
		page.Items = append(page.Items, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, unavailable(ctx, err)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		next := query.Offset + limit
		page.NextOffset = &next
	}
	if err := loadHistory(ctx, tx, page.Items); err != nil {
		return model.Page[model.Document]{}, unavailable(ctx, err)
	}
	if err := tx.Commit(); err != nil {
		return model.Page[model.Document]{}, unavailable(ctx, err)
	}
	return page, nil
}

func loadHistory(ctx context.Context, tx *sql.Tx, documents []model.Document) error {
	if len(documents) == 0 {
		return nil
	}
	byID := make(map[string]int, len(documents))
	args := make([]any, 0, len(documents))
	for i, d := range documents {
		byID[d.ID] = i
		args = append(args, d.ID)
	}
	rows, err := tx.QueryContext(ctx, "SELECT document_id, state, at FROM state_events WHERE document_id IN ("+placeholders(len(args))+") ORDER BY document_id, ordinal", args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, at string
		var event model.Event
		if err := rows.Scan(&id, &event.State, &at); err != nil {
			return err
		}
		event.At, err = time.Parse(timestampLayout, at)
		if err != nil {
			return err
		}
		i := byID[id]
		documents[i].History = append(documents[i].History, event)
	}
	return rows.Err()
}

// filterSQL is shared by task and keyword queries. All caller-supplied values
// are bound; folder prefix comparisons deliberately avoid LIKE wildcards.
func filterSQL(f model.Filter) (string, []any, error) {
	clauses := []string{"1 = 1"}
	args := []any{}
	if f.Folder != "" {
		if path.IsAbs(f.Folder) || strings.IndexByte(f.Folder, 0) >= 0 {
			return "", nil, invalid("folder must be collection-relative")
		}
		for _, segment := range strings.Split(f.Folder, "/") {
			if segment == ".." {
				return "", nil, invalid("folder must not traverse parents")
			}
		}
		folder := path.Clean(f.Folder)
		if !f.Recursive {
			clauses = append(clauses, "d.folder = ?")
			args = append(args, folder)
		} else if folder != "." {
			prefix := folder + "/"
			clauses = append(clauses, "substr(d.path, 1, length(?)) = ?")
			args = append(args, prefix, prefix)
		}
	}
	if len(f.Kinds) > 0 {
		for _, kind := range f.Kinds {
			if kind != model.Note && kind != model.Task {
				return "", nil, invalid("unsupported document kind")
			}
			args = append(args, kind)
		}
		clauses = append(clauses, "d.kind IN ("+placeholders(len(f.Kinds))+")")
	}
	if len(f.States) > 0 {
		for _, state := range f.States {
			if !validState(state) {
				return "", nil, invalid("unsupported task state")
			}
			args = append(args, state)
		}
		clauses = append(clauses, "d.state IN ("+placeholders(len(f.States))+")")
	}
	if f.From != nil && f.Until != nil && !f.From.Before(*f.Until) {
		return "", nil, invalid("date range must have from before until")
	}
	column := ""
	switch f.DateField {
	case "":
		if f.From != nil || f.Until != nil || f.EventState != "" {
			return "", nil, invalid("date bounds and event state require a date field")
		}
	case "created":
		column = "d.created_at"
	case "state_changed":
		column = "d.state_changed_at"
	case "transition":
		if !validState(f.EventState) {
			return "", nil, invalid("transition filtering requires a supported event state")
		}
		column = "e.at"
	default:
		return "", nil, invalid("unsupported date field")
	}
	if f.DateField != "transition" && f.EventState != "" {
		return "", nil, invalid("event state requires transition filtering")
	}
	if column != "" {
		// Dates apply only to tasks; note-date semantics are not defined in v1.
		clauses = append(clauses, "d.kind = 'task'")
		bounds := []string{column + " IS NOT NULL"}
		if f.DateField == "transition" {
			bounds = append(bounds, "e.state = ?")
			args = append(args, f.EventState)
		}
		if f.From != nil {
			bounds = append(bounds, column+" >= ?")
			args = append(args, stamp(*f.From))
		}
		if f.Until != nil {
			bounds = append(bounds, column+" < ?")
			args = append(args, stamp(*f.Until))
		}
		condition := strings.Join(bounds, " AND ")
		if f.DateField == "transition" {
			condition = "EXISTS (SELECT 1 FROM state_events e WHERE e.document_id = d.id AND " + condition + ")"
		}
		clauses = append(clauses, condition)
	}
	return strings.Join(clauses, " AND "), args, nil
}

func queryLimit(limit, fallback, maximum int) (int, error) {
	if limit < 0 || limit > maximum {
		return 0, invalid("limit outside supported range")
	}
	if limit == 0 {
		limit = fallback
	}
	return limit, nil
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func validState(state model.State) bool {
	switch state {
	case model.Planned, model.InProgress, model.Stalled, model.Completed:
		return true
	}
	return false
}

func invalid(message string) error { return &model.Error{Code: "invalid_argument", Message: message} }
