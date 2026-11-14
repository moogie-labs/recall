package desktop

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"recall/internal/app"
	"recall/internal/attachment"
)

var desktopPNG = []byte("\x89PNG\r\n\x1a\ndesktop-image")

func TestSaveImageAndCount(t *testing.T) {
	s, a, _, session := fixture(t)
	if err := a.CreateFolder(t.Context(), "Ideas"); err != nil {
		t.Fatal(err)
	}
	note, err := a.Create(t.Context(), app.CreateInput{Folder: "Ideas", Name: "plan"})
	if err != nil {
		t.Fatal(err)
	}
	data := base64.StdEncoding.EncodeToString(desktopPNG)
	r := s.SaveImage(SaveImageInput{Session: session, ID: note.ID, Name: "shot.png", Data: data})
	if r.Problem != nil || r.Name != "shot.png" || r.Link != "plan.assets/shot.png" {
		t.Fatalf("save = %+v", r)
	}
	if c := s.Attachments(session, note.ID); c.Problem != nil || c.Count != 1 {
		t.Fatalf("count = %+v", c)
	}
	for name, in := range map[string]SaveImageInput{
		"stale session": {Session: "old", ID: note.ID, Name: "x.png", Data: data},
		"no document":   {Session: session, Name: "x.png", Data: data},
		"bad base64":    {Session: session, ID: note.ID, Name: "x.png", Data: "***"},
		"too large":     {Session: session, ID: note.ID, Name: "x.png", Data: strings.Repeat("A", base64.StdEncoding.EncodedLen(attachment.MaxBytes)+4)},
		"not an image":  {Session: session, ID: note.ID, Name: "x.png", Data: base64.StdEncoding.EncodeToString([]byte("<svg/>"))},
	} {
		if got := s.SaveImage(in); got.Problem == nil || got.Link != "" {
			t.Errorf("%s accepted: %+v", name, got)
		}
	}
	if c := s.Attachments("old", note.ID); c.Problem == nil {
		t.Error("stale session counted")
	}
}

func TestImageHandlerServesOnlyConfinedImages(t *testing.T) {
	s, a, cfg, session := fixture(t)
	if err := a.CreateFolder(t.Context(), "Ideas"); err != nil {
		t.Fatal(err)
	}
	note, err := a.Create(t.Context(), app.CreateInput{Folder: "Ideas", Name: "my plan"})
	if err != nil {
		t.Fatal(err)
	}
	if r := s.SaveImage(SaveImageInput{Session: session, ID: note.ID, Name: "shot.png", Data: base64.StdEncoding.EncodeToString(desktopPNG)}); r.Problem != nil {
		t.Fatal(r.Problem)
	}
	assets := filepath.Join(cfg.Content, "Ideas", "my plan.assets")
	if err := os.WriteFile(filepath.Join(assets, "fake.png"), []byte("text"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "x.png")
	if err := os.WriteFile(outside, desktopPNG, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(assets, "link.png")); err != nil {
		t.Fatal(err)
	}
	handler := ImageHandler(s)
	get := func(method, target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(method, target, nil))
		return w
	}

	ok := get(http.MethodGet, "/note-image/Ideas/my%20plan.assets/shot.png")
	if ok.Code != http.StatusOK || !bytes.Equal(ok.Body.Bytes(), desktopPNG) {
		t.Fatalf("GET = %d %q", ok.Code, ok.Body.String())
	}
	for header, want := range map[string]string{"Content-Type": "image/png", "X-Content-Type-Options": "nosniff", "Cache-Control": "no-cache"} {
		if got := ok.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	for _, target := range []string{
		"/note-image/Ideas/my%20plan.md",
		"/note-image/Ideas/my%20plan.assets/fake.png",
		"/note-image/Ideas/my%20plan.assets/link.png",
		"/note-image/Ideas/my%20plan.assets/missing.png",
		"/note-image/%2e%2e/config.json",
		"/note-image/..%2F..%2Fconfig.json",
		"/note-image/Ideas/my%20plan.assets/..%2F..%2F..%2Fconfig.json",
		"/note-image/",
		"/other/Ideas/my%20plan.assets/shot.png",
	} {
		if w := get(http.MethodGet, target); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d", target, w.Code)
		}
	}
	// httptest.NewRequest panics on an invalid escape, so build this one by hand.
	// EscapedPath re-escapes an invalid RawPath, so this exercises the
	// not-an-attachment path rather than the unescape failure.
	malformed := httptest.NewRecorder()
	handler.ServeHTTP(malformed, &http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/note-image/x", RawPath: "/note-image/Ideas/my%zzplan.assets/shot.png"}})
	if malformed.Code != http.StatusNotFound {
		t.Errorf("malformed escape = %d", malformed.Code)
	}
	if w := get(http.MethodPost, "/note-image/Ideas/my%20plan.assets/shot.png"); w.Code != http.StatusNotFound {
		t.Errorf("POST = %d", w.Code)
	}
	closed := NewService(nil, nil)
	w := httptest.NewRecorder()
	ImageHandler(closed).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/note-image/Ideas/my%20plan.assets/shot.png", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("no collection = %d", w.Code)
	}
}

func TestSidebarMoveCarriesNoteImages(t *testing.T) {
	s, _, cfg, session := fixture(t)
	s.Create(CreateInput{Session: session, Kind: "folder", Name: "archive"})
	d := s.Create(CreateInput{Session: session, Kind: "note", Name: "idea"})
	if r := s.SaveImage(SaveImageInput{Session: session, ID: d.Document.ID, Name: "shot.png", Data: base64.StdEncoding.EncodeToString(desktopPNG)}); r.Problem != nil {
		t.Fatal(r.Problem)
	}
	r := s.Move(MoveInput{Session: session, ID: d.Document.ID, Path: d.Document.Path, Folder: "archive", ExpectedRevision: d.Document.Revision})
	if r.Problem != nil || r.Document == nil || r.Document.Path != "archive/idea.md" {
		t.Fatalf("move %+v", r)
	}
	if _, err := os.Stat(filepath.Join(cfg.Content, "archive", "idea.assets", "shot.png")); err != nil {
		t.Fatalf("images did not move with the note: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Content, "idea.assets")); !os.IsNotExist(err) {
		t.Fatal("attachment folder left behind")
	}
}
