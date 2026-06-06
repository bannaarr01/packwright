package cmd

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/bannaarr01/packwright/gui"
)

// guiCmd is the explicit `packwright gui` subcommand. The --gui flag on the
// root command also reaches the GUI via GUILauncher (overridden in init below).
var guiCmd = &cobra.Command{
	Use:   "gui",
	Short: "Launch the graphical user interface",
	Long: `Launch the Packwright graphical user interface.

This is equivalent to invoking packwright --gui — the GUI is wired into the
root command's GUILauncher as well.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return gui.Launch(cmd.Context())
	},
}

// init wires the GUI into the CLI in two ways:
//   - registers guiCmd as a child of the root command via registerSubcommand,
//     so `packwright gui` dispatches here.
//   - overrides GUILauncher, so `packwright --gui` (handled in root.go) lands
//     in the same place.
func init() {
	registerSubcommand(guiCmd)
	GUILauncher = func(ctx context.Context) error { return gui.Launch(ctx) }
}
