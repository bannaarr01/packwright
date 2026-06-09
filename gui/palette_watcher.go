package gui

import (
	"context"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/manifest"
	"github.com/bannaarr01/packwright/pack"
)

// PaletteChangedEvent is the Wails event the manifest watcher emits when a
// file under one of the watched roots changes. The frontend subscribes via
// runtime.EventsOn(PaletteChangedEvent, …) and re-calls ListSlashCommands
// to refresh the visible palette. The event carries no payload — the
// frontend already has the data-fetching path and the watcher is purely a
// "something changed, take another look" signal.
const PaletteChangedEvent = "packwright:palette-changed"

// paletteWatcherFactory is a package-level seam so tests can inject a fake
// watcher that emits change events on demand without touching fsnotify or
// the real filesystem. Production code keeps the default which constructs a
// real internal/manifest.Watcher.
var paletteWatcherFactory = func() (paletteWatcher, error) {
	return manifest.NewWatcher(0)
}

// paletteWatcher is the minimal slice of internal/manifest.Watcher the
// startPaletteWatcher bridge depends on. It exists only so the test seam
// can hand back a stub without re-implementing the watcher's full API.
type paletteWatcher interface {
	Add(root string) error
	Events() <-chan manifest.Change
	Close() error
}

// startPaletteWatcher subscribes a manifest watcher to the Packwright home's
// manifest roots and emits PaletteChangedEvent on every debounced change so
// the frontend can re-fetch ListSlashCommands. The bridge runs until ctx is
// cancelled by shutdown.
//
// Setup failure is non-fatal: the GUI still runs, just without live palette
// updates. The frontend's on-mount fetch still picks up the latest data on
// the next palette open.
func (a *App) startPaletteWatcher(ctx context.Context) {
	home, err := config.Home()
	if err != nil {
		a.logger.Warn("gui: palette watcher: resolve home", "err", err)
		return
	}
	w, err := paletteWatcherFactory()
	if err != nil {
		a.logger.Warn("gui: palette watcher: new", "err", err)
		return
	}
	for _, root := range pack.WatchRoots(home) {
		if err := w.Add(root); err != nil {
			a.logger.Warn("gui: palette watcher: add root", "root", root, "err", err)
		}
	}
	wailsCtx := a.wailsCtx
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-w.Events():
				if !ok {
					return
				}
				if wailsCtx != nil {
					runtime.EventsEmit(wailsCtx, PaletteChangedEvent)
				}
			}
		}
	}()
	a.watcherStop = func() {
		_ = w.Close()
		<-done
	}
}
