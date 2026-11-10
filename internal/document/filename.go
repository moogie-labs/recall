package document

import (
	"path/filepath"
	"strings"

	"recall/internal/model"
)

// Suffix marks a file as a Recall document.
const Suffix = ".md"

// ValidatePath accepts a case-sensitive .md filename with a nonempty name and
// no hidden path component. Path confinement and filesystem entry validation
// belong to the store.
func ValidatePath(path string) error {
	if path != "" && !strings.HasSuffix(path, string(filepath.Separator)) && strings.HasSuffix(filepath.Base(path), Suffix) && !hidden(path) {
		return nil
	}
	return &model.Error{Code: "invalid_document", Path: path, Message: "a note must be a .md file outside hidden paths"}
}

func hidden(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if strings.HasPrefix(part, ".") && part != "." && part != ".." {
			return true
		}
	}
	return false
}
