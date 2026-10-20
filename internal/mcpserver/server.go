// Package mcpserver adapts the shared application to local stdio MCP. It starts
// neither a watcher nor inference: application calls reconcile on demand.
package mcpserver

import (
	"context"
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"recall/internal/app"
)

// New exposes full collection permissions. Tool annotations are informational,
// not an approval system; confinement and revisions are enforced by App.
// The caller owns the application lifecycle.
func New(application *app.App) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "recall", Version: "1"}, nil)
	registerTools(server, application)
	return server
}

// Run serves newline-delimited JSON-RPC only. Existing closers are deliberately
// preserved so the SDK can unblock idle reads/writes on cancellation or EOF.
// Non-closable injected streams must themselves return from blocked I/O.
func Run(ctx context.Context, application *app.App, input io.Reader, output io.Writer) error {
	reader, ok := input.(io.ReadCloser)
	if !ok {
		reader = io.NopCloser(input)
	}
	writer, ok := output.(io.WriteCloser)
	if !ok {
		writer = nopWriteCloser{output}
	}
	return New(application).Run(ctx, &mcp.IOTransport{Reader: reader, Writer: writer})
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
