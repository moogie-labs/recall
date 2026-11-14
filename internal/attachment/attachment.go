// Package attachment holds the pure rules for note image attachments: where a
// note's images live, which formats are accepted, how stored files are named,
// and how Markdown links to them are written and rewritten. It performs no I/O;
// callers do all filesystem access through the confined store.
package attachment

import (
	"bytes"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"recall/internal/document"
)

const (
	// FolderSuffix marks an attachment folder: <dir>/<base>.assets holds the
	// images of <dir>/<base>.md.
	FolderSuffix = ".assets"
	// MaxBytes is the largest image accepted.
	MaxBytes = 20 << 20
	maxStem  = 80
)

type Format struct {
	Extension string
	MIME      string
}

var (
	PNG  = Format{".png", "image/png"}
	JPEG = Format{".jpg", "image/jpeg"}
	GIF  = Format{".gif", "image/gif"}
	WebP = Format{".webp", "image/webp"}
)

// Detect identifies an accepted image format from its leading bytes.
func Detect(data []byte) (Format, bool) {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return PNG, true
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return JPEG, true
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return GIF, true
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return WebP, true
	}
	return Format{}, false
}

// ForExtension maps a filename extension, including its dot, to a format.
func ForExtension(ext string) (Format, bool) {
	switch strings.ToLower(ext) {
	case ".png":
		return PNG, true
	case ".jpg", ".jpeg":
		return JPEG, true
	case ".gif":
		return GIF, true
	case ".webp":
		return WebP, true
	}
	return Format{}, false
}

// Folder returns the content-relative attachment folder of a note path.
func Folder(notePath string) string {
	base := strings.TrimSuffix(filepath.Base(notePath), document.Suffix)
	return filepath.Join(filepath.Dir(notePath), base+FolderSuffix)
}

func IsFolderName(name string) bool { return strings.HasSuffix(name, FolderSuffix) }

// Within reports whether a content-relative path is an attachment folder or
// lies anywhere inside one.
func Within(path string) bool {
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if IsFolderName(part) {
			return true
		}
	}
	return false
}

// FileName derives a safe stored filename from an original name: directory
// parts and the original extension are dropped, runs of anything but letters,
// digits and underscores become one "-", and the detected format's extension
// is appended. n > 1 adds a "-n" collision suffix.
func FileName(original string, f Format, n int) string {
	base := filepath.Base(strings.ReplaceAll(original, "\\", "/"))
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	var b strings.Builder
	dash := false
	for _, r := range stem {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	runes := []rune(strings.TrimRight(b.String(), "-"))
	if len(runes) > maxStem {
		runes = runes[:maxStem]
	}
	name := strings.TrimRight(string(runes), "-")
	if name == "" {
		name = "image"
	}
	if n > 1 {
		name += "-" + strconv.Itoa(n)
	}
	return name + f.Extension
}

// Destination percent-encodes each segment of a relative path so it is a valid
// Markdown link destination even when names contain spaces or parentheses.
func Destination(rel string) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

// RewriteLinks repoints Markdown link and image destinations (inline, angle
// bracketed, and reference definitions) that start with oldFolder/ to
// newFolder/. Both the literal and percent-encoded forms of oldFolder match;
// the replacement is always encoded. Other text is untouched.
func RewriteLinks(body []byte, oldFolder, newFolder string) []byte {
	if oldFolder == newFolder {
		return body
	}
	replacement := Destination(newFolder) + "/"
	olds := []string{oldFolder}
	if encoded := Destination(oldFolder); encoded != oldFolder {
		olds = append(olds, encoded)
	}
	for _, old := range olds {
		for _, opener := range []string{"](", "](<", "]: ", "]: <"} {
			body = bytes.ReplaceAll(body, []byte(opener+old+"/"), []byte(opener+replacement))
		}
	}
	return body
}
