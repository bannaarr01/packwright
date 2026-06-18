package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/manifest"
	"github.com/bannaarr01/packwright/internal/record"
	"github.com/bannaarr01/packwright/internal/scaling"
	"github.com/bannaarr01/packwright/internal/update"
)

// scaleCmd is the `packwright scale <stack>` subcommand and the entry point
// the TUI / GUI palette's `/scale` slash command routes through.
//
// Per ADR-0049 scaling is a parameter-only update via the ADR-0048 change-set
// flow: the manifest declares a `scaling:` block (a list of form fields that
// may be tweaked), the command loads the stack record (ADR-0046), clamps the
// user's deltas through any env_guard (warn-logged, never silent), gates on
// the ADR-0036 consent modal when an env guard sets require_confirmation, and
// hands off to update.Stack — PR-06's coordinator — for the actual change-set
// lifecycle. The HistoryKind appended to the stack record is "scale" rather
// than "update" (see record.KindScale).
//
// No SDK scaling tools (UpdateService, ModifyDBInstance, …) are added: every
// scale flows through CloudFormation so the template remains the source of
// truth.
var scaleCmd = &cobra.Command{
	Use:   "scale <stack>",
	Short: "Scale a deployed stack by rerunning its CFN change-set with new parameter values",
	Long: `Scale a deployed stack via the ADR-0049 parameter-override path.

` + "`packwright scale <stack>`" + ` is the headless face of the /scale slash
command. It loads the stack record, renders only the manifest's scaling[]
fields pre-filled with the stack's current parameter values, clamps any
value that crosses an env_guard (with one warn-level log line per clamp —
clamps are never silent), and hands the result to PR-06's update.Stack
coordinator with HistoryKind=scale.

For the headless CLI flow, pass new values via --param key=value (one per
scaling-eligible parameter; non-scaling parameters are rejected). The TUI
/ GUI palette renders an interactive form via the scaleCollect seam.

Examples:

  packwright scale alb-dev-stack --param DesiredCount=5
  packwright scale alb-prd-stack --param DesiredCount=30 --yes`,
	Args: cobra.ExactArgs(1),
	RunE: runScale,
}

// scaleFlags is the parsed CLI surface for `packwright scale`.
type scaleFlags struct {
	profile      string
	region       string
	params       []string
	yes          bool
	pollInterval time.Duration
}

var scaleOpts scaleFlags

func init() {
	scaleCmd.Flags().StringVar(&scaleOpts.profile, "profile", "", "AWS profile to use (defaults to the config-active profile)")
	scaleCmd.Flags().StringVar(&scaleOpts.region, "region", "", "AWS region (defaults to the profile's region)")
	scaleCmd.Flags().StringArrayVar(&scaleOpts.params, "param", nil, "Scaling delta, key=value. Repeatable. Each key must appear in the manifest's scaling block.")
	scaleCmd.Flags().BoolVar(&scaleOpts.yes, "yes", false, "Approve both the scale-on-env consent and any replacement consent without prompting")
	scaleCmd.Flags().DurationVar(&scaleOpts.pollInterval, "poll-interval", 0, "Override the DescribeChangeSet polling cadence (default: 1s)")
	registerSubcommand(scaleCmd)
}

// ScaleConsentDecision mirrors update.ConsentDecision but is named for the
// ADR-0049 scale-consent gate (which fires on require_confirmation env guards
// regardless of whether the change set carries any replacements). The TUI /
// GUI front-ends translate their consent modal's outcome to one of these.
type ScaleConsentDecision int

// Recognised scale-consent decisions.
const (
	// ScaleConsentDeny aborts the scale before any AWS write occurs.
	ScaleConsentDeny ScaleConsentDecision = iota
	// ScaleConsentApprove allows the flow to proceed into update.Stack.
	ScaleConsentApprove
)

