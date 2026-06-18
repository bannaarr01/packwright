package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bannaarr01/packwright/internal/scaffold"
)

// promoteTemplateCmd is the `packwright promote-template` subcommand
// (ADR-0047, PR-05). It is the headless face of the /promote-template
// slash command: take a draft manifest path, run the validator pipeline
// (via the loader's strict-decode + Validate), then atomically remove the
// `_draft: true` line so the manifest becomes deployable.
//
// The atomic-write contract — temp file + rename — is what gives the ADR
// guarantee that the file is valid YAML at every observable moment. A
// concurrent watcher reload can never see a half-written promotion.
var promoteTemplateCmd = &cobra.Command{
	Use:   "promote-template <path>",
	Short: "Promote a draft manifest by removing the _draft: true line",
	Long: `Promote a draft manifest by removing the _draft: true line.

` + "`packwright promote-template`" + ` is the headless face of the
/promote-template slash command. It reads the manifest at <path>,
removes the ` + "`_draft: true`" + ` key, and atomically rewrites the file via
a temp-file-plus-rename so the YAML on disk is always valid — even mid-
promotion. The ` + "`_copied_from`" + ` provenance header (if present) is left
intact so the deployed manifest still carries its audit trail.

Use this once the draft is ready to deploy. Drafts can be loaded,
validated, and previewed at any time; only the deploy path is blocked
until promotion clears the ` + "`_draft`" + ` flag.`,
	Args: cobra.ExactArgs(1),
	RunE: runPromoteTemplate,
}

func init() {
	registerSubcommand(promoteTemplateCmd)
}

// runPromoteTemplate is the cobra adapter for scaffold.PromoteTemplate.
// Errors propagate verbatim so the cobra exit code and stderr message
// match the underlying scaffold-layer message — there is no second-level
// wrapping to confuse the user.
func runPromoteTemplate(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()
	path := args[0]
	if err := scaffold.PromoteTemplate(path); err != nil {
		return err
	}
	fmt.Fprintf(out, "Promoted %s — _draft removed\n", path)
	return nil
}
