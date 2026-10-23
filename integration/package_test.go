package integration

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPackageArchiveValidation(t *testing.T) {
	// Same hostile classes as bootstrap Task 8; the release verifier must reject
	// them before extracting anything, even with a freshly computed checksum.
	for _, tc := range []struct {
		name, link string
		kind       byte
		valid      bool
	}{
		{"llama-b11669/libalias.dylib", "libreal.dylib", tar.TypeSymlink, true},
		{"../escape", "", tar.TypeReg, false}, {"/absolute", "", tar.TypeReg, false},
		{"llama-b11669/../escape", "", tar.TypeReg, false},
		{"other/server", "", tar.TypeReg, false},
		{"llama-b11669/libalias.dylib", "../../escape.dylib", tar.TypeSymlink, false},
		{"llama-b11669/libalias.dylib", "/absolute.dylib", tar.TypeSymlink, false},
		{"llama-b11669/libalias.dylib", "libalias.dylib", tar.TypeSymlink, false},
		{"llama-b11669/libalias.dylib", "missing.dylib", tar.TypeSymlink, false},
		{"llama-b11669/link", "llama-server", tar.TypeSymlink, false},
		{"llama-b11669/hard", "llama-b11669/llama-server", tar.TypeLink, false},
		{"llama-b11669/fifo", "", tar.TypeFifo, false},
	} {
		t.Run(tc.name+tc.link, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "fixture.tar.gz")
			f, err := os.Create(archive)
			if err != nil {
				t.Fatal(err)
			}
			gz := gzip.NewWriter(f)
			tw := tar.NewWriter(gz)
			for _, h := range []*tar.Header{{Name: "llama-b11669/llama-server", Mode: 0755, Typeflag: tar.TypeReg}, {Name: "llama-b11669/libreal.dylib", Mode: 0644, Typeflag: tar.TypeReg}, {Name: tc.name, Linkname: tc.link, Typeflag: tc.kind, Mode: 0644}} {
				if err := tw.WriteHeader(h); err != nil {
					t.Fatal(err)
				}
			}
			tw.Close()
			gz.Close()
			f.Close()
			out, err := exec.Command("python3", "../scripts/distribution.py", "check-runtime", archive).CombinedOutput()
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v %s", tc.valid, err, out)
			}
		})
	}
}