// ScaleConsentPayload is the context handed to the scale-consent gate. It
// carries the reason ("scale on prd env"), the env, the stack name, the
// scaling deltas the user submitted, and any clamps that were applied — so
// the front-end can render an honest modal.
type ScaleConsentPayload struct {
	StackName string
	Env       string
	Reason    string
	Deltas    map[string]string
	Clamps    []scaling.ClampEvent
}

// ScaleConsentGate gates the flow when any active env guard sets
// require_confirmation. The default implementation in the CLI gates on
// --yes / stdin; the TUI and GUI replace it from init() with their own
// modal-driven gates.
type ScaleConsentGate func(ctx context.Context, payload ScaleConsentPayload) ScaleConsentDecision

// Package-level seams. The TUI / GUI front-ends override scaleCollectInput
// and scaleConsentGate from init() to replace the default headless flows.
// Tests override every seam below to drive the coordinator without hitting
// AWS.
var (
	// scaleCollectInput renders the scaling-only form pre-filled with the
	// stack's current values and returns the user-submitted deltas keyed by
	// Spec.Param. The default implementation derives the deltas from
	// --param flags (headless CLI). Front-ends override it for the
	// palette modal.
	scaleCollectInput = func(_ context.Context, form scaling.Form) (map[string]string, error) {
		return parseScaleParams(scaleOpts.params, form)
	}

	// scaleConsentGate is the ADR-0036 consent surface for the
	// require_confirmation env guard. The default reads stdin (or returns
	// approve on --yes); front-ends wire their modal-driven gate.
	scaleConsentGate = func(ctx context.Context, payload ScaleConsentPayload) ScaleConsentDecision {
		return cliScaleConsent(scaleOpts.yes, os.Stdin, os.Stderr)(ctx, payload)
	}

	// scaleUpdateStack calls into PR-06's update.Stack coordinator. The
	// default is the real call; tests inject a stub that asserts the
	// constructed StackInput / StackOptions.
	scaleUpdateStack = update.Stack

	// scaleNewStore returns the record.Store rooted at the Packwright
	// home directory. Tests inject a Store backed by t.TempDir().
	scaleNewStore = func() (*record.Store, error) {
		home, err := config.Home()
		if err != nil {
			return nil, fmt.Errorf("scale: resolve home: %w", err)
		}
		return record.NewStore(home), nil
	}

	// scaleReadTemplate loads the on-disk template body referenced by the
	// manifest. Indirected for tests; production calls os.ReadFile.
	scaleReadTemplate = os.ReadFile

	// scaleLogger is the slog logger /scale uses to emit one warn line per
	// ClampEvent (ADR-0049 forbids silent clamps). Tests swap it out for
	// a handler that captures records into a buffer.
	scaleLogger = slog.Default
)

