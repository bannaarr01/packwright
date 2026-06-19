package gui

// This file exposes the MVP-7 stack actions (update / scale / delete) to the
// GUI, mirroring the TUI sidebar's u/s/d flow (tui/update.go) over the same
// coordinator, internal/update.Stack (ADR-0048), and internal/scaling
// (ADR-0049). It is the GUI half of the front-end parity the wiring plan
// scoped as a follow-on.
//
// Replacement consent (ADR-0036). update.Stack only gates *replacements*, and
// it does so by calling a consent gate from inside the call. The TUI bridges
// that blocking gate into its event loop; the GUI instead uses a two-call
// confirm pattern that fits an RPC surface and stays fail-closed:
//
//   1. The frontend calls StackUpdate/StackScale with confirm=false. The gate
//      DENIES, so a non-destructive change still executes and reports, but a
//      change that would REPLACE resources is declined — update.Stack deletes
//      the change set and returns the diff. We surface that as
//      Outcome="needs_confirm" with the replacement rows for a modal.
//   2. If the user confirms, the frontend re-calls with confirm=true. The gate
//      APPROVES, and the (idempotently re-created) change set executes.
//
// The default (confirm=false) therefore never replaces a resource without an
// explicit second, user-driven call — the same fail-closed guarantee the TUI's
// deny-by-default gate gives. Delete is a notice, not a second destructive
// path, exactly as in the TUI.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"

	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/record"
	"github.com/bannaarr01/packwright/internal/scaling"
	"github.com/bannaarr01/packwright/internal/update"
	amanifest "github.com/bannaarr01/packwright/manifest"
	"github.com/bannaarr01/packwright/render/cfn"
)

// StackActionResult is the outcome of a stack mutation (update / scale) or a
// notice (delete), shaped for the frontend.
type StackActionResult struct {
	OK bool `json:"ok"`
	// Outcome is one of: "executed", "no_changes", "needs_confirm", "notice",
	// "error". "needs_confirm" means the change would replace resources (or a
	// scale hit an env guard) — show the diff/reason and re-call with confirm.
	Outcome string `json:"outcome"`
	// Notice is a short human-readable summary of the outcome.
	Notice string `json:"notice"`
	// NeedsConfirm is true when the frontend should show a confirm modal and,
	// on approval, re-call the same action with confirm=true.
	NeedsConfirm bool `json:"needs_confirm"`
	// ConfirmReason explains why confirmation is required (replacement rows or
	// the env-guard scale reason).
	ConfirmReason string `json:"confirm_reason"`
	// Diff is the change-set diff (always populated for update/scale, even on
	// needs_confirm, so the modal can show what would change).
	Diff *DiffPayload `json:"diff,omitempty"`
	// Output carries the drained CFN events after an executed change set.
	Output []string `json:"output"`
	// Error is non-empty when the action failed before/at execution.
	Error string `json:"error"`
}

// DiffPayload is the JSON-friendly projection of update.Diff.
type DiffPayload struct {
	Adds      []DiffRow  `json:"adds"`
	Modifies  []DiffRow  `json:"modifies"`
	Replaces  []DiffRow  `json:"replaces"`
	Deletes   []DiffRow  `json:"deletes"`
	Params    []ParamRow `json:"params"`
	NoChanges bool       `json:"no_changes"`
}

// DiffRow is one resource change in the diff.
type DiffRow struct {
	Action       string   `json:"action"` // add | modify | replace | delete
	LogicalID    string   `json:"logical_id"`
	ResourceType string   `json:"resource_type"`
	Replacement  string   `json:"replacement"` // "True" | "False" | "Conditional" | ""
	IAM          bool     `json:"iam"`
	Causes       []string `json:"causes"`
}

// ParamRow is one parameter delta.
type ParamRow struct {
	Key               string `json:"key"`
	Old               string `json:"old"`
	New               string `json:"new"`
	CausedReplacement bool   `json:"caused_replacement"`
}

// ScalingFormPayload is the form schema for /scale, mirroring scaling.Form.
type ScalingFormPayload struct {
	Resolved  bool            `json:"resolved"`
	StackName string          `json:"stack_name"`
	Env       string          `json:"env"`
	Targets   []ScalingTarget `json:"targets"`
	Error     string          `json:"error"`
}

