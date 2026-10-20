package bootstrap

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"recall/internal/config"
	"recall/internal/model"
)

type tarEntry struct {
	name, body, link string
	kind             byte
}

func runtimeArchive(t *testing.T, entries ...tarEntry) ([]byte, Manifest) {
	t.Helper()
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		kind := entry.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		header := &tar.Header{Name: entry.name, Typeflag: kind, Linkname: entry.link, Mode: 0755}
		if kind == tar.TypeReg {
			header.Size = int64(len(entry.body))
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := tw.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	m := fixtureManifest(raw.Bytes())
	m.ID, m.Filename, m.License = "b11669", "llama-b11669-bin-macos-arm64.tar.gz", "MIT"
	return raw.Bytes(), m
}

func preparedConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Resolve(filepath.Join(t.TempDir(), "collection with spaces"))
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestRuntimeInstallsCompletePayloadAndSafeDylibLinksIdempotently(t *testing.T) {
	cfg := preparedConfig(t)
	raw, manifest := runtimeArchive(t,
		tarEntry{name: "llama-b11669", kind: tar.TypeDir},
		tarEntry{name: "llama-b11669/llama-server", body: "native fixture"},
		tarEntry{name: "llama-b11669/libggml.1.dylib", body: "sibling library"},
		tarEntry{name: "llama-b11669/libggml.dylib", kind: tar.TypeSymlink, link: "libggml.1.dylib"},
		tarEntry{name: "llama-b11669/LICENSE", body: "MIT license fixture"},
		tarEntry{name: "llama-b11669/tools/llama-tokenize", body: "tool fixture"},
	)
	archive := filepath.Join(t.TempDir(), manifest.Filename)
	writeFile(t, archive, raw)
	for range 2 {
		if err := installRuntime(t.Context(), cfg, archive, manifest); err != nil {
			t.Fatal(err)
		}
	}
	dest := filepath.Join(cfg.RuntimeDir, "b11669")
	assertFile(t, filepath.Join(dest, "llama-server"), []byte("native fixture"))
	assertFile(t, filepath.Join(dest, "libggml.dylib"), []byte("sibling library"))
	assertFile(t, filepath.Join(dest, "LICENSE"), []byte("MIT license fixture"))
	assertFile(t, filepath.Join(dest, "tools", "llama-tokenize"), []byte("tool fixture"))
	link, err := os.Readlink(filepath.Join(dest, "libggml.dylib"))
	if err != nil || link != "libggml.1.dylib" {
		t.Fatalf("dylib symlink not preserved: %q %v", link, err)
	}
	info, _ := os.Stat(filepath.Join(dest, "llama-server"))
	if info.Mode()&0111 == 0 {
		t.Fatal("server lost executable permission")
	}
}

func TestRuntimeRejectsUnsafeArchivesWithoutEscapingStage(t *testing.T) {
	for _, entry := range []tarEntry{
		{name: "../escaped", body: "attack"},
		{name: "/absolute-escape", body: "attack"},
		{name: "llama-b11669/../escaped", body: "attack"},
		{name: "other-prefix/llama-server", body: "attack"},
		{name: "llama-b11669/libbad.dylib", kind: tar.TypeSymlink, link: "../../escaped"},
		{name: "llama-b11669/libbad.dylib", kind: tar.TypeSymlink, link: "/absolute-escape"},
		{name: "llama-b11669/arbitrary-link", kind: tar.TypeSymlink, link: "llama-server"},
		{name: "llama-b11669/libbad.dylib", kind: tar.TypeSymlink, link: "missing.dylib"},
		{name: "llama-b11669/libbad.dylib", kind: tar.TypeSymlink, link: "libbad.dylib"},
		{name: "llama-b11669/hardlink", kind: tar.TypeLink, link: "llama-b11669/llama-server"},
		{name: "llama-b11669/pipe", kind: tar.TypeFifo},
	} {
		t.Run(entry.name+entry.link, func(t *testing.T) {
			cfg := preparedConfig(t)
			outside := filepath.Join(t.TempDir(), "untouched")
			writeFile(t, outside, []byte("user work"))
			if entry.link == "/absolute-escape" {
				entry.link = outside
			}
			if entry.name == "/absolute-escape" {
				entry.name = outside
			}
			raw, manifest := runtimeArchive(t,
				tarEntry{name: "llama-b11669/llama-server", body: "staged but never installed"}, entry,
				tarEntry{name: "llama-b11669/libbad.dylib/escaped", body: "attack through link"},
			)
			archive := filepath.Join(t.TempDir(), manifest.Filename)
			writeFile(t, archive, raw)
			if err := installRuntime(t.Context(), cfg, archive, manifest); model.ErrorCode(err) != "asset_invalid" {
				t.Fatalf("unsafe tar not rejected: %v", err)
			}
			assertFile(t, outside, []byte("user work"))
			entries, err := os.ReadDir(cfg.RuntimeDir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed install left payload or escaped staging: %v %v", entries, err)
			}
			if _, err := os.Stat(filepath.Join(cfg.Root, "escaped")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("wrote outside stage: %v", err)
			}
		})
	}
}

func TestRuntimeRejectsEscapingLinkChains(t *testing.T) {
	cfg := preparedConfig(t)
	outside := filepath.Join(t.TempDir(), "outside.dylib")
	writeFile(t, outside, []byte("user library"))
	raw, m := runtimeArchive(t,
		tarEntry{name: "llama-b11669/llama-server", body: "server"},
		tarEntry{name: "llama-b11669/libfirst.dylib", kind: tar.TypeSymlink, link: "libsecond.dylib"},
		tarEntry{name: "llama-b11669/libsecond.dylib", kind: tar.TypeSymlink, link: outside},
	)
	archive := filepath.Join(t.TempDir(), m.Filename)
	writeFile(t, archive, raw)
	if err := installRuntime(t.Context(), cfg, archive, m); model.ErrorCode(err) != "asset_invalid" {
		t.Fatalf("escaping chain accepted: %v", err)
	}
	assertFile(t, outside, []byte("user library"))
	entries, _ := os.ReadDir(cfg.RuntimeDir)
	if len(entries) != 0 {
		t.Fatalf("escaping chain left files: %v", entries)
	}
}

func TestRuntimeAcceptsConfinedRelativeDylibChains(t *testing.T) {
	cfg := preparedConfig(t)
	raw, m := runtimeArchive(t,
		tarEntry{name: "llama-b11669/llama-server", body: "server"},
		tarEntry{name: "llama-b11669/libreal.dylib", body: "library"},
		tarEntry{name: "llama-b11669/libalias.dylib", kind: tar.TypeSymlink, link: "libreal.dylib"},
		tarEntry{name: "llama-b11669/libs/liblocal.dylib", kind: tar.TypeSymlink, link: "../libalias.dylib"},
	)
	archive := filepath.Join(t.TempDir(), m.Filename)
	writeFile(t, archive, raw)
	if err := installRuntime(t.Context(), cfg, archive, m); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(cfg.RuntimeDir, m.ID, "libs", "liblocal.dylib"), []byte("library"))
}

