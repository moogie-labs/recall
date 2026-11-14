package attachment

import (
	"strings"
	"testing"
)

var pngHeader = []byte("\x89PNG\r\n\x1a\nrest")

func TestDetect(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want Format
		ok   bool
	}{
		{"png", pngHeader, PNG, true},
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0}, JPEG, true},
		{"gif87", []byte("GIF87a...."), GIF, true},
		{"gif89", []byte("GIF89a...."), GIF, true},
		{"webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), WebP, true},
		{"riff-not-webp", []byte("RIFF\x00\x00\x00\x00WAVEfmt "), Format{}, false},
		{"svg", []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"), Format{}, false},
		{"text", []byte("hello"), Format{}, false},
		{"empty", nil, Format{}, false},
	}
	for _, c := range cases {
		got, ok := Detect(c.data)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: Detect = %v, %v; want %v, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestForExtension(t *testing.T) {
	for ext, want := range map[string]Format{".png": PNG, ".PNG": PNG, ".jpg": JPEG, ".jpeg": JPEG, ".JPEG": JPEG, ".gif": GIF, ".webp": WebP} {
		if got, ok := ForExtension(ext); !ok || got != want {
			t.Errorf("ForExtension(%q) = %v, %v", ext, got, ok)
		}
	}
	for _, ext := range []string{"", ".svg", ".heic", ".md", "png"} {
		if _, ok := ForExtension(ext); ok {
			t.Errorf("ForExtension(%q) accepted", ext)
		}
	}
}

func TestFolderAndWithin(t *testing.T) {
	for note, want := range map[string]string{"plan.md": "plan.assets", "Ideas/plan.md": "Ideas/plan.assets", "a b/my plan.md": "a b/my plan.assets"} {
		if got := Folder(note); got != want {
			t.Errorf("Folder(%q) = %q, want %q", note, got, want)
		}
	}
	for path, want := range map[string]bool{
		"plan.assets":                 true,
		"Ideas/plan.assets":           true,
		"Ideas/plan.assets/shot.png":  true,
		"Ideas/plan.assets/deep/x.md": true,
		"plan.md":                     false,
		"plan.assets.md":              false,
		"assets/x.png":                false,
		"Ideas/plan.md":               false,
	} {
		if got := Within(path); got != want {
			t.Errorf("Within(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestFileName(t *testing.T) {
	cases := []struct {
		original string
		format   Format
		n        int
		want     string
	}{
		{"Screen Shot 2026-11-14 at 15.30.12.png", PNG, 1, "Screen-Shot-2026-11-14-at-15-30-12.png"},
		{"photo.jpg", PNG, 1, "photo.png"},
		{"photo.JPEG", JPEG, 2, "photo-2.jpg"},
		{"../../etc/passwd", GIF, 1, "passwd.gif"},
		{".hidden.png", PNG, 1, "hidden.png"},
		{"", WebP, 1, "image.webp"},
		{"!!!.png", PNG, 1, "image.png"},
		{"café_menu.png", PNG, 1, "café_menu.png"},
		{"pasted-2026-11-14-153012", PNG, 3, "pasted-2026-11-14-153012-3.png"},
	}
	for _, c := range cases {
		if got := FileName(c.original, c.format, c.n); got != c.want {
			t.Errorf("FileName(%q, %v, %d) = %q, want %q", c.original, c.format, c.n, got, c.want)
		}
	}
	long := FileName(strings.Repeat("a", 200)+".png", PNG, 1)
	if len([]rune(long)) != 80+len(".png") {
		t.Errorf("long name not truncated: %d runes", len([]rune(long)))
	}
}

func TestDestination(t *testing.T) {
	for rel, want := range map[string]string{
		"plan.assets/shot.png":    "plan.assets/shot.png",
		"my plan.assets/shot.png": "my%20plan.assets/shot.png",
		"a (b).assets/café.png":   "a%20%28b%29.assets/caf%C3%A9.png",
		"x<y>.assets/z.png":       "x%3Cy%3E.assets/z.png",
	} {
		if got := Destination(rel); got != want {
			t.Errorf("Destination(%q) = %q, want %q", rel, got, want)
		}
	}
}

func TestRewriteLinks(t *testing.T) {
	body := "![a](plan.assets/a.png)\n" +
		"![b](<plan.assets/b.png>)\n" +
		"[ref]: plan.assets/c.png\n" +
		"[file](plan.assets.md) [web](https://x.example/plan.assets/a.png)\n" +
		"text plan.assets/a.png stays\n"
	want := "![a](roadmap.assets/a.png)\n" +
		"![b](<roadmap.assets/b.png>)\n" +
		"[ref]: roadmap.assets/c.png\n" +
		"[file](plan.assets.md) [web](https://x.example/plan.assets/a.png)\n" +
		"text plan.assets/a.png stays\n"
	if got := string(RewriteLinks([]byte(body), "plan.assets", "roadmap.assets")); got != want {
		t.Errorf("RewriteLinks:\n%s\nwant:\n%s", got, want)
	}
	spaced := "![a](my%20plan.assets/a.png) ![b](<my plan.assets/b.png>)"
	wantSpaced := "![a](new%20name.assets/a.png) ![b](<new%20name.assets/b.png>)"
	if got := string(RewriteLinks([]byte(spaced), "my plan.assets", "new name.assets")); got != wantSpaced {
		t.Errorf("RewriteLinks spaced = %q, want %q", got, wantSpaced)
	}
	if got := string(RewriteLinks([]byte(body), "plan.assets", "plan.assets")); got != body {
		t.Error("same folder changed the body")
	}
}
