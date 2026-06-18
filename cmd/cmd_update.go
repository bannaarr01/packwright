package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/spf13/cobra"

	"github.com/bannaarr01/packwright/awsx"
	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/update"
	"github.com/bannaarr01/packwright/render/cfn"
)

// updateCmd is the `packwright update` headless subcommand and the entry
// point the TUI / GUI palette's `/update` slash command routes through.
//
// The flow it implements is the in-place update from ADR-0048:
// CreateChangeSet → poll DescribeChangeSet → render diff → consent for any
// replacement → ExecuteChangeSet → stream events → harvest. The script
// driver (deploy.sh) is intentionally bypassed for ExecuteChangeSet to
// avoid creating a redundant second change set; that is the only ADR-0008
// carve-out and it is bounded to this command's change-set lifecycle.
//
// On a "no updates are to be performed" change set the command emits a
// benign "No changes — nothing to deploy" notice and exits 0; the
// orchestrator never writes a stack record for that path.
var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update an existing CloudFormation stack with a change-set preview",
	Long: `Update an existing CloudFormation stack in-place.

Per ADR-0048 the engine runs a change-set preview before any side effect:
the template + parameters are submitted as a change set, the diff is
displayed, replacements require explicit consent (ADR-0036), and only
then is ExecuteChangeSet called. A "no changes" change set is reported as
a benign notice — not an error.

This subcommand is the headless face of the ` + "`/update`" + ` slash command
and the sidebar "Update" entry point in the TUI / GUI front-ends.

Examples:

  packwright update --stack acme-dev-alb --template-file ./alb.yaml \
    --param VpcId=vpc-0abc1234 --capability CAPABILITY_IAM`,
	Args: cobra.NoArgs,
	RunE: runUpdate,
}

// updateFlags is the parsed CLI surface for `packwright update`.
type updateFlags struct {
	profile      string
	region       string
	stack        string
	templateFile string
	templateURL  string
	params       []string
	capabilities []string
	description  string
	yes          bool
	pollInterval time.Duration
}

var updateOpts updateFlags

func init() {
	updateCmd.Flags().StringVar(&updateOpts.profile, "profile", "", "AWS profile to use (defaults to the config-active profile)")
	updateCmd.Flags().StringVar(&updateOpts.region, "region", "", "AWS region (defaults to the profile's region)")
	updateCmd.Flags().StringVar(&updateOpts.stack, "stack", "", "Existing stack name to update (required)")
	updateCmd.Flags().StringVar(&updateOpts.templateFile, "template-file", "", "Path to the new template (mutually exclusive with --template-url)")
	updateCmd.Flags().StringVar(&updateOpts.templateURL, "template-url", "", "S3 URL to the new template (mutually exclusive with --template-file)")
	updateCmd.Flags().StringArrayVar(&updateOpts.params, "param", nil, "Parameter override, key=value. Repeatable.")
	updateCmd.Flags().StringArrayVar(&updateOpts.capabilities, "capability", nil, "CFN capability (CAPABILITY_IAM / CAPABILITY_NAMED_IAM / CAPABILITY_AUTO_EXPAND). Repeatable.")
	updateCmd.Flags().StringVar(&updateOpts.description, "description", "", "Change-set description (visible in the AWS console)")
	updateCmd.Flags().BoolVar(&updateOpts.yes, "yes", false, "Approve replacements without prompting (headless equivalent of the consent modal)")
	updateCmd.Flags().DurationVar(&updateOpts.pollInterval, "poll-interval", 0, "Override the DescribeChangeSet polling cadence (default: 1s)")
	registerSubcommand(updateCmd)
}

// runUpdate is the cobra RunE function. It loads the AWS client, builds the
// StackInput from flags, wires the consent gate (stdin / --yes), drives
// update.Stack, and renders the diff + outcome to the command's writers.
func runUpdate(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if updateOpts.stack == "" {
		return errors.New("update: --stack is required")
	}
	if updateOpts.templateFile == "" && updateOpts.templateURL == "" {
		return errors.New("update: one of --template-file or --template-url is required")
	}
	if updateOpts.templateFile != "" && updateOpts.templateURL != "" {
		return errors.New("update: --template-file and --template-url are mutually exclusive")
	}

	params, err := parseUpdateParams(updateOpts.params)
	if err != nil {
		return err
	}

	var body string
	if updateOpts.templateFile != "" {
		raw, err := os.ReadFile(updateOpts.templateFile)
		if err != nil {
			return fmt.Errorf("update: read template: %w", err)
		}
		body = string(raw)
	}

	client, err := newAWSClientForUpdate(ctx, updateOpts.profile, updateOpts.region)
	if err != nil {
		return err
	}
	api, err := changeSetAPIFromClient(ctx, client)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	stderr := cmd.ErrOrStderr()

	res, err := update.Stack(ctx, update.StackInput{
		StackName:    updateOpts.stack,
		TemplateBody: body,
		TemplateURL:  updateOpts.templateURL,
		Parameters:   params,
		Capabilities: updateOpts.capabilities,
		Description:  updateOpts.description,
	}, update.StackOptions{
		API:          api,
		Consent:      cliConsent(updateOpts.yes, cmd.InOrStdin(), stderr),
		PollInterval: updateOpts.pollInterval,
	})
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}

	renderUpdateDiff(out, res)

	switch res.Outcome {
	case update.OutcomeNoChanges:
		fmt.Fprintln(out, res.Notice)
		return nil
	case update.OutcomeConsentDenied:
		fmt.Fprintln(out, res.Notice)
		return nil
	case update.OutcomeExecuted:
		fmt.Fprintln(out, res.Notice)
		// Drain any events the streamer forwarded so the wait-on-events
		// shape stays consistent with the deploy-script path; in the
		// headless CLI no streamer is wired so this is a no-op.
		if res.Events != nil {
			for ev := range res.Events {
				fmt.Fprintf(out, "[cfn] %s %s %s\n", ev.ResourceStatus, ev.ResourceType, ev.LogicalResourceID)
			}
		}
		return nil
	}
	return fmt.Errorf("update: unexpected outcome %v", res.Outcome)
}

