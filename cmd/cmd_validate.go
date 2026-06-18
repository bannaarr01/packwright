package cmd

import (
	"github.com/spf13/cobra"

	"github.com/bannaarr01/packwright/action/dispatch"
)

// noValidate is the captured value of the --no-validate flag. It is a
// package-level var so the persistent flag has somewhere to land; the value
// is read once in the root command's PersistentPreRunE and stamped onto the
// cobra context via dispatch.WithValidatorsDisabled so every downstream
// dispatch call site reads from one place.
//
// The flag is session-scoped per ADR-0050: Packwright never writes it to
// config.yaml, never stamps it on a saved profile, and never persists it
// across processes. Logging of the skipped run happens inside the engine
// (resource.Execute) so a fresh `packwright tui --no-validate` shows up
// in the operational log every time it is used.
var noValidate bool

// applyValidatorFlags wires the --no-validate flag onto the root command.
// Root.go calls it during newRootCmd so the flag is inherited by every
// subcommand (tui, gui, future deploy paths). The PersistentPreRunE here
// translates the parsed flag value into a context value that
// action/dispatch.Dispatch hands to the resource runner.
//
// applyValidatorFlags is idempotent — calling it twice on the same command
// would shadow the existing PersistentPreRunE; production code calls it
// exactly once from newRootCmd.
func applyValidatorFlags(root *cobra.Command) {
	root.PersistentFlags().BoolVar(&noValidate, "no-validate", false,
		"skip the template-validator pipeline (YAML lint + cloudformation validate-template) for this run only; never persisted")

	prev := root.PersistentPreRunE
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		// Preserve any pre-existing hook the root or a subcommand registered
		// before us; cobra picks the closest non-nil PersistentPreRunE on the
		// command chain, so chaining manually keeps composition explicit
		// rather than relying on cobra's internal precedence rules.
		if prev != nil {
			if err := prev(cmd, args); err != nil {
				return err
			}
		}
		cmd.SetContext(dispatch.WithValidatorsDisabled(cmd.Context(), noValidate))
		return nil
	}
}
