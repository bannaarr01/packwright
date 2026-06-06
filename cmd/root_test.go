package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// withStubLaunchers swaps in test launchers and restores the originals when the
// test finishes, so mutating the package-level registry does not leak across
// tests.
func withStubLaunchers(t *testing.T, tui, gui Launcher) {
	t.Helper()
	origTUI, origGUI := TUILauncher, GUILauncher
	t.Cleanup(func() { TUILauncher, GUILauncher = origTUI, origGUI })
	TUILauncher, GUILauncher = tui, gui
}

// TestDefaultGUILauncherReportsNotLinked checks that the GUI front-end stub
// is still in place. The TUI front-end is wired in by cmd_tui.go's init, so
// no equivalent assertion is possible for TUILauncher; the corresponding GUI
// assertion will move out once PR-09 lands.
func TestDefaultGUILauncherReportsNotLinked(t *testing.T) {
	if err := GUILauncher(context.Background()); err == nil || !strings.Contains(err.Error(), "GUI not linked") {
		t.Fatalf("GUILauncher() error = %v, want it to contain %q", err, "GUI not linked")
	}
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