// parseUpdateParams parses --param key=value flags into the map the
// coordinator consumes. A bare key with no = sign is rejected as a typo
// rather than silently treated as "" — every parameter mistake should
// loud-fail at parse time.
func parseUpdateParams(in []string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	for _, raw := range in {
		i := strings.Index(raw, "=")
		if i <= 0 {
			return nil, fmt.Errorf("update: invalid --param %q: expected key=value", raw)
		}
		out[raw[:i]] = raw[i+1:]
	}
	return out, nil
}

// cliConsent returns a ConsentGate suitable for the CLI. When --yes is
// passed (or stdin is not a terminal and no replacements occur), the gate
// auto-approves. Otherwise it prints the replacement summary and reads a
// y/n decision from stdin.
func cliConsent(autoYes bool, in io.Reader, out io.Writer) update.ConsentGate {
	if autoYes {
		return update.AlwaysApproveConsent
	}
	return func(_ context.Context, p update.ReplacementPayload) update.ConsentDecision {
		fmt.Fprintln(out, "")
		fmt.Fprintf(out, "This update REPLACES %d resource(s):\n", p.Count)
		for _, r := range p.Rows {
			fmt.Fprintf(out, "  - %s (%s)", r.LogicalID, r.ResourceType)
			if len(r.PropertyCauses) > 0 {
				fmt.Fprintf(out, " — %s changed", strings.Join(r.PropertyCauses, ", "))
			}
			fmt.Fprintln(out)
		}
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, "A replacement destroys and recreates the resource. Data on the old")
		fmt.Fprintln(out, "resource is not migrated unless your template specifies it.")
		fmt.Fprint(out, "Type \"yes\" to proceed, anything else to cancel: ")
		var line string
		fmt.Fscanln(in, &line)
		if strings.EqualFold(strings.TrimSpace(line), "yes") {
			return update.ConsentApprove
		}
		return update.ConsentDeny
	}
}

// renderUpdateDiff prints the typed Diff as a compact table. The TUI and
// GUI front-ends own their own richer renderers (PR-09 / PR-10); this is
// the headless fallback.
func renderUpdateDiff(w io.Writer, r update.StackResult) {
	if r.Diff.NoChanges {
		return
	}
	a, m, rp, dl := r.Diff.Counts()
	fmt.Fprintf(w, "Change set %s — %d add, %d modify, %d replace, %d delete\n",
		r.ChangeSetName, a, m, rp, dl)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, row := range allRows(r.Diff) {
		causes := ""
		if len(row.PropertyCauses) > 0 {
			causes = " (" + strings.Join(row.PropertyCauses, ", ") + ")"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s%s\n", row.Action, row.LogicalID, row.ResourceType, causes)
	}
	tw.Flush()
	if len(r.Diff.ParameterDeltas) > 0 {
		fmt.Fprintln(w, "Parameter deltas:")
		tw2 := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, p := range r.Diff.ParameterDeltas {
			tag := ""
			if p.CausedReplacement {
				tag = "  ← triggered replacement"
			}
			fmt.Fprintf(tw2, "  %s\t%s\t→\t%s%s\n", p.Key, p.Old, p.New, tag)
		}
		tw2.Flush()
	}
}

func allRows(d update.Diff) []update.ResourceDelta {
	out := make([]update.ResourceDelta, 0, d.Total())
	out = append(out, d.Adds...)
	out = append(out, d.Modifies...)
	out = append(out, d.Replaces...)
	out = append(out, d.Deletes...)
	return out
}

// newAWSClientForUpdate is the seam tests can override to inject a fake
// awsx.Client without going through the global SDK config loader. The
// production implementation forwards to awsx.New.
var newAWSClientForUpdate = func(ctx context.Context, profile, region string) (*awsx.Client, error) {
	home, err := config.Home()
	if err != nil {
		return nil, fmt.Errorf("update: resolve home: %w", err)
	}
	c, err := awsx.New(ctx, profile, region, home, nil)
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	return c, nil
}

// changeSetAPIFromClient builds a cfn.ChangeSetAPI from an awsx.Client.
// The implementation is a package var so tests can swap in an in-process
// fake, and so a future awsx.CloudFormation accessor (per the comment in
// awsx/cfn.go on sibling worktrees) can be plugged in without touching
// this command.
var changeSetAPIFromClient = func(ctx context.Context, client *awsx.Client) (cfn.ChangeSetAPI, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if p := client.Profile(); p != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(p))
	}
	if r := client.Region(); r != "" {
		opts = append(opts, awsconfig.WithRegion(r))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("update: load aws config: %w", err)
	}
	return cloudformation.NewFromConfig(cfg), nil
}
