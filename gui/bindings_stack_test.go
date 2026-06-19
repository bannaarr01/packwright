package gui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bannaarr01/packwright/internal/record"
	"github.com/bannaarr01/packwright/internal/update"
	amanifest "github.com/bannaarr01/packwright/manifest"
	"github.com/bannaarr01/packwright/render/cfn"
)

// stubStackContext points loadStackContext at a fixture for the duration of a
// test, so the stack bindings can be exercised without touching disk or AWS.
func stubStackContext(t *testing.T, rec *record.StackRecord, mf *amanifest.Manifest, body string) {
	t.Helper()
	prev := loadStackContext
	loadStackContext = func(_, _, _ string) (*record.StackRecord, *amanifest.Manifest, string, error) {
		return rec, mf, body, nil
	}
	t.Cleanup(func() { loadStackContext = prev })
}

// stubChangeSetAPI swaps the CFN client builder so tests can assert whether it
// is reached (and fail fast without an AWS account).
func stubChangeSetAPI(t *testing.T, fn func(ctx context.Context, profile, region string) (cfn.ChangeSetAPI, error)) {
	t.Helper()
	prev := stackChangeSetAPI
	stackChangeSetAPI = fn
	t.Cleanup(func() { stackChangeSetAPI = prev })
}

func scalingManifest() *amanifest.Manifest {
	one, ten := 1, 10
	return &amanifest.Manifest{
		Slash:    "/alb",
		Template: &amanifest.TemplateSpec{Path: "alb.yaml"},
		Scaling: []amanifest.ScalingSpec{{
			Param: "InstanceCount",
			Label: "Instances",
			Kind:  "integer",
			Min:   &one,
			Max:   &ten,
			EnvGuards: map[string]amanifest.ScalingEnvGuard{
				"prod": {RequireConfirmation: true},
			},
		}},
	}
}

func scalingRecord(env string) *record.StackRecord {
	return &record.StackRecord{
		StackName:  "alb-" + env,
		Manifest:   record.ManifestRef{Slash: "/alb", Source: "/tmp/packs/alb/manifests/alb.yaml"},
		Env:        env,
		Profile:    "default",
		Region:     "us-east-1",
		Parameters: record.Parameters{"InstanceCount": "2"},
	}
}

// TestScalingFormResolved proves a manifest with a scaling block yields a
// form pre-filled with the stack's current parameter values.
func TestScalingFormResolved(t *testing.T) {
	stubStackContext(t, scalingRecord("prod"), scalingManifest(), "template-body")

	got := newApp(nil).ScalingForm("acme", "prod", "alb-prod")
	if !got.Resolved {
		t.Fatalf("Resolved=false, err=%q", got.Error)
	}
	if len(got.Targets) != 1 {
		t.Fatalf("targets=%d, want 1", len(got.Targets))
	}
	if tg := got.Targets[0]; tg.Param != "InstanceCount" || tg.Current != "2" {
		t.Errorf("target=%+v, want Param=InstanceCount Current=2", tg)
	}
}

// TestScalingFormNoScaling proves a manifest without a scaling block declines
// to open a form (and explains why) rather than rendering an empty one.
func TestScalingFormNoScaling(t *testing.T) {
	mf := &amanifest.Manifest{Slash: "/x", Template: &amanifest.TemplateSpec{Path: "x.yaml"}}
	stubStackContext(t, scalingRecord("dev"), mf, "body")

	got := newApp(nil).ScalingForm("acme", "dev", "x-dev")
	if got.Resolved {
		t.Error("Resolved=true for a manifest with no scaling block")
	}
	if got.Error == "" {
		t.Error("Error empty; want a no-scaling-block message")
	}
}

