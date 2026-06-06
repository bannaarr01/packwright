package gui

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestStartupBridgeQuitsOnParentCancel proves the cobra-ctx → Wails-quit
// bridge actually fires. Without this test the previous implementation
// silently tore the bridge down inside startup and shipped a window that
// never responded to Ctrl+C on the CLI.
func TestStartupBridgeQuitsOnParentCancel(t *testing.T) {
	app := newTestApp()
	parent, cancelParent := context.WithCancel(context.Background())
	app.parentCtx = parent

	quitFired := make(chan struct{})
	var quitCount atomic.Int32
	app.quit = func(_ context.Context) {
		quitCount.Add(1)
		close(quitFired)
	}

	// Use a non-nil sentinel ctx in place of the real Wails ctx; the bridge
	// hands it back to quit unchanged.
	app.startup(context.Background())

	cancelParent()

	select {
	case <-quitFired:
		// Good — bridge observed the cancel and called quit.
	case <-time.After(2 * time.Second):
		t.Fatal("bridge did not call quit within 2s after parent ctx was cancelled")
	}

	app.shutdown(context.Background())

	if got := quitCount.Load(); got != 1 {
		t.Errorf("quit called %d times, want exactly 1", got)
	}
}

// TestShutdownDoesNotQuitWhenParentStillLive proves shutdown's bridgeStop
// path is distinguishable from a parent cancel. When Wails closes the
// window first (no Ctrl+C upstream), the bridge must stop without calling
// quit — otherwise the runtime would receive a Quit during its own
// teardown.
func TestShutdownDoesNotQuitWhenParentStillLive(t *testing.T) {
	app := newTestApp()
	parent, cancelParent := context.WithCancel(context.Background())
	t.Cleanup(cancelParent)
	app.parentCtx = parent

	var quitCount atomic.Int32
	app.quit = func(_ context.Context) { quitCount.Add(1) }

	app.startup(context.Background())
	// shutdown stops the bridge and Waits for the goroutine — fully
	// deterministic, no sleeps required.
	app.shutdown(context.Background())

	if got := quitCount.Load(); got != 0 {
		t.Errorf("quit called %d times during normal window-close shutdown, want 0", got)
	}
}

// TestStartupWithoutParentCtxIsNoop covers the path where Launch is bypassed
// (e.g. a future caller constructs an App by hand and forgets to set
// parentCtx). The bridge must simply not start; nothing should panic.
func TestStartupWithoutParentCtxIsNoop(t *testing.T) {
	app := newTestApp()
	app.quit = func(_ context.Context) {
		t.Fatal("quit must not fire when no parent ctx was attached")
	}

	app.startup(context.Background())
	app.shutdown(context.Background())
}

// TestNewAppDefaultsToSlogDefault checks the nil-logger guard. Without it,
// later log calls would panic when the App is constructed by a caller that
// doesn't pass a logger.
func TestNewAppDefaultsToSlogDefault(t *testing.T) {
	app := newApp(nil)
	if app.logger == nil {
		t.Fatal("newApp(nil) left logger nil; expected slog.Default fallback")
	}
}
