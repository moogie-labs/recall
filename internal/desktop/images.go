package desktop

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"recall/internal/attachment"
	"recall/internal/model"
)

const imageRoute = "/note-image/"

// SaveImage stores a dropped or pasted image beside a note and returns the
// Markdown destination to insert. Data is standard base64.
func (s *Service) SaveImage(in SaveImageInput) SaveImageReply {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p := s.check(in.Session); p != nil {
		return SaveImageReply{Problem: p}
	}
	if in.ID == "" {
		return SaveImageReply{Problem: invalid("A document is required")}
	}
	if len(in.Data) > base64.StdEncoding.EncodedLen(attachment.MaxBytes) {
		return SaveImageReply{Problem: invalid("images must be 20 MB or smaller")}
	}
	data, err := base64.StdEncoding.DecodeString(in.Data)
	if err != nil {
		return SaveImageReply{Problem: invalid("the image data could not be read")}
	}
	saved, err := s.application.SaveAttachment(s.ctx, model.Ref{ID: in.ID}, in.Name, data)
	if err != nil {
		return SaveImageReply{Problem: problem(err)}
	}
	return SaveImageReply{Name: saved.Name, Link: saved.Link}
}

// Attachments counts a note's stored images for the delete confirmation.
func (s *Service) Attachments(session, id string) AttachmentsReply {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p := s.check(session); p != nil {
		return AttachmentsReply{Problem: p}
	}
	n, err := s.application.AttachmentCount(s.ctx, model.Ref{ID: id})
	if err != nil {
		return AttachmentsReply{Problem: problem(err)}
	}
	return AttachmentsReply{Count: n}
}

// ImageHandler serves note images to the webview at /note-image/<path>. Wails
// uses it as the asset server's fallback, so it only sees requests the embedded
// frontend cannot satisfy, and only from the app's own webview. Anything other
// than a GET for a genuine image directly inside a .assets folder of the open
// collection is a plain 404.
func ImageHandler(s *Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, format, ok := s.image(r)
		if !ok {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", format.MIME)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-cache")
		_, _ = w.Write(data)
	})
}

func (s *Service) image(r *http.Request) ([]byte, attachment.Format, bool) {
	if r.Method != http.MethodGet {
		return nil, attachment.Format{}, false
	}
	escaped, found := strings.CutPrefix(r.URL.EscapedPath(), imageRoute)
	if !found {
		return nil, attachment.Format{}, false
	}
	path, err := url.PathUnescape(escaped)
	if err != nil {
		return nil, attachment.Format{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.application == nil {
		return nil, attachment.Format{}, false
	}
	data, format, err := s.application.ReadAttachment(s.ctx, filepath.FromSlash(path))
	return data, format, err == nil
}
