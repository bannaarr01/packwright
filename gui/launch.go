package gui

import (
	"context"
	"fmt"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"github.com/bannaarr01/packwright/bootstrap"
)

// Launch starts the Packwright GUI and blocks until the user closes the
// window, the bound ctx is cancelled, or Wails errors out. It is the entry
// point invoked by both the `gui` subcommand and the `--gui` flag (via
// cmd.GUILauncher).
//
// Cancelling ctx causes the Wails window to quit cleanly via the bridge
// goroutine attached to the App.
//
// Bootstrap (shared with the TUI) opens the operational log
// <home>/logs/packwright.log and the usage log <home>/logs/usage.jsonl,
// and rewires slog.Default to the operational logger so every package
// that calls slog.Default() flows through the rotated file.
func Launch(ctx context.Context) error {
	logger := bootstrap.Init("gui")

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
