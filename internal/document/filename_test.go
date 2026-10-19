package document

import (
	"testing"

	"recall/internal/model"
)

func TestKindFromPath(t *testing.T) {
	tests := []struct {
		path string
		want model.Kind
	}{
		{"ideas.note.md", model.Note},
		{"fix.task.md", model.Task},
		{"folder with spaces/café.note.md", model.Note},
		{"folder/dotted.name.task.md", model.Task},
		{".hidden.note.md", model.Note},
		{"", ""},
		{".note.md", ""},
		{"folder/.task.md", ""},
		{"ideas.NOTE.md", ""},
		{"ideas.note.MD", ""},
		{"fix.TASK.md", ""},
		{"ideas.md", ""},
		{"ideas.note.md.bak", ""},
		{"ideas.note.md/", ""},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, err := KindFromPath(tt.path)
			if tt.want == "" {
				if err == nil || model.ErrorCode(err) != "invalid_document" {
					t.Fatalf("KindFromPath(%q) = %q, %v; want invalid_document", tt.path, got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("kind = %q, want %q", got, tt.want)
			}
		})
	}
}
