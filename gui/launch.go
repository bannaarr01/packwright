package gui

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

// Launch starts the Packwright GUI and blocks until the user closes the
// window, the bound ctx is cancelled, or Wails errors out. It is the entry
// point invoked by both the `gui` subcommand and the `--gui` flag (via
// cmd.GUILauncher).
//
// Cancelling ctx causes the Wails window to quit cleanly via the bridge
// goroutine attached to the App.
//
// Wave-1 dependencies (config, pack registry) are not yet wired in; Launch
// runs standalone with a stderr slog logger to match the TUI's posture.
func Launch(ctx context.Context) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	app := newApp(logger)
	app.parentCtx = ctx

	err := wails.Run(&options.App{
		Title:     "Packwright",
		Width:     1100,
		Height:    760,
		MinWidth:  640,
		MinHeight: 480,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind:       []interface{}{app},
		Mac: &mac.Options{
			// TitleBarHiddenInset hides the native title bar but keeps the
			// traffic lights inset into the chrome. With the title bar hidden
			// the frontend must designate a draggable region via
			// -webkit-app-region: drag — App.svelte / Sidebar.svelte do this
			// on a thin rail across the top.
			TitleBar: mac.TitleBarHiddenInset(),
		},
		Windows: &windows.Options{},
	})
	if err != nil {
		return fmt.Errorf("running gui program: %w", err)
	}
	return nil
}
