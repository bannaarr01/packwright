package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// Both TUILauncher and GUILauncher are wired in at link time by cmd_tui.go
// and cmd_gui.go respectively (see their init functions). The bootstrap
// "not linked" assertions that used to live here for one or both front-ends
// have moved out as each PR landed — they would now spin up the real Wails
// runtime, which is not appropriate for unit tests.

// withStubLaunchers swaps in test launchers and restores the originals when the
// test finishes, so mutating the package-level registry does not leak across
// tests.
func withStubLaunchers(t *testing.T, tui, gui Launcher) {
	t.Helper()
	origTUI, origGUI := TUILauncher, GUILauncher
	t.Cleanup(func() { TUILauncher, GUILauncher = origTUI, origGUI })
	TUILauncher, GUILauncher = tui, gui
}

func TestRootNoArgsInvokesTUILauncher(t *testing.T) {
	tuiCalled, guiCalled := false, false
	withStubLaunchers(t,
		func(context.Context) error { tuiCalled = true; return nil },
		func(context.Context) error { guiCalled = true; return nil },
	)

	c := newRootCmd()
	c.SetArgs(nil)
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !tuiCalled {
		t.Error("expected TUILauncher to be invoked for the default (no-args) command")
	}
	if guiCalled {
		t.Error("GUILauncher must not be invoked without --gui")
	}
}

func TestRootGUIFlagInvokesGUILauncher(t *testing.T) {
	tuiCalled, guiCalled := false, false
	withStubLaunchers(t,
		func(context.Context) error { tuiCalled = true; return nil },
		func(context.Context) error { guiCalled = true; return nil },
	)

	c := newRootCmd()
	c.SetArgs([]string{"--gui"})
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !guiCalled {
		t.Error("expected GUILauncher to be invoked with --gui")
	}
	if tuiCalled {
		t.Error("TUILauncher must not be invoked when --gui is set")
	}
}

func TestVersionFlag(t *testing.T) {
	c := newRootCmd()
	out := &bytes.Buffer{}
	c.SetArgs([]string{"--version"})
	c.SetOut(out)
	c.SetErr(out)
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	got := out.String()
	if !strings.Contains(got, "packwright") || !strings.Contains(got, version) {
		t.Fatalf("--version output = %q, want it to contain %q and %q", got, "packwright", version)
	}
}