// TestStackScaleEnvGuardNeedsConfirmBeforeAWS is the safety-critical case: a
// scale on an env whose guard sets require_confirmation must return
// needs_confirm WITHOUT making any AWS call when confirm=false.
func TestStackScaleEnvGuardNeedsConfirmBeforeAWS(t *testing.T) {
	stubStackContext(t, scalingRecord("prod"), scalingManifest(), "body")
	called := false
	stubChangeSetAPI(t, func(_ context.Context, _, _ string) (cfn.ChangeSetAPI, error) {
		called = true
		return nil, nil
	})

	got := newApp(nil).StackScale("acme", "prod", "alb-prod",
		map[string]string{"InstanceCount": "6"}, false)

	if got.Outcome != "needs_confirm" || !got.NeedsConfirm {
		t.Fatalf("Outcome=%q NeedsConfirm=%v, want needs_confirm", got.Outcome, got.NeedsConfirm)
	}
	if called {
		t.Error("stackChangeSetAPI was called; the env-guard gate must fire BEFORE any AWS call")
	}
	if got.ConfirmReason == "" {
		t.Error("ConfirmReason empty; want the env-guard reason")
	}
}

// TestStackScaleInvalidDelta proves a malformed scaling value surfaces as an
// error (shared with the CLI's BuildParams guard) rather than a silent no-op.
func TestStackScaleInvalidDelta(t *testing.T) {
	stubStackContext(t, scalingRecord("dev"), scalingManifest(), "body")

	got := newApp(nil).StackScale("acme", "dev", "alb-dev",
		map[string]string{"InstanceCount": "not-an-int"}, false)
	if got.Outcome != "error" || got.Error == "" {
		t.Errorf("Outcome=%q Error=%q, want an error", got.Outcome, got.Error)
	}
}

// TestStackUpdateReachesCoordinator proves StackUpdate wires through to the
// change-set client builder (surfacing its error) — i.e. the binding is not a
// stub.
func TestStackUpdateReachesCoordinator(t *testing.T) {
	stubStackContext(t, scalingRecord("dev"), scalingManifest(), "body")
	stubChangeSetAPI(t, func(_ context.Context, _, _ string) (cfn.ChangeSetAPI, error) {
		return nil, fmt.Errorf("boom-no-aws")
	})

	got := newApp(nil).StackUpdate("acme", "dev", "alb-dev", false)
	if got.Outcome != "error" || !strings.Contains(got.Error, "boom-no-aws") {
		t.Errorf("Outcome=%q Error=%q, want an error mentioning boom-no-aws", got.Outcome, got.Error)
	}
}

// TestStackDeleteIsNotice proves delete is a notice, not a destructive action.
func TestStackDeleteIsNotice(t *testing.T) {
	got := newApp(nil).StackDelete("acme", "dev", "alb-dev")
	if got.Outcome != "notice" {
		t.Errorf("Outcome=%q, want notice", got.Outcome)
	}
	if got.OK {
		t.Error("OK=true for a delete notice; the GUI performs no deletion")
	}
	if got.Notice == "" {
		t.Error("Notice empty")
	}
}

// TestResultFromStackMapping exercises the pure result mapper across every
// outcome, including the consent-denied → needs_confirm projection that drives
// the GUI's replacement-confirm modal.
func TestResultFromStackMapping(t *testing.T) {
	if r := resultFromStack(update.StackResult{Outcome: update.OutcomeExecuted, Notice: "done"}, nil); !r.OK || r.Outcome != "executed" {
		t.Errorf("executed → %+v", r)
	}
	if r := resultFromStack(update.StackResult{Outcome: update.OutcomeNoChanges, Notice: "none"}, nil); !r.OK || r.Outcome != "no_changes" {
		t.Errorf("no_changes → %+v", r)
	}
	denied := resultFromStack(update.StackResult{
		Outcome:     update.OutcomeConsentDenied,
		Replacement: update.ReplacementPayload{Count: 1, Rows: []update.ReplacementRow{{LogicalID: "DB"}}},
	}, nil)
	if !denied.NeedsConfirm || denied.Outcome != "needs_confirm" {
		t.Errorf("consent_denied → %+v, want needs_confirm", denied)
	}
	if !strings.Contains(denied.ConfirmReason, "DB") {
		t.Errorf("ConfirmReason=%q, want it to name the replaced resource", denied.ConfirmReason)
	}
	if r := resultFromStack(update.StackResult{}, fmt.Errorf("kaboom")); r.Outcome != "error" || !strings.Contains(r.Error, "kaboom") {
		t.Errorf("error → %+v", r)
	}
}
