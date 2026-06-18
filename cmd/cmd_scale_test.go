package cmd

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/awsx"
	"github.com/bannaarr01/packwright/internal/record"
	"github.com/bannaarr01/packwright/internal/scaling"
	"github.com/bannaarr01/packwright/internal/update"
	"github.com/bannaarr01/packwright/render/cfn"
)

// scaleEnv captures the per-test stub set for /scale's package-level seams.
// stubScaleEnv assigns reasonable test defaults and restores the originals
// from t.Cleanup so mutation never leaks across cases.
type scaleEnv struct {
	logBuf      *bytes.Buffer
	store       *record.Store
	storeRoot   string
	manifestDir string
	stackedOpts struct {
		called bool
		in     update.StackInput
		opts   update.StackOptions
	}
}

func stubScaleEnv(t *testing.T) *scaleEnv {
	t.Helper()

	origCollect := scaleCollectInput
	origConsent := scaleConsentGate
	origStack := scaleUpdateStack
	origNewStore := scaleNewStore
	origReadTemplate := scaleReadTemplate
	origLogger := scaleLogger
	origNewAWS := newAWSClientForUpdate
	origCSAPI := changeSetAPIFromClient
	origOpts := scaleOpts
	t.Cleanup(func() {
		scaleCollectInput = origCollect
		scaleConsentGate = origConsent
		scaleUpdateStack = origStack
		scaleNewStore = origNewStore
		scaleReadTemplate = origReadTemplate
		scaleLogger = origLogger
		newAWSClientForUpdate = origNewAWS
		changeSetAPIFromClient = origCSAPI
		scaleOpts = origOpts
	})

	env := &scaleEnv{}
	env.storeRoot = t.TempDir()
	env.manifestDir = t.TempDir()
	env.store = record.NewStore(env.storeRoot)

	env.logBuf = &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(env.logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	scaleLogger = func() *slog.Logger { return logger }

	scaleNewStore = func() (*record.Store, error) { return env.store, nil }

	// Default collector / consent / stack stubs that fail loudly until a
	// test overrides them — keeps "the test forgot to wire X" out of the
	// noise.
	scaleCollectInput = func(context.Context, scaling.Form) (map[string]string, error) {
		return nil, errors.New("test collector not configured")
	}
	scaleConsentGate = func(context.Context, ScaleConsentPayload) ScaleConsentDecision {
		t.Errorf("scaleConsentGate invoked unexpectedly")
		return ScaleConsentDeny
	}
	scaleUpdateStack = func(_ context.Context, in update.StackInput, opts update.StackOptions) (update.StackResult, error) {
		env.stackedOpts.called = true
		env.stackedOpts.in = in
		env.stackedOpts.opts = opts
		return update.StackResult{Outcome: update.OutcomeExecuted, Notice: "executed (test stub)"}, nil
	}
	scaleReadTemplate = func(string) ([]byte, error) {
		return []byte("template-body-stub"), nil
	}

	// AWS client / CFN API construction never runs in unit tests; the
	// stubbed scaleUpdateStack ignores the client and api args. The
	// helpers stay assigned to "return a sentinel non-nil value with no
	// error" so runScale's call chain compiles and stays unsurprising.
	newAWSClientForUpdate = func(context.Context, string, string) (*awsx.Client, error) {
		return &awsx.Client{}, nil
	}
	changeSetAPIFromClient = func(context.Context, *awsx.Client) (cfn.ChangeSetAPI, error) {
		return nil, nil
	}

	// Drop any leftover scaleOpts state from a previous test invocation.
	scaleOpts = scaleFlags{}

	return env
}

// writeScalingManifest writes a manifest YAML with a DesiredCount field +
// scaling block (env_guards.prd.max=20 require_confirmation=true) and an
// adjacent template file. Used by the integration-style tests below to
// exercise the load → build → handoff path against the real manifest
// loader.
func writeScalingManifest(t *testing.T, dir string) string {
	t.Helper()
	templatePath := filepath.Join(dir, "alb.cfn.yaml")
	if err := os.WriteFile(templatePath, []byte("AWSTemplateFormatVersion: '2010-09-09'\nResources: {}\n"), 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}
	path := filepath.Join(dir, "alb.yaml")
	body := `
schema_version: packwright.manifest.v1
kind: resource
slash: /alb
title: ALB
template:
  kind: cloudformation
  path: ./alb.cfn.yaml
deploy:
  driver: script
  script: ./d.sh
form:
  - id: Project
    label: Project
    type: string
  - id: DesiredCount
    label: Tasks
    type: int
  - id: TaskCpu
    label: CPU
    type: enum
    values: ["256","512","1024"]
scaling:
  - param: DesiredCount
    label: Desired tasks
    kind: integer
    min: 1
    max: 50
    env_guards:
      prd: { min: 2, max: 20, require_confirmation: true }
      dev: { min: 0, max: 50 }
  - param: TaskCpu
    label: Task CPU
    kind: enum
    values: ["256","512","1024"]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

// seedRecord writes a record.StackRecord into the test store so /scale
// finds something to scale. Returns the written path.
func seedRecord(t *testing.T, env *scaleEnv, stackName, project, envName, manifestPath string, params record.Parameters) {
	t.Helper()
	rec := &record.StackRecord{
		StackName:  stackName,
		Project:    project,
		Env:        envName,
		Profile:    "test",
		Region:     "eu-west-1",
		Account:    "111111111111",
		Manifest:   record.ManifestRef{Slash: "/alb", Source: manifestPath},
		Parameters: params,
		Status:     record.Status{CFN: "UPDATE_COMPLETE", Broad: record.BroadDeployed, ReconciledAt: time.Now().UTC()},
		History:    []record.HistoryEntry{{At: time.Now().UTC(), Kind: record.KindCreate, Result: record.ResultSuccess}},
	}
	if err := env.store.Write(rec); err != nil {
		t.Fatalf("seed record: %v", err)
	}
}

// runScaleArgs invokes scaleCmd's RunE with the given args. Returns the
// command's stdout (combined with stderr through SetErr) and any error.
func runScaleArgs(t *testing.T, args []string) (string, error) {
	t.Helper()
	cmd := newRootCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs(append([]string{"scale"}, args...))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func TestScaleCmdRegistersAsRootSubcommand(t *testing.T) {
	found := false
	for _, sub := range rootSubcommands {
		if sub.Use == scaleCmd.Use {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("scaleCmd missing from rootSubcommands; init() must call registerSubcommand")
	}
}

func TestRunScaleHappyPathHandsOffToUpdateStack(t *testing.T) {
	env := stubScaleEnv(t)
	manifestPath := writeScalingManifest(t, env.manifestDir)
	seedRecord(t, env, "alb-dev-stack", "", "dev", manifestPath, record.Parameters{
		"Project":      "acme",
		"DesiredCount": "2",
		"TaskCpu":      "256",
	})

	var sawForm scaling.Form
	scaleCollectInput = func(_ context.Context, form scaling.Form) (map[string]string, error) {
		sawForm = form
		return map[string]string{"DesiredCount": "5", "TaskCpu": "512"}, nil
	}

	out, err := runScaleArgs(t, []string{"alb-dev-stack", "--yes"})
	if err != nil {
		t.Fatalf("runScale err = %v, out = %q", err, out)
	}

	// Form: only scaling targets, pre-filled with current.
	if sawForm.StackName != "alb-dev-stack" || sawForm.Env != "dev" {
		t.Errorf("form headers = %+v, want stack=alb-dev-stack env=dev", sawForm)
	}
	if got, want := len(sawForm.Targets), 2; got != want {
		t.Fatalf("len(Targets) = %d, want %d (only scaling fields rendered)", got, want)
	}
	if sawForm.Targets[0].Spec.Param != "DesiredCount" || sawForm.Targets[0].Current != "2" {
		t.Errorf("Targets[0] = %+v, want DesiredCount pre-filled with 2", sawForm.Targets[0])
	}

	// Handoff: update.Stack received the merged params and template body.
	if !env.stackedOpts.called {
		t.Fatal("scaleUpdateStack was not called")
	}
	in := env.stackedOpts.in
	if in.StackName != "alb-dev-stack" {
		t.Errorf("StackInput.StackName = %q, want alb-dev-stack", in.StackName)
	}
	if in.Parameters["DesiredCount"] != "5" || in.Parameters["TaskCpu"] != "512" {
		t.Errorf("StackInput.Parameters = %v, want deltas applied", in.Parameters)
	}
	if in.Parameters["Project"] != "acme" {
		t.Errorf("StackInput.Parameters lost non-scaling current value: %v", in.Parameters)
	}
	if in.PreviousParameters["DesiredCount"] != "2" {
		t.Errorf("StackInput.PreviousParameters = %v, want record snapshot", in.PreviousParameters)
	}
	if !strings.Contains(in.Description, "scale on dev env") {
		t.Errorf("StackInput.Description = %q, want it to mention scale on dev env", in.Description)
	}
	if env.stackedOpts.opts.Harvest == nil {
		t.Error("StackOptions.Harvest = nil; want a scale harvester wired in")
	}
}

func TestRunScalePrdRequiresConsentBeforeUpdateStack(t *testing.T) {
	// DoD: an env_guard prd with require_confirmation: true must surface
	// the ADR-0036 consent reason and gate execution. The gate is
	// independent of update.Stack's replacement consent — it fires even
	// without replacements.
	env := stubScaleEnv(t)
	manifestPath := writeScalingManifest(t, env.manifestDir)
	seedRecord(t, env, "alb-prd-stack", "", "prd", manifestPath, record.Parameters{
		"DesiredCount": "5",
	})

	scaleCollectInput = func(context.Context, scaling.Form) (map[string]string, error) {
		return map[string]string{"DesiredCount": "10"}, nil
	}
	var sawConsent ScaleConsentPayload
	scaleConsentGate = func(_ context.Context, p ScaleConsentPayload) ScaleConsentDecision {
		sawConsent = p
		return ScaleConsentApprove
	}

	if _, err := runScaleArgs(t, []string{"alb-prd-stack"}); err != nil {
		t.Fatalf("runScale err = %v", err)
	}

	if sawConsent.Reason != "scale on prd env" {
		t.Errorf("ScaleConsentPayload.Reason = %q, want %q", sawConsent.Reason, "scale on prd env")
	}
	if sawConsent.StackName != "alb-prd-stack" || sawConsent.Env != "prd" {
		t.Errorf("ScaleConsentPayload headers = %+v", sawConsent)
	}
	if !env.stackedOpts.called {
		t.Error("update.Stack should have been called after consent approval")
	}
}

func TestRunScalePrdConsentDeniedAbortsBeforeUpdateStack(t *testing.T) {
	env := stubScaleEnv(t)
	manifestPath := writeScalingManifest(t, env.manifestDir)
	seedRecord(t, env, "alb-prd-stack", "", "prd", manifestPath, record.Parameters{
		"DesiredCount": "5",
	})

	scaleCollectInput = func(context.Context, scaling.Form) (map[string]string, error) {
		return map[string]string{"DesiredCount": "10"}, nil
	}
	scaleConsentGate = func(context.Context, ScaleConsentPayload) ScaleConsentDecision {
		return ScaleConsentDeny
	}

	out, err := runScaleArgs(t, []string{"alb-prd-stack"})
	if err != nil {
		t.Fatalf("runScale err = %v", err)
	}
	if env.stackedOpts.called {
		t.Error("update.Stack must not be called after consent denial")
	}
	if !strings.Contains(out, "consent denied") {
		t.Errorf("out = %q, want it to mention consent denial", out)
	}
}

func TestRunScalePrdMaxClampLogsExplicitly(t *testing.T) {
	// DoD: a scaling clamp at prd.max=20 must be logged when the user
	// attempts to exceed it (clamp + log; not silent acceptance).
	env := stubScaleEnv(t)
	manifestPath := writeScalingManifest(t, env.manifestDir)
	seedRecord(t, env, "alb-prd-stack", "", "prd", manifestPath, record.Parameters{
		"DesiredCount": "5",
	})

	scaleCollectInput = func(context.Context, scaling.Form) (map[string]string, error) {
		return map[string]string{"DesiredCount": "30"}, nil
	}
	scaleConsentGate = func(context.Context, ScaleConsentPayload) ScaleConsentDecision {
		return ScaleConsentApprove
	}

	out, err := runScaleArgs(t, []string{"alb-prd-stack"})
	if err != nil {
		t.Fatalf("runScale err = %v", err)
	}
	if env.stackedOpts.in.Parameters["DesiredCount"] != "20" {
		t.Errorf("StackInput.Parameters[DesiredCount] = %q, want clamped to 20",
			env.stackedOpts.in.Parameters["DesiredCount"])
	}
	logged := env.logBuf.String()
	if !strings.Contains(logged, "scaling clamp") {
		t.Errorf("log buffer = %q, want a 'scaling clamp' warn line", logged)
	}
	for _, want := range []string{"param=DesiredCount", "env=prd", "requested=30", "effective=20", "bound=max", "limit=20"} {
		if !strings.Contains(logged, want) {
			t.Errorf("log buffer missing %q\nfull log: %s", want, logged)
		}
	}
	if !strings.Contains(out, "Clamped DesiredCount on prd env") {
		t.Errorf("stdout = %q, want a 'Clamped' line for the user", out)
	}
}

func TestRunScaleRejectsManifestWithoutScalingBlock(t *testing.T) {
	env := stubScaleEnv(t)
	templatePath := filepath.Join(env.manifestDir, "x.cfn.yaml")
	_ = os.WriteFile(templatePath, []byte("dummy"), 0o600)
	manifestPath := filepath.Join(env.manifestDir, "noscaling.yaml")
	body := `
schema_version: packwright.manifest.v1
kind: resource
slash: /x
title: X
template:
  kind: cloudformation
  path: ./x.cfn.yaml
deploy:
  driver: script
  script: ./d.sh
form:
  - id: A
    type: string
`
	if err := os.WriteFile(manifestPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	seedRecord(t, env, "s", "", "dev", manifestPath, record.Parameters{})

	_, err := runScaleArgs(t, []string{"s"})
	if err == nil {
		t.Fatal("runScale err = nil, want 'no scaling block' error")
	}
	if !strings.Contains(err.Error(), "no scaling block") {
		t.Errorf("err = %v, want it to explain the manifest declares no scaling block", err)
	}
}

func TestRunScaleMissingRecordIsExplainedClearly(t *testing.T) {
	stubScaleEnv(t)
	_, err := runScaleArgs(t, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "no stack record found") {
		t.Fatalf("err = %v, want it to mention missing record", err)
	}
}

func TestRunScaleEmptyDeltasIsBenign(t *testing.T) {
	env := stubScaleEnv(t)
	manifestPath := writeScalingManifest(t, env.manifestDir)
	seedRecord(t, env, "s", "", "dev", manifestPath, record.Parameters{})

	scaleCollectInput = func(context.Context, scaling.Form) (map[string]string, error) {
		return map[string]string{}, nil
	}

	out, err := runScaleArgs(t, []string{"s"})
	if err != nil {
		t.Fatalf("runScale err = %v, want nil", err)
	}
	if env.stackedOpts.called {
		t.Error("update.Stack invoked with no deltas; want short-circuit to no-op")
	}
	if !strings.Contains(out, "No scaling changes submitted") {
		t.Errorf("out = %q, want it to explain no submission was made", out)
	}
}

func TestParseScaleParamsRejectsNonScalingField(t *testing.T) {
	form := scaling.Form{Targets: []scaling.Target{
		{Spec: scaling.Spec{Param: "DesiredCount", Kind: scaling.KindInteger}},
	}}
	_, err := parseScaleParams([]string{"VpcId=vpc-x"}, form)
	if err == nil || !strings.Contains(err.Error(), "not a scaling-eligible field") {
		t.Fatalf("err = %v, want rejection of non-scaling field", err)
	}
}

func TestParseScaleParamsRejectsMalformedFlag(t *testing.T) {
	form := scaling.Form{Targets: []scaling.Target{
		{Spec: scaling.Spec{Param: "DesiredCount", Kind: scaling.KindInteger}},
	}}
	_, err := parseScaleParams([]string{"no-equals-sign"}, form)
	if err == nil || !strings.Contains(err.Error(), "expected key=value") {
		t.Fatalf("err = %v, want key=value rejection", err)
	}
}

func TestScaleHarvesterAppendsKindScaleHistoryEntry(t *testing.T) {
	store := record.NewStore(t.TempDir())
	rec := &record.StackRecord{
		StackName:  "alb-dev-stack",
		Project:    "acme",
		Env:        "dev",
		Manifest:   record.ManifestRef{Slash: "/alb", Source: "/dev/null"},
		Parameters: record.Parameters{"DesiredCount": "2"},
		History:    []record.HistoryEntry{{At: time.Now().UTC(), Kind: record.KindCreate, Result: record.ResultSuccess}},
	}
	if err := store.Write(rec); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	harvester := scaleHarvester(store, "acme", "dev")
	err := harvester(context.Background(), update.HarvestInfo{
		StackName:     "alb-dev-stack",
		ChangeSetID:   "arn:cs",
		ChangeSetName: "packwright-test",
		HistoryKind:   update.HistoryKind,
		ParametersSent: map[string]string{
			"DesiredCount": "5",
		},
		Diff: update.Diff{ParameterDeltas: []update.ParameterDelta{
			{Key: "DesiredCount", Old: "2", New: "5"},
		}},
	})
	if err != nil {
		t.Fatalf("harvester err = %v", err)
	}

	written, err := store.Read("acme", "dev", "alb-dev-stack")
	if err != nil {
		t.Fatalf("read post-harvest: %v", err)
	}
	if written.Parameters["DesiredCount"] != "5" {
		t.Errorf("Parameters[DesiredCount] = %q, want %q (post-clamp value)", written.Parameters["DesiredCount"], "5")
	}
	if got, want := len(written.History), 2; got != want {
		t.Fatalf("len(History) = %d, want %d (one prior + one scale)", got, want)
	}
	last := written.History[len(written.History)-1]
	if last.Kind != record.KindScale {
		t.Errorf("last history entry Kind = %q, want %q", last.Kind, record.KindScale)
	}
	if last.ChangesetID != "arn:cs" {
		t.Errorf("last history entry ChangesetID = %q, want arn:cs", last.ChangesetID)
	}
	if !strings.Contains(last.ParametersDiff, "DesiredCount: 2 → 5") {
		t.Errorf("ParametersDiff = %q, want it to render the delta", last.ParametersDiff)
	}
}

func TestSetScaleInputCollectorIgnoresNil(t *testing.T) {
	orig := scaleCollectInput
	t.Cleanup(func() { scaleCollectInput = orig })

	custom := func(context.Context, scaling.Form) (map[string]string, error) {
		return map[string]string{"marker": "1"}, nil
	}
	SetScaleInputCollector(custom)
	SetScaleInputCollector(nil)
	got, _ := scaleCollectInput(context.Background(), scaling.Form{})
	if got["marker"] != "1" {
		t.Error("SetScaleInputCollector(nil) overwrote prior collector; want nil-safe")
	}
}

func TestSetScaleConsentGateIgnoresNil(t *testing.T) {
	orig := scaleConsentGate
	t.Cleanup(func() { scaleConsentGate = orig })

	custom := func(context.Context, ScaleConsentPayload) ScaleConsentDecision {
		return ScaleConsentApprove
	}
	SetScaleConsentGate(custom)
	SetScaleConsentGate(nil)
	if scaleConsentGate(context.Background(), ScaleConsentPayload{}) != ScaleConsentApprove {
		t.Error("SetScaleConsentGate(nil) overwrote prior gate; want nil-safe")
	}
}
