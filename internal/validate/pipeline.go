// Package validate implements Packwright's pre-deploy template validators
// (ADR-0050). Two stages run behind a single Pipeline: a local YAML lint
// (Stage 1 — strict unmarshal of the CFN template, duplicate-key and
// tab/space-mix detection with exact line:column) and a CloudFormation
// validate-template round-trip (Stage 2 — schema checks plus capability /
// parameter surfacing for downstream form flows).
//
// The engine (action/resource.Execute) calls Pipeline.Run between resolveEnv
// and Renderer.Render; an error-severity Finding short-circuits the deploy
// with a typed ValidationFailure that the existing error model (ADR-0016)
// renders as a card.
package validate

import (
	"context"
	"fmt"
	"strings"

	pkgerrors "github.com/bannaarr01/packwright/internal/errors"
)

// Stage identifies which validator produced a Finding. The string values are
// stable — they appear in catalogue regexes and in the error-card heading.
const (
	StageYAML = "yaml"
	StageCFN  = "cfn"
)

// Severity classifies a Finding's blocking behaviour. SeverityError stops the
// deploy; SeverityWarning and SeverityInfo render in the UI but never block.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
	SeverityInfo    = "info"
)

// Finding is one observation from a single validator. Stage + Severity drive
// rendering; Path/Line/Col let the front-end deep-link to the offending
// position in the template; Reason is the human-readable summary that flows
// into the error model.
type Finding struct {
	Stage    string
	Severity string
	Path     string
	Line     int
	Col      int
	Reason   string
}

// Input is what callers hand to Pipeline.Run. TemplatePath is required;
// ParametersPath is opportunistic — Stage 1 lints it only when the file
// exists (the engine generates parameters.json after validators run, so on
// a fresh deploy the path resolves but the file is absent; on a re-run it
// gets linted too).
type Input struct {
	TemplatePath   string
	ParametersPath string
	StackName      string
	AWS            CloudFormationAPI
}

// Pipeline is the validator contract resource.Execute depends on. The
// default implementation composes the YAML stage and the CFN stage; tests
// inject their own (or skip validation entirely with WithValidators(false)).
type Pipeline interface {
	Run(ctx context.Context, in Input) ([]Finding, error)
}

// Default is the canonical pipeline: Stage 1 (YAML lint) followed by Stage 2
// (cloudformation ValidateTemplate). Stages run sequentially; Stage 1 errors
// short-circuit Stage 2 because a syntactically-broken template would only
// confuse ValidateTemplate.
type Default struct{}

// NewDefault is sugar for callers that prefer constructor-style wiring; the
// zero value of Default works just as well.
func NewDefault() Pipeline { return Default{} }

// Run executes the two stages and returns every Finding produced (errors,
// warnings, infos). A non-nil error is reserved for infrastructure failures
// (e.g. file-not-found on the template path) — catalogue-mapped CFN errors
// surface as error-severity Findings, not as a Go error, so callers see one
// uniform error-model render path.
func (Default) Run(ctx context.Context, in Input) ([]Finding, error) {
	var findings []Finding

	yamlFindings, err := runYAMLStage(in)
	if err != nil {
		return nil, fmt.Errorf("validate: yaml stage: %w", err)
	}
	findings = append(findings, yamlFindings...)
	if HasErrors(yamlFindings) {
		return findings, nil
	}

	cfnFindings, err := runCFNStage(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("validate: cfn stage: %w", err)
	}
	findings = append(findings, cfnFindings...)
	return findings, nil
}

// HasErrors reports whether any finding is error-severity. The engine uses
// this to decide whether to short-circuit the deploy.
func HasErrors(findings []Finding) bool {
	for _, f := range findings {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}

// FirstError returns the first error-severity finding, or nil if none.
// Convenience used by ValidationFailure to populate AppError.
func FirstError(findings []Finding) *Finding {
	for i := range findings {
		if findings[i].Severity == SeverityError {
			return &findings[i]
		}
	}
	return nil
}

// ValidationFailure is the typed error resource.Execute returns when a
// validator produces an error-severity finding. It implements error so the
// dispatcher can return it through the standard error channel, and pre-
// resolves the leading finding into an *errors.AppError so the TUI/GUI
// render path is identical to a real CFN deploy failure.
//
// Findings carries every finding the pipeline produced (errors, warnings,
// infos) so a surface can show capability hints alongside the blocking
// error.
type ValidationFailure struct {
	Findings []Finding
	AppError *pkgerrors.AppError
}

// Error renders a short string for log output. The structured surface is
// AppError; this method is what shows up in plain log lines, slog values, and
// %v formatting.
func (v *ValidationFailure) Error() string {
	if v == nil {
		return "validate: nil ValidationFailure"
	}
	first := FirstError(v.Findings)
	if first == nil {
		return "validate: pipeline produced no error-severity findings"
	}
	switch {
	case first.Path != "" && first.Line > 0:
		return fmt.Sprintf("validate: %s:%d:%d: %s", first.Path, first.Line, first.Col, first.Reason)
	case first.Path != "":
		return fmt.Sprintf("validate: %s: %s", first.Path, first.Reason)
	default:
		return "validate: " + first.Reason
	}
}

// FailureFromFindings builds a ValidationFailure from a slice of findings,
// resolving the first error-severity finding through the error catalogue so
// callers do not have to repeat that wiring at every surface.
//
// The matcher Context carries the stack name and the input data — the same
// inputs the deploy-time matcher consumes — so catalogue templates that
// reference {{ .StackName }} or form values render the same way for a
// pre-deploy validation error as they do for a post-deploy CFN failure.
//
// Returns nil when findings contains no error-severity entry.
func FailureFromFindings(findings []Finding, stackName string, inputs map[string]any) *ValidationFailure {
	first := FirstError(findings)
	if first == nil {
		return nil
	}
	app := pkgerrors.MatchString(first.Reason, pkgerrors.Context{
		AWSService: "CloudFormation",
		AWSCode:    "ValidationError",
		StackName:  stackName,
		Inputs:     inputs,
	})
	// Even when no catalogue entry matches, MatchString returns a populated
	// fallback AppError. Promote the validator's own data into the fields
	// the renderer relies on (Title / Cause) so the card never shows a bare
	// raw string.
	if app.Title == "" {
		app.Title = humanTitle(first.Stage)
	}
	if app.Cause == "" {
		app.Cause = first.Reason
	}
	app.Raw = first.Reason
	return &ValidationFailure{Findings: findings, AppError: app}
}

func humanTitle(stage string) string {
	switch stage {
	case StageYAML:
		return "Template YAML lint failed"
	case StageCFN:
		return "CloudFormation validate-template failed"
	default:
		s := strings.TrimSpace(stage)
		if s == "" {
			return "Template validation failed"
		}
		return "Template validation failed (" + s + ")"
	}
}
