package main

import (
	"bytes"
	"io"
	"os"
	"reflect"
	"testing"

	"recall/internal/version"
)

func TestWorkerDispatchBeforeGUI(t *testing.T) {
	t.Setenv("RECALL_GUARDIAN", "preserve-me")
	input, output, logs := &bytes.Buffer{}, &bytes.Buffer{}, &bytes.Buffer{}
	args := []string{"_embedding-worker", "--root", "/a root"}
	handled, code := dispatch(args, input, output, logs, func(got []string, in io.Reader, out, err io.Writer) int {
		if !reflect.DeepEqual(got, args) || in != input || out != output || err != logs || os.Getenv("RECALL_GUARDIAN") != "preserve-me" {
			t.Fatal("worker invocation changed")
		}
		return 7
	})
	if !handled || code != 7 {
		t.Fatalf("got %v %d", handled, code)
	}
	handled, _ = dispatch(nil, input, output, logs, func([]string, io.Reader, io.Writer, io.Writer) int { t.Fatal("normal launch entered worker"); return 0 })
	if handled {
		t.Fatal("normal launch handled as worker")
	}
}

func TestVersionDispatchBeforeGUI(t *testing.T) {
	previous := version.Version
	version.Version = "1.2.3"
	t.Cleanup(func() { version.Version = previous })
	var out bytes.Buffer
	handled, code := dispatch([]string{"_version"}, nil, &out, io.Discard, func([]string, io.Reader, io.Writer, io.Writer) int {
		t.Fatal("version entered the worker")
		return 0
	})
	if !handled || code != 0 || out.String() != "1.2.3\n" {
		t.Fatalf("%v %d %q", handled, code, out.String())
	}
}
