package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSummarizeProjectsMetadataOnly(t *testing.T) {
	d := Document{ID: "abc", Path: "work.md", Title: "Work", Revision: "rev", Body: "private body", Raw: "private source"}
	b, err := json.Marshal(Summarize(d))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"id":"abc"`, `"path":"work.md"`, `"title":"Work"`, `"revision":"rev"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("missing %s: %s", want, b)
		}
	}
	for _, forbidden := range []string{"body", "raw", "private"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("leaked %s: %s", forbidden, b)
		}
	}
}