// ScalingTarget is one editable scaling parameter, pre-filled with its current
// value.
type ScalingTarget struct {
	Param   string   `json:"param"`
	Label   string   `json:"label"`
	Kind    string   `json:"kind"` // integer | enum | string
	Current string   `json:"current"`
	Values  []string `json:"values"` // enum options; empty otherwise
	Min     *int     `json:"min"`
	Max     *int     `json:"max"`
}

// stackChangeSetAPI builds the CFN change-set client for a profile/region pair.
// It mirrors tui/update.go's changeSetAPIFor and is a package var so tests can
// inject a fake cfn.ChangeSetAPI without an AWS account.
var stackChangeSetAPI = func(ctx context.Context, profile, region string) (cfn.ChangeSetAPI, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("gui: stack: load aws config: %w", err)
	}
	return cloudformation.NewFromConfig(cfg), nil
}

// loadStackContext resolves a stack's record, manifest, and template body. It
// is a package var so tests can supply a fixture without touching disk.
var loadStackContext = func(project, env, stack string) (*record.StackRecord, *amanifest.Manifest, string, error) {
	home, err := config.Home()
	if err != nil {
		return nil, nil, "", err
	}
	rec, err := record.NewStore(home).Read(project, env, stack)
	if err != nil {
		return nil, nil, "", fmt.Errorf("read stack record %q: %w", stack, err)
	}
	mf, err := amanifest.Load(rec.Manifest.Source)
	if err != nil {
		return nil, nil, "", fmt.Errorf("load manifest %q: %w", rec.Manifest.Source, err)
	}
	tmpl := resolveStackTemplatePath(rec.Manifest.Source, mf)
	if tmpl == "" {
		return nil, nil, "", fmt.Errorf("manifest %s has no template path", rec.Manifest.Slash)
	}
	body, err := os.ReadFile(tmpl)
	if err != nil {
		return nil, nil, "", fmt.Errorf("read template %s: %w", tmpl, err)
	}
	return rec, mf, string(body), nil
}

// StackUpdate runs the in-place change-set update for a stack: it applies the
// manifest's current template to the deployed stack using its current
// parameters. confirm=false previews (and denies any replacement); confirm=true
// approves a previewed replacement. See the file header for the two-call flow.
func (a *App) StackUpdate(project, env, stack string, confirm bool) StackActionResult {
	rec, _, body, err := loadStackContext(project, env, stack)
	if err != nil {
		return stackErr(err)
	}
	current := map[string]string(rec.Parameters)
	return a.runStackChange(rec, body, current, current, "packwright /update", confirm)
}

// ScalingForm returns the scaling form schema for a stack whose manifest
// declares a scaling block, pre-filled with the stack's current parameter
// values. Resolved is false (with Error set) when the stack has no scaling.
func (a *App) ScalingForm(project, env, stack string) ScalingFormPayload {
	rec, mf, _, err := loadStackContext(project, env, stack)
	if err != nil {
		return ScalingFormPayload{Error: err.Error()}
	}
	if len(mf.Scaling) == 0 {
		return ScalingFormPayload{Error: "this stack's manifest declares no scaling block"}
	}
	specs := stackScalingSpecs(mf.Scaling)
	form := scaling.BuildForm(stack, rec.Env, map[string]string(rec.Parameters), specs)
	targets := make([]ScalingTarget, 0, len(form.Targets))
	for _, t := range form.Targets {
		label := t.Spec.Label
		if label == "" {
			label = t.Spec.Param
		}
		targets = append(targets, ScalingTarget{
			Param:   t.Spec.Param,
			Label:   label,
			Kind:    string(t.Spec.Kind),
			Current: t.Current,
			Values:  t.Spec.Values,
			Min:     t.Spec.Min,
			Max:     t.Spec.Max,
		})
	}
	return ScalingFormPayload{Resolved: true, StackName: form.StackName, Env: form.Env, Targets: targets}
}

