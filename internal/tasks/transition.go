package tasks

import (
	"time"

	"recall/internal/document"
	"recall/internal/model"
)

// Transition changes a task's state and appends one UTC history event. Every
// supported state is reachable from every other state, including Completed.
// Same-state requests leave source bytes and history unchanged. The input is
// never mutated. A current state is required; document.Normalize handles adoption.
// changed reports the transition, not edits already present in p.
func Transition(p document.Parsed, state model.State, at time.Time) (document.Parsed, bool, error) {
	if !validState(state) {
		return p, false, &model.Error{Code: "invalid_argument", Message: "state must be a supported task state"}
	}
	// Honor pending typed/metadata edits and authoritative Body, validate before
	// even a no-op, and obtain private history and metadata before changing them.
	raw, err := document.Encode(p)
	if err != nil {
		return p, false, err
	}
	q, err := document.Parse(p.Document.Path, raw)
	if err != nil {
		return p, false, err
	}
	if q.Document.Kind != model.Task {
		return p, false, &model.Error{Code: "type_mismatch", Path: q.Document.Path, Message: "only tasks have state transitions"}
	}
	if !validState(q.Document.State) {
		return p, false, &model.Error{Code: "invalid_document", Path: q.Document.Path, Message: "task requires a supported current state"}
	}
	if q.Document.State == state {
		return q, false, nil
	}
	q.Document.State = state
	q.Document.History = append(q.Document.History, model.Event{State: state, At: at.UTC()})
	raw, err = document.Encode(q)
	if err != nil {
		return p, false, err
	}
	q, err = document.Parse(q.Document.Path, raw)
	if err != nil {
		return p, false, err
	}
	return q, true, nil
}

func validState(state model.State) bool {
	switch state {
	case model.Planned, model.InProgress, model.Stalled, model.Completed:
		return true
	}
	return false
}