func TestRuntimeChecksumFailureRetryAndExistingPayloadPreservation(t *testing.T) {
	cfg := preparedConfig(t)
	raw, manifest := runtimeArchive(t, tarEntry{name: "llama-b11669/llama-server", body: "server"})
	archive := filepath.Join(t.TempDir(), manifest.Filename)
	writeFile(t, archive, []byte("tampered archive"))
	if err := installRuntime(t.Context(), cfg, archive, manifest); model.ErrorCode(err) != "asset_invalid" {
		t.Fatalf("bad archive accepted: %v", err)
	}
	writeFile(t, archive, raw)
	if err := installRuntime(t.Context(), cfg, archive, manifest); err != nil {
		t.Fatalf("retry: %v", err)
	}
	userFile := filepath.Join(cfg.RuntimeDir, "b11669", "user-file")
	writeFile(t, userFile, []byte("preserve unknown"))
	if err := installRuntime(t.Context(), cfg, archive, manifest); model.ErrorCode(err) != "asset_invalid" {
		t.Fatalf("unexpected installed payload silently replaced: %v", err)
	}
	assertFile(t, userFile, []byte("preserve unknown"))
	assertFile(t, filepath.Join(cfg.RuntimeDir, "b11669", "llama-server"), []byte("server"))
}

func TestRuntimeVerifiesBytesConsumedByExtraction(t *testing.T) {
	good, manifest := runtimeArchive(t, tarEntry{name: "llama-b11669/llama-server", body: "verified"})
	changed, _ := runtimeArchive(t, tarEntry{name: "llama-b11669/llama-server", body: "modified"})
	for _, tc := range []struct {
		name     string
		raw      []byte
		wantCode string
	}{{"original", good, ""}, {"changed after initial verification", changed, "asset_invalid"}} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if err := extractVerifiedRuntime(t.Context(), bytes.NewReader(tc.raw), r, manifest); model.ErrorCode(err) != tc.wantCode {
				t.Fatalf("extracted stream verification: %v", err)
			}
		})
	}
}
