package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"recall/internal/cli"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	handled, code := dispatch(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, func(args []string, in io.Reader, out, logs io.Writer) int { return cli.Run(ctx, args, in, out, logs) })
	if handled {
		os.Exit(code)
	}
	var appctx context.Context
	host := newHost(func(name string, payload any) { runtime.EventsEmit(appctx, name, payload) }, func() { runtime.Quit(appctx) }, func(url string) { runtime.BrowserOpenURL(appctx, url) })
	err := wails.Run(&options.App{Title: "Recall", Width: 1140, Height: 760, MinWidth: 760, MinHeight: 500,
		AssetServer: &assetserver.Options{Assets: assets},
		OnStartup:   func(ctx context.Context) { appctx = ctx }, OnBeforeClose: host.BeforeClose, Bind: []interface{}{host},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
