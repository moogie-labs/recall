package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"recall/internal/app"
	"recall/internal/bootstrap"
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
	service.SetSetup(automaticSetup())
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

// automaticSetup lets first launch create ~/Documents/recall and install the
// pinned runtime (bundled in Contents/Resources/assets) plus the model in the
// background. Asset bytes are verified against pins compiled into this binary.
func automaticSetup() *dto.Setup {
	setup := &dto.Setup{
		Prepare: bootstrap.PrepareMetadata,
		Ready:   bootstrap.AssetsPresent,
		Install: func(ctx context.Context, cfg config.Config, progress func(int64, int64)) error {
			dir, err := bundledAssets()
			if err != nil {
				return err
			}
			_, err = bootstrap.Initialize(ctx, cfg, dir, progress)
			return err
		},
	}
	if home, err := os.UserHomeDir(); err == nil && filepath.IsAbs(home) {
		setup.Legacy = filepath.Join(home, ".local", "share", "recall")
	}
	return setup
}

func bundledAssets() (string, error) {
	if dir := os.Getenv("RECALL_ASSETS_DIR"); dir != "" {
		if !filepath.IsAbs(dir) {
			return "", fmt.Errorf("RECALL_ASSETS_DIR must be absolute")
		}
		return dir, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	if executable, err = filepath.EvalSymlinks(executable); err != nil {
		return "", err
	}
	// Recall.app/Contents/MacOS/Recall -> Recall.app/Contents/Resources/assets
	return filepath.Join(filepath.Dir(filepath.Dir(executable)), "Resources", "assets"), nil
}
