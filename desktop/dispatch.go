package main

import "io"

func dispatch(args []string, stdin io.Reader, stdout, stderr io.Writer, runWorker func([]string, io.Reader, io.Writer, io.Writer) int) (bool, int) {
	if len(args) > 0 && args[0] == "_embedding-worker" {
		return true, runWorker(args, stdin, stdout, stderr)
	}
	return false, 0
}