// StackScale applies a parameter-only change computed by scaling.BuildParams.
// deltas is field-id → new value from ScalingForm. An env guard with
// require_confirmation returns Outcome="needs_confirm" before any AWS call when
// confirm=false; otherwise the change runs through the same coordinator as
// StackUpdate (replacements still gated by confirm).
func (a *App) StackScale(project, env, stack string, deltas map[string]string, confirm bool) StackActionResult {
	rec, mf, body, err := loadStackContext(project, env, stack)
	if err != nil {
		return stackErr(err)
	}
	if len(mf.Scaling) == 0 {
		return StackActionResult{Outcome: "error", Error: "this stack's manifest declares no scaling block"}
	}
	specs := stackScalingSpecs(mf.Scaling)
	current := map[string]string(rec.Parameters)
	res, err := scaling.BuildParams(current, deltas, rec.Env, specs)
	if err != nil {
		return stackErr(err)
	}
	// ADR-0049: clamps are never silent.
	out := make([]string, 0, len(res.Clamps))
	for _, c := range res.Clamps {
		a.logger.Warn("gui: scaling clamp",
			"param", c.Param, "env", c.Env, "requested", c.Requested, "effective", c.Effective)
		out = append(out, fmt.Sprintf("clamped %s: %s → %s (%s guard on %s)",
			c.Param, c.Requested, c.Effective, c.Bound, c.Env))
	}
	// Env-guard confirmation gates before any AWS call (ADR-0049).
	if res.RequireConsent && !confirm {
		return StackActionResult{
			Outcome:       "needs_confirm",
			NeedsConfirm:  true,
			ConfirmReason: res.ConsentReason,
			Output:        out,
		}
	}
	desc := "packwright /scale on " + rec.Env + " env"
	r := a.runStackChange(rec, body, res.Params, current, desc, confirm)
	// Preserve the clamp lines ahead of any execution output.
	r.Output = append(out, r.Output...)
	return r
}

// StackDelete is the GUI delete entry point. Like the TUI it is a notice, not a
// second destructive path: the cascading-delete engine and the audit deletion
// tray own that machinery (ADR-0053).
func (a *App) StackDelete(project, env, stack string) StackActionResult {
	a.logger.Info("gui: delete requested", "project", project, "env", env, "stack", stack)
	return StackActionResult{
		Outcome: "notice",
		Notice: "Stack deletion runs through the audit deletion tray, which applies " +
			"dependency-aware batch consent. Run `packwright delete-resource` for the " +
			"headless cascading-delete flow, or use the TUI and pick /audit. The GUI " +
			"deliberately does not expose a second destructive path.",
	}
}

// runStackChange is the shared coordinator call for update and scale. It builds
// the change-set client, runs update.Stack with a confirm-driven consent gate,
// and maps the result for the frontend.
func (a *App) runStackChange(rec *record.StackRecord, body string, params, prev map[string]string, desc string, confirm bool) StackActionResult {
	ctx := context.Background()
	profile, region := awsCoords(rec)
	api, err := stackChangeSetAPI(ctx, profile, region)
	if err != nil {
		return stackErr(err)
	}
	gate := func(_ context.Context, _ update.ReplacementPayload) update.ConsentDecision {
		if confirm {
			return update.ConsentApprove
		}
		return update.ConsentDeny
	}
	res, err := update.Stack(ctx, update.StackInput{
		StackName:          rec.StackName,
		TemplateBody:       body,
		Parameters:         params,
		PreviousParameters: prev,
		Description:        desc,
	}, update.StackOptions{API: api, Consent: gate})
	return resultFromStack(res, err)
}

// resultFromStack maps update.StackResult (and any error) into the frontend
// shape, draining the CFN event stream on an executed change set.
func resultFromStack(res update.StackResult, err error) StackActionResult {
	out := StackActionResult{Diff: diffPayload(res.Diff)}
	if err != nil {
		out.Outcome = "error"
		out.Error = err.Error()
		return out
	}
	switch res.Outcome {
	case update.OutcomeExecuted:
		out.OK = true
		out.Outcome = "executed"
		out.Notice = res.Notice
		out.Output = drainStackEvents(res.Events)
	case update.OutcomeNoChanges:
		out.OK = true
		out.Outcome = "no_changes"
		out.Notice = res.Notice
	case update.OutcomeConsentDenied:
		// A replacement was declined by the deny gate: surface it as a preview
		// the user can confirm (re-call with confirm=true).
		out.Outcome = "needs_confirm"
		out.NeedsConfirm = true
		out.ConfirmReason = replacementReason(res.Replacement)
		out.Notice = res.Notice
	default:
		out.Outcome = "error"
		if res.Notice != "" {
			out.Error = res.Notice
		} else {
			out.Error = "update did not complete"
		}
	}
	return out
}

