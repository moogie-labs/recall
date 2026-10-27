package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"recall/internal/app"
	"recall/internal/cli"
	"recall/internal/config"
	dto "recall/internal/desktop"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	handled, code := dispatch(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, func(args []string, in io.Reader, out, logs io.Writer) int { return cli.Run(ctx, args, in, out, logs) })
	if handled {
		os.Exit(code)
	}
	var appctx context.Context
	flags := flag.NewFlagSet("Recall", flag.ContinueOnError)
	root := flags.String("root", "", "Collection root")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return
	}
	preferences, err := dto.DefaultPreferences()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	service := dto.NewService(func(ctx context.Context, cfg config.Config) (dto.Application, error) { return app.Open(ctx, cfg) }, preferences)
	service.SetInitialRoot(*root)
	defer service.Close()
	host := newHost(func(name string, payload any) { runtime.EventsEmit(appctx, name, payload) }, func() { runtime.Quit(appctx) }, func(url string) { runtime.BrowserOpenURL(appctx, url) })
	host.close = service.Close
	host.copy = func(text string) error { return runtime.ClipboardSetText(appctx, text) }
	host.choose = func() (string, error) {
		return runtime.OpenDirectoryDialog(appctx, runtime.OpenDialogOptions{Title: "Choose Recall collection", CanCreateDirectories: false})
	}
	err = wails.Run(&options.App{Title: "Recall", Width: 1140, Height: 760, MinWidth: 760, MinHeight: 500,
		AssetServer: &assetserver.Options{Assets: assets},
		OnDomReady:  verifyDOMReady,
		OnStartup: func(ctx context.Context) {
			appctx = ctx
			service.SetEventSink(func(name string, payload any) { runtime.EventsEmit(ctx, name, payload) })
		}, OnBeforeClose: host.BeforeClose, Bind: []interface{}{host, dto.Bindings(service)},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