// runScale is the cobra RunE function. It composes the headless flow:
// resolve the stack record, load the manifest, gather deltas, clamp them,
// gate on consent, and hand off to update.Stack.
func runScale(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	stackName := args[0]
	out := cmd.OutOrStdout()

	if stackName == "" {
		return errors.New("scale: stack name is required")
	}

	store, err := scaleNewStore()
	if err != nil {
		return err
	}
	rec, err := store.Find(stackName)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("scale: no stack record found for %q — has it been deployed?", stackName)
		}
		return fmt.Errorf("scale: look up record for %q: %w", stackName, err)
	}

	if rec.Manifest.Source == "" {
		return fmt.Errorf("scale: stack record %q has no manifest source — cannot resolve scaling block", stackName)
	}
	m, err := manifest.Load(rec.Manifest.Source)
	if err != nil {
		return fmt.Errorf("scale: load manifest %q: %w", rec.Manifest.Source, err)
	}
	if len(m.Scaling) == 0 {
		return fmt.Errorf("scale: manifest %q declares no scaling block — /scale is not available for this stack",
			rec.Manifest.Source)
	}

	specs := manifestScalingToSpecs(m.Scaling)
	form := scaling.BuildForm(stackName, rec.Env, rec.Parameters, specs)

	deltas, err := scaleCollectInput(ctx, form)
	if err != nil {
		return fmt.Errorf("scale: collect input: %w", err)
	}
	if len(deltas) == 0 {
		fmt.Fprintln(out, "No scaling changes submitted.")
		return nil
	}

	result, err := scaling.BuildParams(rec.Parameters, deltas, rec.Env, specs)
	if err != nil {
		return fmt.Errorf("scale: build params: %w", err)
	}
	for _, c := range result.Clamps {
		logClamp(c)
		fmt.Fprintf(out, "Clamped %s on %s env: %s → %s (%s=%d)\n",
			c.Param, c.Env, c.Requested, c.Effective, c.Bound, c.Limit)
	}

	if result.RequireConsent {
		decision := scaleConsentGate(ctx, ScaleConsentPayload{
			StackName: stackName,
			Env:       rec.Env,
			Reason:    result.ConsentReason,
			Deltas:    deltas,
			Clamps:    result.Clamps,
		})
		if decision != ScaleConsentApprove {
			fmt.Fprintf(out, "Scale cancelled — consent denied (%s).\n", result.ConsentReason)
			return nil
		}
	}

	templatePath := resolveTemplatePath(rec.Manifest.Source, m)
	if templatePath == "" {
		return fmt.Errorf("scale: manifest %q has no template.path", rec.Manifest.Source)
	}
	body, err := scaleReadTemplate(templatePath)
	if err != nil {
		return fmt.Errorf("scale: read template %q: %w", templatePath, err)
	}

	client, err := newAWSClientForUpdate(ctx, scaleOpts.profile, scaleOpts.region)
	if err != nil {
		return err
	}
	api, err := changeSetAPIFromClient(ctx, client)
	if err != nil {
		return err
	}

	res, err := scaleUpdateStack(ctx, update.StackInput{
		StackName:          stackName,
		TemplateBody:       string(body),
		Parameters:         result.Params,
		PreviousParameters: parametersToMap(rec.Parameters),
		Description:        fmt.Sprintf("packwright /scale on %s env", rec.Env),
	}, update.StackOptions{
		API:          api,
		Consent:      cliConsent(scaleOpts.yes, cmd.InOrStdin(), cmd.ErrOrStderr()),
		Harvest:      scaleHarvester(store, rec.Project, rec.Env),
		PollInterval: scaleOpts.pollInterval,
	})
	if err != nil {
		return fmt.Errorf("scale: %w", err)
	}

	renderScaleOutcome(out, res, result.ConsentReason)

	if res.Outcome == update.OutcomeExecuted && res.Events != nil {
		for ev := range res.Events {
			fmt.Fprintf(out, "[cfn] %s %s %s\n", ev.ResourceStatus, ev.ResourceType, ev.LogicalResourceID)
		}
	}
	return nil
}

// resolveTemplatePath returns the absolute on-disk path of the manifest's
// template. The manifest's Template.Path is resolved relative to the
// manifest file's own directory, mirroring how the action engine handles
// relative paths in the resource pipeline.
func resolveTemplatePath(manifestSource string, m *manifest.Manifest) string {
	if m.Template == nil || m.Template.Path == "" {
		return ""
	}
	p := m.Template.Path
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(filepath.Dir(manifestSource), p)
}

// parseScaleParams converts the --param key=value flags into the delta map
// scaling.BuildParams consumes. Every key MUST appear in the form (it must
// be a scaling-eligible parameter); a typo is loud-failed at parse time.
func parseScaleParams(in []string, form scaling.Form) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	eligible := make(map[string]struct{}, len(form.Targets))
	for _, t := range form.Targets {
		eligible[t.Spec.Param] = struct{}{}
	}
	out := make(map[string]string, len(in))
	for _, raw := range in {
		i := strings.Index(raw, "=")
		if i <= 0 {
			return nil, fmt.Errorf("scale: invalid --param %q: expected key=value", raw)
		}
		key, val := raw[:i], raw[i+1:]
		if _, ok := eligible[key]; !ok {
			names := make([]string, 0, len(eligible))
			for k := range eligible {
				names = append(names, k)
			}
			sort.Strings(names)
			return nil, fmt.Errorf("scale: --param %q is not a scaling-eligible field (eligible: %s)",
				key, strings.Join(names, ", "))
		}
		out[key] = val
	}
	return out, nil
}

