package cmd

import (
	"bytes"
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

func TestDefaultLaunchersReportNotLinked(t *testing.T) {
	if err := TUILauncher(); err == nil || !strings.Contains(err.Error(), "TUI not linked") {
		t.Fatalf("TUILauncher() error = %v, want it to contain %q", err, "TUI not linked")
	}
	if err := GUILauncher(); err == nil || !strings.Contains(err.Error(), "GUI not linked") {
		t.Fatalf("GUILauncher() error = %v, want it to contain %q", err, "GUI not linked")
	}
}

func TestRootNoArgsInvokesTUILauncher(t *testing.T) {
	tuiCalled, guiCalled := false, false
	withStubLaunchers(t,
		func() error { tuiCalled = true; return nil },
		func() error { guiCalled = true; return nil },
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
		func() error { tuiCalled = true; return nil },
		func() error { guiCalled = true; return nil },
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

func TestRootNoArgsReturnsTUINotLinked(t *testing.T) {
	// With no front-end registered, the default command must surface the
	// "TUI not linked" stub error — the behaviour `./packwright` exhibits in a
	// bootstrap build.
	c := newRootCmd()
	c.SetArgs(nil)
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	err := c.Execute()
	if err == nil || !strings.Contains(err.Error(), "TUI not linked") {
		t.Fatalf("Execute() error = %v, want it to contain %q", err, "TUI not linked")
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
