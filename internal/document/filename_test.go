package document

import (
	"testing"

	"recall/internal/model"
)

func TestValidatePath(t *testing.T) {
	for _, path := range []string{"ideas.md", "folder with spaces/café.md", "folder/dotted.name.md", "left.note.md", "old.task.md", "ideas.NOTE.md"} {
		if err := ValidatePath(path); err != nil {
			t.Errorf("ValidatePath(%q) = %v", path, err)
		}
	}
	for _, path := range []string{"", ".md", "folder/.md", ".hidden.md", ".git/README.md", "a/.obsidian/b/c.md", "ideas.MD", "ideas.Md", "ideas.txt", "ideas", "ideas.md.bak", "ideas.md/"} {
		if err := ValidatePath(path); model.ErrorCode(err) != "invalid_document" {
			t.Errorf("ValidatePath(%q) = %v; want invalid_document", path, err)
		}
	}
}
