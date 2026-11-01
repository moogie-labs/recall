package runtimetree

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "lib"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "llama-server"), []byte("server"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib", "libggml.1.dylib"), []byte("library"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("libggml.1.dylib", filepath.Join(dir, "lib", "libggml.dylib")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func digest(t *testing.T, dir string) string {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := Digest(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestDigestIsStableAcrossCopiesAndDirectoryModes(t *testing.T) {
	a, b := fixture(t), fixture(t)
	if err := os.Chmod(filepath.Join(b, "lib"), 0755); err != nil {
		t.Fatal(err)
	}
	if digest(t, a) != digest(t, b) {
		t.Fatal("identical payloads produced different digests")
	}
}

func TestDigestDetectsEveryKindOfPayloadChange(t *testing.T) {
	base := digest(t, fixture(t))
	for name, change := range map[string]func(string) error{
		"content":    func(d string) error { return os.WriteFile(filepath.Join(d, "llama-server"), []byte("malicious"), 0755) },
		"mode":       func(d string) error { return os.Chmod(filepath.Join(d, "lib", "libggml.1.dylib"), 0755) },
		"added file": func(d string) error { return os.WriteFile(filepath.Join(d, "lib", "extra.dylib"), nil, 0644) },
		"removed":    func(d string) error { return os.Remove(filepath.Join(d, "lib", "libggml.dylib")) },
		"link target": func(d string) error {
			if err := os.Remove(filepath.Join(d, "lib", "libggml.dylib")); err != nil {
				return err
			}
			return os.Symlink("../llama-server", filepath.Join(d, "lib", "libggml.dylib"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := fixture(t)
			if err := change(dir); err != nil {
				t.Fatal(err)
			}
			if digest(t, dir) == base {
				t.Fatal("payload change was not detected")
			}
		})
	}
}

func TestEntriesRejectSpecialFilesAndHonorCancellation(t *testing.T) {
	dir := fixture(t)
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Entries(ctx, r); err != context.Canceled {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "llama-server")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "llama-server"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Entries(context.Background(), r); err == nil {
		t.Fatal("FIFO accepted as a runtime file")
	}
}
