// Package cmd implements the Packwright command-line interface together with
// the front-end registry that lets the TUI and GUI front-ends wire themselves
// into the CLI at link time.
package cmd

import "fmt"

// Launcher starts a Packwright front-end (the TUI or the GUI) and blocks until
// that front-end exits. It returns a non-nil error if the front-end fails to
// start or exits abnormally.
//
// The concrete TUI and GUI implementations live in their own packages so the
// CLI can be built, tested, and shipped without pulling in their heavier
// dependencies. Those packages register themselves by overriding the
// package-level TUILauncher / GUILauncher variables from an init function:
//
//	// in the TUI package
//	func init() { cmd.TUILauncher = run }
//
// Because Execute builds the cobra command tree at run time — after every init
// function has executed — the root command always observes the registered
// launchers rather than the stubs below.
type Launcher func() error

// TUILauncher and GUILauncher are the front-end entry points invoked by the
// root command. They default to stubs that report the corresponding front-end
// was not linked into this build; the real front-end packages replace them via
// init (see Launcher).
var (
	TUILauncher Launcher = notLinked("TUI")
	GUILauncher Launcher = notLinked("GUI")
)

// notLinked returns a Launcher stub that always fails, explaining that the
// named front-end ("TUI" or "GUI") was not compiled into this binary.
func notLinked(frontend string) Launcher {
	return func() error {
		return fmt.Errorf("%s not linked into this build", frontend)
	}
}
