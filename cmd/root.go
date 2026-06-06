package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

// version is the Packwright build version. It defaults to "dev" for local
// builds and is overridden at release time via the linker, e.g.:
//
//	go build -ldflags "-X github.com/bannaarr01/packwright/cmd.version=v1.2.3"
var version = "dev"

// newRootCmd constructs the root cobra command. It is a constructor rather than
// a package-level singleton so that tests can build isolated command trees. The
// command reads the TUILauncher / GUILauncher registry variables at run time,
// so front-ends registered via init (see Launcher) are picked up automatically.
func newRootCmd() *cobra.Command {
	var guiMode bool

	cmd := &cobra.Command{
		Use:   "packwright",
		Short: "Packwright scaffolds and manages AWS infrastructure templates",
		Long: `Packwright is a hybrid terminal/graphical tool for generating and
managing AWS infrastructure templates.

Run without arguments to launch the interactive terminal UI (TUI), or pass
--gui to launch the graphical UI (GUI).`,
		Version:       version,
		Args:          cobra.NoArgs,
		SilenceUsage:  true, // a failing launcher is a runtime error, not a usage error
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			if guiMode {
				return GUILauncher(cmd.Context())
			}
			return TUILauncher(cmd.Context())
		},
	}

	cmd.Flags().BoolVar(&guiMode, "gui", false,
		"launch the graphical (GUI) front-end instead of the terminal (TUI) front-end")

	for _, sub := range rootSubcommands {
		cmd.AddCommand(sub)
	}

	return cmd
}

// Execute builds and runs the root command. It is the single entry point used
// by main. On error it exits the process with status code 1; cobra has already
// printed the error message to stderr by that point.
func Execute() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
