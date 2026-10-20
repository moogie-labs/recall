package bootstrap

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
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
		{name: "llama-b11669/libbad.dylib", kind: tar.TypeSymlink, link: "../../escaped.dylib"},
		{name: "llama-b11669/libbad.dylib", kind: tar.TypeSymlink, link: "/absolute-escape"},
		{name: "llama-b11669/arbitrary-link", kind: tar.TypeSymlink, link: "llama-server"},
		{name: "llama-b11669/libbad.dylib", kind: tar.TypeSymlink, link: "missing.dylib"},
		{name: "llama-b11669/libbad.dylib", kind: tar.TypeSymlink, link: "libbad.dylib"},
		{name: "llama-b11669/hardlink", kind: tar.TypeLink, link: "llama-b11669/llama-server"},
		{name: "llama-b11669/pipe", kind: tar.TypeFifo},
	} {
		t.Run(entry.name+entry.link, func(t *testing.T) {
			cfg := preparedConfig(t)
			outside := filepath.Join(t.TempDir(), "untouched.dylib")
			writeFile(t, outside, []byte("user work"))
			if entry.link == "/absolute-escape" {
				entry.link = outside
			}
			if entry.name == "/absolute-escape" {
				entry.name = outside
			}
			raw, manifest := runtimeArchive(t,
				tarEntry{name: "llama-b11669/llama-server", body: "staged but never installed"}, entry,
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

func TestRuntimeRejectsArchiveWritingThroughDylibLink(t *testing.T) {
	cfg := preparedConfig(t)
	raw, m := runtimeArchive(t,
		tarEntry{name: "llama-b11669/llama-server", body: "server"},
		tarEntry{name: "llama-b11669/libreal.dylib", body: "library"},
		tarEntry{name: "llama-b11669/libalias.dylib", kind: tar.TypeSymlink, link: "libreal.dylib"},
		tarEntry{name: "llama-b11669/libalias.dylib/escaped", body: "attack through link"},
	)
	archive := filepath.Join(t.TempDir(), m.Filename)
	writeFile(t, archive, raw)
	if err := installRuntime(t.Context(), cfg, archive, m); model.ErrorCode(err) != "asset_invalid" {
		t.Fatalf("write-through archive accepted: %v", err)
	}
	entries, err := os.ReadDir(cfg.RuntimeDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("write-through archive left files: %v %v", entries, err)
	}
}

// Fail precisely at a compressed-stream boundary, without relying on timing or
// allowing gzip's buffered read-ahead to consume bytes beyond that boundary.
type failingArchiveReader struct {
	raw              []byte
	offset, boundary int
	err              error
	beforeError      func()
}

func (r *failingArchiveReader) Read(p []byte) (int, error) {
	if r.offset == r.boundary {
		if r.beforeError != nil {
			r.beforeError()
		}
		return 0, r.err
	}
	n := copy(p, r.raw[r.offset:r.boundary])
	r.offset += n
	return n, nil
}

func TestRuntimeDecodingPreservesActiveCancellation(t *testing.T) {
	raw := runtimeDecodingFixture(t)
	for _, phase := range []struct {
		name     string
		boundary int
	}{
		{"gzip_header", 0}, {"tar_header", 10}, {"gzip_trailer", len(raw) - 8},
	} {
		t.Run(phase.name, func(t *testing.T) {
			r, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reader := &failingArchiveReader{raw: raw, boundary: phase.boundary, err: io.ErrUnexpectedEOF, beforeError: cancel}
			if err := extractVerifiedRuntime(ctx, reader, r, fixtureManifest(raw)); !errors.Is(err, context.Canceled) {
				t.Fatalf("active cancellation became corruption: %v", err)
			}
		})
	}
}

func runtimeDecodingFixture(t *testing.T) []byte {
	t.Helper()
	var tarBytes, compressed bytes.Buffer
	tw := tar.NewWriter(&tarBytes)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(&compressed)
	// Extra uncompressed bytes ensure tar EOF precedes the gzip trailer read.
	if _, err := gz.Write(append(tarBytes.Bytes(), make([]byte, 64<<10)...)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

func TestRuntimeDecodingPreservesContextErrors(t *testing.T) {
	raw := runtimeDecodingFixture(t)
	for _, phase := range []struct {
		name     string
		boundary int
	}{
		{"gzip_header", 0}, {"tar_header", 10}, {"gzip_trailer", len(raw) - 8},
	} {
		for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
			t.Run(phase.name+"/"+cause.Error(), func(t *testing.T) {
				r, err := os.OpenRoot(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				defer r.Close()
				reader := &failingArchiveReader{raw: raw, boundary: phase.boundary, err: fmt.Errorf("interrupted archive read: %w", cause)}
				if err := extractVerifiedRuntime(t.Context(), reader, r, fixtureManifest(raw)); !errors.Is(err, cause) {
					t.Fatalf("%s lost cancellation identity: %v; want errors.Is(%v)", phase.name, err, cause)
				}
			})
		}
	}
}

func TestRuntimeDecodingStillRejectsCorruption(t *testing.T) {
	raw := runtimeDecodingFixture(t)
	raw[len(raw)-8] ^= 0xff // Gzip CRC, after the tar end marker.
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"gzip_header", []byte("not a gzip archive")},
		{"gzip_trailer", raw},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if err := extractVerifiedRuntime(t.Context(), bytes.NewReader(tc.raw), r, fixtureManifest(tc.raw)); model.ErrorCode(err) != "asset_invalid" {
				t.Fatalf("corrupt archive no longer rejected: %v", err)
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
