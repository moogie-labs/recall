// Command runtimedigest prints the payload digest of a runtime archive, using
// the same extraction and tree description as installation and launch checks.
// Release tooling records this value as tree_sha256 in the runtime lock.
package main

import (
	"context"
	"fmt"
	"os"

	"recall/internal/bootstrap"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: runtimedigest RUNTIME-ARCHIVE")
		os.Exit(2)
	}
	digest, err := bootstrap.RuntimeArchiveDigest(context.Background(), os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "runtimedigest:", err)
		os.Exit(1)
	}
	fmt.Println(digest)
}
