package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bannaarr01/packwright/awsx"
	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/audit"
	// Importing the scanners package for its init side effect — every
	// concrete Scanner self-registers with audit.Default from this
	// import. The audit command consumes audit.Default.All().
	_ "github.com/bannaarr01/packwright/internal/audit/scanners"
)

// auditCmd is the `packwright audit` subcommand. It is the headless
// CLI surface for the audit feature; the `/audit` slash command in the
// TUI/GUI palette routes through the same audit.Run engine once the
// palette dispatch lands in a later MVP-6 PR.
//
// The command takes no arguments. It loads the user's profile and
// region (from --profile / --region or the Packwright config), opens
// an awsx.Client, resolves the account via STS, and drives every
// registered scanner concurrently. Output is a plain-text summary —
// the structured event stream lives in the engine and is rendered by
// the TUI strip in PR-02.
var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Walk AWS resources read-only and report what exists",
	Long: `Walk every registered AWS scanner read-only and report the inventory.

` + "`packwright audit`" + ` is the headless face of the /audit slash command. It
visits each scanner kind (ec2/instance, rds/db-snapshot, s3/bucket, …)
concurrently, paginates each AWS API fully, and prints a summary table
of resource counts and any per-scanner errors.

Scanners are read-only by construction (ADR-0040): every IAM action
they declare is validated against a Describe*/List*/Get* allowlist at
program startup, so this command can never mutate AWS state.`,
	Args: cobra.NoArgs,
	RunE: runAudit,
}

// auditFlags is the parsed CLI surface for `packwright audit`. Wiring
// flags through a struct keeps RunE small and trivially testable.
type auditFlags struct {
	profile string
	region  string
}

var auditOpts auditFlags

func init() {
	auditCmd.Flags().StringVar(&auditOpts.profile, "profile", "", "AWS profile to use (defaults to the config-active profile)")
	auditCmd.Flags().StringVar(&auditOpts.region, "region", "", "AWS region to scan (defaults to the profile's region)")
	registerSubcommand(auditCmd)
}

// runAudit is the auditCmd RunE. It orchestrates awsx.New, STS
// verification, scanner registration, the worker pool, and a final
// summary write. Every failure path returns a non-nil error so cobra
// surfaces it with a non-zero exit code.
func runAudit(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	home, err := config.Home()
	if err != nil {
		return fmt.Errorf("audit: resolve home: %w", err)
	}

	client, err := awsx.New(ctx, auditOpts.profile, auditOpts.region, home, nil)
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	id, err := awsx.Verify(ctx, client)
	if err != nil {
		return err
	}

	ac := audit.NewFromAWSX(client, id.Account, nil)
	scanners := audit.Default.All()

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Scanning account %s in %s with %d scanners…\n",
		id.Account, client.Region(), len(scanners))

	res := drainAudit(ctx, scanners, ac, out)

	writeSummary(out, res)
	if len(res.Errors) > 0 {
		return fmt.Errorf("audit: %d scanner(s) failed", len(res.Errors))
	}
	return nil
}

// drainAudit runs the pool, prints per-scanner Done lines as events
// arrive, and returns the final Result. It is split out so tests can
// drive it with a synthetic scanner list and an in-memory writer.
func drainAudit(ctx context.Context, scanners []audit.Scanner, c *audit.Client, out io.Writer) audit.Result {
	events, result := audit.Run(ctx, scanners, c, audit.RunOptions{})
	for ev := range events {
		switch ev.Type {
		case audit.EventDone:
			fmt.Fprintf(out, "  ✓ %-28s %d resource(s)\n", ev.Kind, ev.Count)
		case audit.EventError:
			fmt.Fprintf(out, "  ✗ %-28s %v\n", ev.Kind, ev.Err)
		case audit.EventWarn:
			fmt.Fprintf(out, "  ! %-28s %s\n", ev.Kind, ev.Msg)
		}
	}
	return <-result
}

// writeSummary renders the final aggregate the way the headless CLI
// wants it: a single line per kind, alphabetised, followed by the
// total resource count and any error tally.
func writeSummary(out io.Writer, res audit.Result) {
	byKind := map[string]int{}
	for _, r := range res.Resources {
		byKind[r.Kind]++
	}
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Summary:")
	for _, k := range kinds {
		fmt.Fprintf(out, "  %-28s %d\n", k, byKind[k])
	}
	fmt.Fprintf(out, "  %-28s %d\n", "total resources", len(res.Resources))
	if len(res.Errors) > 0 {
		fmt.Fprintf(out, "  %-28s %d (%s)\n", "scanner errors", len(res.Errors), strings.Join(errorKinds(res.Errors), ","))
	}
}

// errorKinds returns the sorted list of kinds in the Errors map for a
// stable summary line.
func errorKinds(errs map[string]error) []string {
	out := make([]string, 0, len(errs))
	for k := range errs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
