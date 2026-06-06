package cmd

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/bannaarr01/packwright/tui"
)

// tuiCmd is the explicit `packwright tui` subcommand. The default no-args
// invocation also reaches the TUI via TUILauncher (overridden in init below).
var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Launch the terminal user interface",
	Long: `Launch the Packwright terminal user interface.

This is equivalent to invoking packwright with no arguments — the TUI is the
default front-end and is also wired into the root command's TUILauncher.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return tui.Launch(cmd.Context())
	},
}

// init wires the TUI into the CLI in two ways:
//   - registers tuiCmd as a child of the root command via registerSubcommand,
//     so `packwright tui` dispatches here.
//   - overrides TUILauncher, so the default no-args command (handled in
//     root.go) lands in the same place.
func init() {
	registerSubcommand(tuiCmd)
	TUILauncher = func(ctx context.Context) error { return tui.Launch(ctx) }
}
