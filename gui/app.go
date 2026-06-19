// Package gui implements the Packwright graphical front-end. It runs a
// native webview (via Wails v2) loading the embedded Svelte bundle in
// web/dist, and exposes a small set of RPC methods to the frontend.
//
// The package is wired into the CLI in cmd/cmd_gui.go, which assigns
// cmd.GUILauncher to Launch from its init.
package gui

import (
	"context"
	"log/slog"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the Wails application object. Its exported methods (see bindings.go)
// are surfaced to the frontend over Wails RPC; lifecycle hooks (startup,
// shutdown) bridge the cobra context to the Wails runtime so cancelling the
// cobra command quits the window.
type App struct {
	// parentCtx is the cobra command's ctx, populated by Launch before
	// wails.Run is called. The shutdown bridge (spawned in startup) watches
	// this context so a Ctrl+C on the CLI quits the window.
	parentCtx context.Context

	// wailsCtx is the context handed in by Wails at startup. It is the only
	// safe argument to runtime.* calls (Quit, WindowSetTitle, etc.).
	wailsCtx context.Context

	// bridgeStop cancels the shutdown-bridge goroutine started by startup.
	// shutdown calls it and then Waits on bridgeWG so the goroutine is
	// guaranteed to have exited before the process tears down.
	bridgeStop context.CancelFunc
	bridgeWG   sync.WaitGroup

	// watcherStop tears down the manifest-watcher bridge spawned by startup
	// (see startPaletteWatcher). Nil when no watcher was started. shutdown
	// invokes it before bridgeWG.Wait so the watcher goroutine exits before
	// the runtime tears down its event subsystem.
	watcherStop func()

	// workspaceWatcherStop tears down the workspace-watcher bridge spawned
	// by startup (see startWorkspaceWatcher). Same lifecycle as
	// watcherStop; both are stopped during shutdown.
	workspaceWatcherStop func()

	// quit is the function invoked when the cobra ctx fires before the user
	// closes the window. Defaults to runtime.Quit; tests inject a fake so
	// the bridge lifecycle can be verified without a live Wails runtime.
	quit func(context.Context)

	// ai holds the AI chat session state (bindings_ai.go). Kept as a field of
	// a package-local type so this file needs no AI imports.
	ai *aiBridge

	logger *slog.Logger
}

// newApp constructs an App. The logger receives lifecycle and palette events;
// passing nil disables those log lines but is otherwise harmless.
func newApp(logger *slog.Logger) *App {
	if logger == nil {
		logger = slog.Default()
	}
	return &App{logger: logger, quit: runtime.Quit, ai: &aiBridge{}}
}

// startup is invoked by Wails once the webview is ready. It captures the
// Wails runtime ctx (needed for runtime.Quit) and — if Launch attached a
// parent ctx — spawns the shutdown bridge and the manifest-watcher bridge.
//
// The bridges run only here, never before, so each goroutine always has a
// valid wailsCtx to hand to quit / EventsEmit. That avoids the race where
// the cobra ctx fires before Wails finishes initialising and the bridges
// would otherwise have nothing to call against.
func (a *App) startup(wailsCtx context.Context) {
	a.wailsCtx = wailsCtx
	a.logger.Info("gui startup")
	// Diagnostic: pre-warm the palette and log the row count so we can confirm
	// what the binding would return without waiting for the frontend to call
	// it. Cheap (one Discover walk + slice copy) and runs once per launch.
	probe := a.ListSlashCommands()
	a.logger.Info("gui startup: palette probe", "rows", len(probe))
	for _, sc := range probe {
		a.logger.Info("gui startup: palette row", "slash", sc.Slash, "title", sc.Title)
	}
	if a.parentCtx == nil {
		return
	}
	parent := a.parentCtx
	quitFn := a.quit
	bridgeCtx, cancel := context.WithCancel(parent)
	a.bridgeStop = cancel
	a.bridgeWG.Add(1)
	go func() {
		defer a.bridgeWG.Done()
		<-bridgeCtx.Done()
		// bridgeCtx is a child of parent (cobra ctx) and is also cancelled
		// by shutdown via bridgeStop. Distinguish the two: if the cobra ctx
		// itself fired, the user wants the window closed.
		if parent.Err() != nil && quitFn != nil {
			quitFn(wailsCtx)
		}
	}()

	a.startPaletteWatcher(bridgeCtx)
	a.startWorkspaceWatcher(bridgeCtx)
}

// shutdown is invoked by Wails just before the runtime tears down. It
// stops the bridge goroutine and blocks until the goroutine has actually
// exited, so the process never leaks it.
func (a *App) shutdown(_ context.Context) {
	if a.bridgeStop != nil {
		a.bridgeStop()
		a.bridgeWG.Wait()
		a.bridgeStop = nil
	}
	if a.watcherStop != nil {
		a.watcherStop()
		a.watcherStop = nil
	}
	if a.workspaceWatcherStop != nil {
		a.workspaceWatcherStop()
		a.workspaceWatcherStop = nil
	}
	// Tear down any live AI session: cancels in-flight turns, closes the
	// provider, restores the consent modal, and unblocks a pending prompt.
	a.CloseAISession()
	a.logger.Info("gui shutdown")
}
