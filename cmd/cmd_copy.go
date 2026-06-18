package cmd

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bannaarr01/packwright/internal/scaffold"
)

// copyTemplateCmd is the `packwright copy-template` subcommand (ADR-0047,
// PR-05). It is the headless CLI surface for the /copy-template slash
// command: read an existing manifest, fork it under a new slash, and write
// the result as a draft into the destination scope's drafts/ directory.
//
// Front-ends (TUI palette, GUI sidebar) wrap this same logic but collect
// the destination via workspace.PromptScope (ADR-0045 PR-01). The CLI
// surface keeps the two-step flow explicit: --src + --dest + --slash, no
// interactive picker. That keeps copy-template scriptable from CI and
// regression tests without modelling a prompt loop.
var copyTemplateCmd = &cobra.Command{
	Use:   "copy-template",
	Short: "Fork an existing manifest into a draft sibling under a destination scope",
	Long: `Fork an existing manifest into a draft sibling.

` + "`packwright copy-template`" + ` is the headless face of the /copy-template
slash command. It reads the source YAML, rewrites its top-level slash to
` + "`--slash`" + `, injects ` + "`_draft: true`" + ` plus a ` + "`_copied_from`" + ` provenance
header, and atomically writes the result under ` + "`<dest>/drafts/<slug>.yaml`" + `.

The destination is the scope directory (a project root such as
` + "`projects/acme/dev`" + `, or the independent ` + "`commands`" + ` directory).
copy-template enforces that the produced file lives under ` + "`drafts/`" + ` so a
half-edited copy can never be confused with a deployable manifest.

The copy is a draft until ` + "`packwright promote-template`" + ` removes the
` + "`_draft: true`" + ` line. Drafts load, validate, and preview normally; only
the deploy path is blocked, via the typed ErrDraftNotPromoted error
surfaced through the engine's existing error pipeline.`,
	Args: cobra.NoArgs,
	RunE: runCopyTemplate,
}

// copyTemplateFlags is the parsed CLI surface for `packwright copy-template`.
type copyTemplateFlags struct {
	src   string
	dest  string
	slash string
}

var copyTemplateOpts copyTemplateFlags

func init() {
	copyTemplateCmd.Flags().StringVar(&copyTemplateOpts.src, "src", "",
		"Path to the source manifest YAML to fork (required)")
	copyTemplateCmd.Flags().StringVar(&copyTemplateOpts.dest, "dest", "",
		"Destination scope directory (e.g. projects/acme/dev). The copy lands under <dest>/drafts/ (required)")
	copyTemplateCmd.Flags().StringVar(&copyTemplateOpts.slash, "slash", "",
		"New slash command for the copy, e.g. /alb-shared (required, must start with /)")

	registerSubcommand(copyTemplateCmd)
}

// runCopyTemplate composes the flag values into a CopyTemplate call. Flag
// validation is intentionally light: scaffold.CopyTemplate runs the slash /
// destination checks itself so the error pipeline is single-sourced.
func runCopyTemplate(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()

	if copyTemplateOpts.src == "" {
		return fmt.Errorf("copy-template: --src is required")
	}
	if copyTemplateOpts.dest == "" {
		return fmt.Errorf("copy-template: --dest is required")
	}
	if copyTemplateOpts.slash == "" {
		return fmt.Errorf("copy-template: --slash is required")
	}

	dstPath := filepath.Join(copyTemplateOpts.dest, "drafts", slugForSlash(copyTemplateOpts.slash)+".yaml")
	if err := scaffold.CopyTemplate(copyTemplateOpts.src, dstPath, copyTemplateOpts.slash); err != nil {
		return err
	}

	fmt.Fprintf(out, "Wrote draft %s -> %s\n", copyTemplateOpts.slash, dstPath)
	fmt.Fprintf(out, "Edit, then run: packwright promote-template %s\n", dstPath)
	return nil
}

// slugForSlash converts a slash command into a filesystem-safe slug. The
// leading slash is stripped and inner slashes become dashes — matching
// the convention scaffold.slugFromSlash uses, kept in sync by hand so the
// CLI does not need to import the unexported helper.
func slugForSlash(slash string) string {
	s := strings.TrimPrefix(slash, "/")
	return strings.ReplaceAll(s, "/", "-")
}
