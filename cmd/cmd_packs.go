package cmd

import (
	"github.com/spf13/cobra"

	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/pack/install"
)

// packsCmd is the `packwright packs` subcommand tree. The verbs (add /
// update / remove / list) match the slash-command shape from ADR-0027 so
// the CLI and the eventual TUI / GUI palette route through the same
// install.Run dispatcher — there is one place to add a new verb, and one
// place to fix a bug in any of them.
//
// `packs` itself is a parent command; running it without a verb prints
// the help text. The verbs delegate to install.Run, which writes
// human-readable status to cmd.OutOrStdout() and returns the operation
// error verbatim (so callers may branch on errors.Is(err,
// install.ErrDenied) / install.ErrNotInstalled).
var packsCmd = &cobra.Command{
	Use:   "packs",
	Short: "Install, update, remove, and list packs",
	Long: `Manage Packwright packs — third-party collections of manifests and
templates installed by git URL or local path. See ADR-0027.`,
}

// newPacksRun returns the RunE wrapper for one `packs <verb>` subcommand.
// The verb is captured by the closure so cobra dispatches `packs add` /
// `packs update` / etc. through the same install.Run code path the TUI /
// GUI palette will share once their slash-command routing lands.
func newPacksRun(verb string) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		home, err := config.Home()
		if err != nil {
			return err
		}
		return install.Run(cmd.Context(), cmd.OutOrStdout(),
			home, append([]string{verb}, args...))
	}
}

// init wires every verb as its own cobra child so cobra's built-in help
// renders the verb list cleanly (each verb gets its own Short line) and
// argument-arity validation happens up front.
func init() {
	add := &cobra.Command{
		Use:   "add <git-url|./path>",
		Short: "Install a pack from a git URL (optionally pinned with #<ref>) or a local path",
		Args:  cobra.ExactArgs(1),
		RunE:  newPacksRun("add"),
	}
	update := &cobra.Command{
		Use:   "update <name>|--all",
		Short: "Update an installed pack (or every pack with --all)",
		Args:  cobra.MinimumNArgs(1),
		RunE:  newPacksRun("update"),
	}
	remove := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an installed pack",
		Args:  cobra.ExactArgs(1),
		RunE:  newPacksRun("remove"),
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List installed packs",
		Args:  cobra.NoArgs,
		RunE:  newPacksRun("list"),
	}
	packsCmd.AddCommand(add, update, remove, list)
	registerSubcommand(packsCmd)
}