// parametersToMap converts a record.Parameters (a typed alias of
// map[string]string) into the plain map[string]string update.StackInput
// expects.
func parametersToMap(p record.Parameters) map[string]string {
	if p == nil {
		return nil
	}
	out := make(map[string]string, len(p))
	for k, v := range p {
		out[k] = v
	}
	return out
}

// cliScaleConsent is the headless scale-consent gate. --yes always approves;
// otherwise the gate prints the reason / deltas / clamps and reads a y/n
// line from stdin. Mirrors cliConsent in cmd_update.go but for the
// require_confirmation env-guard case rather than the replacement case.
func cliScaleConsent(autoYes bool, in io.Reader, out io.Writer) ScaleConsentGate {
	if autoYes {
		return func(context.Context, ScaleConsentPayload) ScaleConsentDecision {
			return ScaleConsentApprove
		}
	}
	return func(_ context.Context, p ScaleConsentPayload) ScaleConsentDecision {
		fmt.Fprintln(out, "")
		fmt.Fprintf(out, "Scale consent required: %s\n", p.Reason)
		fmt.Fprintf(out, "  stack: %s   env: %s\n", p.StackName, p.Env)
		if len(p.Deltas) > 0 {
			keys := make([]string, 0, len(p.Deltas))
			for k := range p.Deltas {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			fmt.Fprintln(out, "  deltas:")
			for _, k := range keys {
				fmt.Fprintf(out, "    %s = %s\n", k, p.Deltas[k])
			}
		}
		if len(p.Clamps) > 0 {
			fmt.Fprintln(out, "  clamps:")
			for _, c := range p.Clamps {
				fmt.Fprintf(out, "    %s: %s → %s (%s=%d)\n",
					c.Param, c.Requested, c.Effective, c.Bound, c.Limit)
			}
		}
		fmt.Fprint(out, `Type "yes" to proceed, anything else to cancel: `)
		var line string
		fmt.Fscanln(in, &line)
		if strings.EqualFold(strings.TrimSpace(line), "yes") {
			return ScaleConsentApprove
		}
		return ScaleConsentDeny
	}
}

// scaleHarvester returns an update.Harvester that appends a KindScale
// history entry to the existing stack record and overwrites its Parameters
// with the post-clamp map. The harvester is intentionally narrow: it does
// not re-run DescribeStacks / DescribeStackResources — the launch-time
// reconciliation in PR-02 refreshes status fields on next start. What
// matters for this PR is that the record carries the new parameters and a
// "scale" history row so subsequent `/scale` invocations see the latest
// state.
func scaleHarvester(store *record.Store, project, env string) update.Harvester {
	return func(_ context.Context, info update.HarvestInfo) error {
		rec, err := store.Read(project, env, info.StackName)
		if err != nil {
			return fmt.Errorf("scale: read record before harvest: %w", err)
		}
		rec.Parameters = record.Parameters{}
		for k, v := range info.ParametersSent {
			rec.Parameters[k] = v
		}
		now := time.Now().UTC()
		rec.LastUpdatedAt = now
		rec.History = append(rec.History, record.HistoryEntry{
			At:             now,
			Kind:           record.KindScale,
			Result:         record.ResultSuccess,
			ChangesetID:    info.ChangeSetID,
			ParametersDiff: formatParameterDeltas(info.Diff.ParameterDeltas),
		})
		// Re-cap because the engine writes records back through Store.Write
		// without revisiting the slice length.
		if len(rec.History) > record.MaxHistoryEntries {
			rec.History = rec.History[len(rec.History)-record.MaxHistoryEntries:]
		}
		return store.Write(rec)
	}
}

// formatParameterDeltas renders the change set's parameter deltas as a
// short, line-per-delta string suitable for the record's ParametersDiff
// field. The format is intentionally compact: callers see "<key>: <old> →
// <new>" so a `cat`'d record reads cleanly without a separate parser.
func formatParameterDeltas(deltas []update.ParameterDelta) string {
	if len(deltas) == 0 {
		return ""
	}
	parts := make([]string, 0, len(deltas))
	for _, d := range deltas {
		parts = append(parts, fmt.Sprintf("%s: %s → %s", d.Key, d.Old, d.New))
	}
	return strings.Join(parts, "; ")
}

// renderScaleOutcome prints the human-readable summary of update.Stack's
// result for the headless CLI. The TUI / GUI front-ends own their own
// renderers; this is the fallback for `packwright scale` runs.
func renderScaleOutcome(w io.Writer, r update.StackResult, scaleReason string) {
	switch r.Outcome {
	case update.OutcomeNoChanges:
		fmt.Fprintln(w, r.Notice)
	case update.OutcomeConsentDenied:
		fmt.Fprintln(w, r.Notice)
	case update.OutcomeExecuted:
		if scaleReason != "" {
			fmt.Fprintf(w, "Scale approved (%s).\n", scaleReason)
		}
		fmt.Fprintln(w, r.Notice)
	default:
		fmt.Fprintf(w, "Scale completed with outcome %s.\n", r.Outcome)
	}
}

// manifestScalingToSpecs converts the manifest's []ScalingSpec into the
// runtime []scaling.Spec the scaling package operates on. The manifest
// type carries YAML tags; the scaling type doesn't. The transformation is
// field-for-field with a tiny lift for the env-guard map.
func manifestScalingToSpecs(in []manifest.ScalingSpec) []scaling.Spec {
	out := make([]scaling.Spec, 0, len(in))
	for _, s := range in {
		spec := scaling.Spec{
			Param:  s.Param,
			Label:  s.Label,
			Kind:   scaling.Kind(s.Kind),
			Min:    s.Min,
			Max:    s.Max,
			Step:   s.Step,
			Values: s.Values,
		}
		if len(s.EnvGuards) > 0 {
			spec.EnvGuards = make(map[string]scaling.EnvGuard, len(s.EnvGuards))
			for env, g := range s.EnvGuards {
				spec.EnvGuards[env] = scaling.EnvGuard{
					Min:                 g.Min,
					Max:                 g.Max,
					RequireConfirmation: g.RequireConfirmation,
				}
			}
		}
		out = append(out, spec)
	}
	return out
}

// logClamp emits one warn line per ClampEvent. ADR-0049: env-guard clamps
// must be visible, not silent. The structured fields let downstream
// log-shippers index per-param / per-env clamp counts; the formatted
// message keeps the audit log readable as plain text.
func logClamp(c scaling.ClampEvent) {
	scaleLogger().LogAttrs(context.Background(), slog.LevelWarn,
		"scaling clamp",
		slog.String("param", c.Param),
		slog.String("env", c.Env),
		slog.String("requested", c.Requested),
		slog.String("effective", c.Effective),
		slog.String("bound", c.Bound),
		slog.Int("limit", c.Limit),
	)
}

// SetScaleInputCollector overrides the front-end input collector. TUI and
// GUI front-ends call this from their init() to wire the scaling form
// renderer into /scale. Calls with nil are ignored so a partially-wired
// front-end cannot silently erase a previously-registered collector.
func SetScaleInputCollector(fn func(context.Context, scaling.Form) (map[string]string, error)) {
	if fn != nil {
		scaleCollectInput = fn
	}
}

// SetScaleConsentGate overrides the ADR-0036 consent gate for the
// require_confirmation env-guard path. TUI and GUI front-ends call this
// from their init() to wire their modal in. Nil is ignored for the same
// reason SetScaleInputCollector ignores nil.
func SetScaleConsentGate(fn ScaleConsentGate) {
	if fn != nil {
		scaleConsentGate = fn
	}
}
