package main

import (
	"bytes"
	"io"
	"os"
	"reflect"
	"testing"
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