// drainStackEvents reads the CFN event stream to completion (the channel is
// closed by the streamer) and formats each line. A nil channel yields nil.
func drainStackEvents(ch <-chan cfn.StackEvent) []string {
	if ch == nil {
		return nil
	}
	var lines []string
	for ev := range ch {
		lines = append(lines, fmt.Sprintf("[cfn] %-22s %-28s %s",
			ev.ResourceStatus, ev.ResourceType, ev.LogicalResourceID))
	}
	return lines
}

// replacementReason summarises the resources a change set would replace, for
// the confirm modal.
func replacementReason(p update.ReplacementPayload) string {
	if p.Count == 0 {
		return ""
	}
	ids := make([]string, 0, len(p.Rows))
	for _, r := range p.Rows {
		ids = append(ids, r.LogicalID)
	}
	return fmt.Sprintf("%d resource(s) will be REPLACED (data loss / downtime risk): %s",
		p.Count, strings.Join(ids, ", "))
}

// diffPayload projects update.Diff into the JSON shape. It is always non-nil so
// the frontend can read the buckets unconditionally.
func diffPayload(d update.Diff) *DiffPayload {
	return &DiffPayload{
		Adds:      diffRows(d.Adds, "add"),
		Modifies:  diffRows(d.Modifies, "modify"),
		Replaces:  diffRows(d.Replaces, "replace"),
		Deletes:   diffRows(d.Deletes, "delete"),
		Params:    paramRows(d.ParameterDeltas),
		NoChanges: d.NoChanges,
	}
}

func diffRows(in []update.ResourceDelta, action string) []DiffRow {
	out := make([]DiffRow, 0, len(in))
	for _, r := range in {
		out = append(out, DiffRow{
			Action:       action,
			LogicalID:    r.LogicalID,
			ResourceType: r.ResourceType,
			Replacement:  r.Replacement,
			IAM:          r.IAM,
			Causes:       r.PropertyCauses,
		})
	}
	return out
}

func paramRows(in []update.ParameterDelta) []ParamRow {
	out := make([]ParamRow, 0, len(in))
	for _, p := range in {
		out = append(out, ParamRow{
			Key:               p.Key,
			Old:               p.Old,
			New:               p.New,
			CausedReplacement: p.CausedReplacement,
		})
	}
	return out
}

// resolveStackTemplatePath mirrors tui/update.go's resolveManifestTemplatePath:
// the template path is resolved against the manifest file's own directory.
func resolveStackTemplatePath(manifestSource string, mf *amanifest.Manifest) string {
	if mf.Template == nil || mf.Template.Path == "" {
		return ""
	}
	if filepath.IsAbs(mf.Template.Path) {
		return mf.Template.Path
	}
	return filepath.Join(filepath.Dir(manifestSource), mf.Template.Path)
}

// awsCoords prefers the stack record's recorded profile/region (the account the
// stack actually lives in) and falls back to the active config — mirroring
// tui/update.go's profileOf / regionOf.
func awsCoords(rec *record.StackRecord) (string, string) {
	profile, region := "", ""
	if rec != nil {
		profile, region = rec.Profile, rec.Region
	}
	if profile == "" || region == "" {
		if cfg, err := config.Load(); err == nil {
			if profile == "" {
				profile = cfg.Profile
			}
			if region == "" {
				region = cfg.Region
			}
		}
	}
	return profile, region
}

// stackScalingSpecs converts the manifest's scaling entries into runtime
// scaling.Spec values (mirrors tui/update.go's manifestScalingSpecs).
func stackScalingSpecs(in []amanifest.ScalingSpec) []scaling.Spec {
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

// stackErr wraps a Go error as a failed StackActionResult.
func stackErr(err error) StackActionResult {
	return StackActionResult{Outcome: "error", Error: err.Error()}
}
