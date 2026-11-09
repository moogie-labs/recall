package document

import (
	"path/filepath"
	"strings"

	"recall/internal/model"
)

// Suffix marks a file as a Recall document.
const Suffix = ".note.md"

// ValidatePath recognizes only nonempty, case-sensitive .note.md filenames.
// Path confinement and filesystem entry validation belong to the store.
func ValidatePath(path string) error {
	name := filepath.Base(path)
	if path != "" && !strings.HasSuffix(path, string(filepath.Separator)) && strings.HasSuffix(name, Suffix) && len(name) > len(Suffix) {
		return nil
	}
	return &model.Error{Code: "invalid_document", Path: path, Message: "filename must have a nonempty name followed by " + Suffix}
}
