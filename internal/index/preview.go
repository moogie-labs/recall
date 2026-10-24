package index

import "recall/internal/model"

// preview repairs only the returned excerpt and caps it at exactly the shared
// 1024-byte UTF-8-safe budget, including the truncation marker. Source is intact.
func preview(text string) string {
	text, _ = model.ClipPreview(text, 1024)
	return text
}
