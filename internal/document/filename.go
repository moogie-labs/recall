package document

import (
	"path/filepath"
	"strings"

	"recall/internal/model"
)

// KindFromPath recognizes only nonempty, case-sensitive type-bearing filenames.
// Path confinement and filesystem entry validation belong to the store.
func KindFromPath(path string) (model.Kind, error) {
	name := filepath.Base(path)
	if path != "" && !strings.HasSuffix(path, string(filepath.Separator)) {
		for _, kind := range []model.Kind{model.Note, model.Task} {
			suffix := "." + string(kind) + ".md"
			if strings.HasSuffix(name, suffix) && len(name) > len(suffix) {
				return kind, nil
			}
		}
	}
	return "", &model.Error{Code: "invalid_document", Path: path, Message: "filename must have a nonempty name followed by .note.md or .task.md"}
}
