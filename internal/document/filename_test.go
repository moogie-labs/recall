package document

import (
	"testing"

	"recall/internal/model"
)

func TestValidatePath(t *testing.T) {
	for _, path := range []string{"ideas.note.md", "folder with spaces/café.note.md", "folder/dotted.name.note.md", ".hidden.note.md"} {
		if err := ValidatePath(path); err != nil {
			t.Errorf("ValidatePath(%q) = %v", path, err)
		}
	}
	for _, path := range []string{"", ".note.md", "folder/.note.md", "ideas.NOTE.md", "ideas.note.MD", "ideas.md", "fix.task.md", "ideas.note.md.bak", "ideas.note.md/"} {
		if err := ValidatePath(path); model.ErrorCode(err) != "invalid_document" {
			t.Errorf("ValidatePath(%q) = %v; want invalid_document", path, err)
		}
	}
}
