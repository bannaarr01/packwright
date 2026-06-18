package gui

import (
	"context"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/manifest"
	"github.com/bannaarr01/packwright/internal/workspace"
)

// WorkspaceChangedEvent is the Wails event the workspace watcher emits when
// anything under <home>/projects/ changes (project.yaml, env.yaml, or a
// record JSON under stacks/). The sidebar's Projects grouping subscribes
// via runtime.EventsOn(WorkspaceChangedEvent, …) and re-fetches
// ListProjects + ListStacks. The event carries no payload — the watcher
// is purely a "something changed, take another look" signal, matching
// PaletteChangedEvent's contract.
const WorkspaceChangedEvent = "packwright:workspace-changed"

// workspaceWatcherFactory is a package-level seam so tests can inject a
// fake watcher that emits change events on demand without touching
// fsnotify or the real filesystem. Production code keeps the default
// which constructs a real internal/manifest.Watcher.
var workspaceWatcherFactory = func() (workspaceWatcher, error) {
	return manifest.NewWatcher(0)
}

// workspaceWatcher is the minimal slice of internal/manifest.Watcher the
// startWorkspaceWatcher bridge depends on. It exists only so the test seam
// can hand back a stub without re-implementing the watcher's full API.
type workspaceWatcher interface {
	Add(root string) error
	Events() <-chan manifest.Change
	Close() error
}

// startWorkspaceWatcher subscribes a manifest watcher to <home>/projects/
// and emits WorkspaceChangedEvent on every debounced change so the
// frontend can re-fetch the Projects-grouping data. The bridge runs until
// ctx is cancelled by shutdown.
//
// Setup failure is non-fatal: the GUI still runs, just without live
// project-tree updates. The frontend's on-mount fetch picks up the latest
// data on the next sidebar refresh.
func (a *App) startWorkspaceWatcher(ctx context.Context) {
	home, err := config.Home()
	if err != nil {
		a.logger.Warn("gui: workspace watcher: resolve home", "err", err)
		return
	}
	w, err := workspaceWatcherFactory()
	if err != nil {
		a.logger.Warn("gui: workspace watcher: new", "err", err)
		return
	}
	root := filepath.Join(home, workspace.ProjectsSubdir)
	if _, err := os.Stat(root); err == nil {
		if err := w.Add(root); err != nil {
			a.logger.Warn("gui: workspace watcher: add root", "root", root, "err", err)
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
					runtime.EventsEmit(wailsCtx, WorkspaceChangedEvent)
				}
			}
		}
	}()
	a.workspaceWatcherStop = func() {
		_ = w.Close()
		<-done
	}
}
