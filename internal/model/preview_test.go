package model

import (
	"strings"
	"testing"
)

func TestSummaryTitlePreviewUTF8Boundaries(t *testing.T) {
	for _, tc := range []struct {
		name, title, want string
		truncated         bool
	}{
		{"exact", strings.Repeat("x", 256), strings.Repeat("x", 256), false},
		{"ascii-over", strings.Repeat("x", 257), strings.Repeat("x", 253) + "…", true},
		{"four-byte-exact", strings.Repeat("x", 252) + "🙂", strings.Repeat("x", 252) + "🙂", false},
		{"four-byte-split", strings.Repeat("x", 252) + "🙂 tail", strings.Repeat("x", 252) + "…", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := Document{ID: "id", Path: "a.note.md", Revision: "revision", Title: tc.title}
			rows, err := Summaries([]Document{d})
			if err != nil {
				t.Fatal(err)
			}
			if rows[0].Title != tc.want || rows[0].TitleTruncated != tc.truncated || d.Title != tc.title {
				t.Fatal("preview boundary or source preservation changed")
			}
		})
	}
}
