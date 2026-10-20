package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSummarizeProjectsMetadataAndTaskTimesOnly(t *testing.T) {
	first := time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC)
	last := first.Add(time.Hour)
	d := Document{ID: "abc", Path: "work.task.md", Title: "Work", Kind: Task, State: Completed, Revision: "rev", Body: "private body", Raw: "private source", History: []Event{{State: Planned, At: first}, {State: Completed, At: last}}}
	b, err := json.Marshal(Summarize(d))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"id":"abc"`, `"path":"work.task.md"`, `"title":"Work"`, `"kind":"task"`, `"state":"completed"`, `"revision":"rev"`, `"created_at":"2026-10-18T01:00:00Z"`, `"state_changed_at":"2026-10-18T02:00:00Z"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("missing %s: %s", want, b)
		}
	}
	for _, forbidden := range []string{"body", "raw", "history", "private"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("leaked %s: %s", forbidden, b)
		}
	}
	d.Kind = Note
	b, _ = json.Marshal(Summarize(d))
	if strings.Contains(string(b), "_at") {
		t.Fatalf("note has task dates: %s", b)
	}
}
