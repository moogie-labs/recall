package index

import (
	"strings"
	"unicode/utf8"
)

// preview repairs only the returned excerpt and caps it at exactly the shared
// 1024-byte UTF-8-safe budget, including the truncation marker. Source is intact.
func preview(text string) string {
	text = strings.ToValidUTF8(text, "�")
	if len(text) <= 1024 {
		return text
	}
	end := 1024 - len("…")
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + "…"
}
