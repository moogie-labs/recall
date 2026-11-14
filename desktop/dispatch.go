package main

import (
	"fmt"
	"io"

	"recall/internal/version"
)

func dispatch(args []string, stdin io.Reader, stdout, stderr io.Writer, runWorker func([]string, io.Reader, io.Writer, io.Writer) int) (bool, int) {
	if len(args) == 1 && args[0] == "_version" {
		// Lets the release build check the app binary's embedded version.
		fmt.Fprintln(stdout, version.Version)
		return true, 0
	}
	if len(args) > 0 && args[0] == "_embedding-worker" {
		return true, runWorker(args, stdin, stdout, stderr)
	}
	return false, 0
}
