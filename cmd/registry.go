// Package cmd implements the Packwright command-line interface together with
// the front-end registry that lets the TUI and GUI front-ends wire themselves
// into the CLI at link time.
package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

// Launcher starts a Packwright front-end (the TUI or the GUI) and blocks
// until that front-end exits. The ctx is the cobra command's context —
// cancelling it causes the front-end to exit. Returns a non-nil error if the
// front-end fails to start or exits abnormally.
//
// The concrete TUI and GUI implementations live in their own packages so the
// CLI can be built, tested, and shipped without pulling in their heavier
// dependencies. Those packages register themselves by overriding the
// package-level TUILauncher / GUILauncher variables from an init function:
//
//	// in the TUI package's cmd file
//	func init() { cmd.TUILauncher = func(ctx context.Context) error { ... } }
//
// Because Execute builds the cobra command tree at run time — after every
// init function has executed — the root command always observes the
// registered launchers rather than the stubs below.
type Launcher func(ctx context.Context) error

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
	return func(context.Context) error {
		return fmt.Errorf("%s not linked into this build", frontend)
	}
}

// rootSubcommands holds subcommands that should be attached to every freshly
// constructed root command. Subcommand files (e.g. cmd_tui.go, cmd_gui.go)
// append themselves from their init function via registerSubcommand; the
// newRootCmd constructor consumes the slice at run time.
//
// This indirection preserves the constructor-style root command — preferred
// per AGENTS.md so tests can build isolated command trees — while still
// letting later PRs add subcommands without modifying root.go.
var rootSubcommands []*cobra.Command

// registerSubcommand records sub as a child of the root command. It is meant
// to be called from a subcommand file's init.
func registerSubcommand(sub *cobra.Command) {
	rootSubcommands = append(rootSubcommands, sub)
}
