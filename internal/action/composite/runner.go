// Package composite is the headless engine for kind: composite manifests.
// It walks the manifest's steps in declared order; each run-step resolves
// to an existing slash command's manifest, has its inputs supplied from
// prompt / previous / static (see [Step.InputsFrom]), and is invoked
// through the shared dispatcher. A confirm pseudo-step publishes a
// [ConfirmRequiredEvent] on the stream EventBus and blocks until the
// caller-supplied Await callback returns.
//
// The package deliberately does not import action/dispatch — that would
// create an import cycle once a future PR wires composite back into the
// dispatcher. Instead the two functions composite needs from the
// dispatcher (slash-to-manifest lookup and "invoke this manifest") are
// supplied as fields on [Spec]; production wiring closes over
// dispatch.Dispatch and the manifest registry, tests pass stubs.
//
// Per ADR-0024 v1 the runner is sequential; on_failure: continue keeps
// the chain going on a failed step, but parallel fan-out is explicitly
// out of scope.
package composite

import (
	"context"
	"errors"
	"fmt"

	"github.com/bannaarr01/packwright/action"
	"github.com/bannaarr01/packwright/internal/stream"
	"github.com/bannaarr01/packwright/manifest"
)

// InputsFrom names where a step's inputs are sourced from. The empty
// value means [InputsFromPrompt] so a manifest may omit the field for
// the common case.
type InputsFrom string

// Recognised inputs_from values, per ADR-0024.
const (
	// InputsFromPrompt opens the step's normal form for the user. In
	// headless callers (tests, batch scripts) the form is short-circuited
	// by Spec.Prompt; if Spec.Prompt is nil the composite Run's top-level
	// inputs are reused as a single shared bag.
	InputsFromPrompt InputsFrom = "prompt"
	// InputsFromPrevious carries forward the previous run-step's
	// submitted inputs, filtered to keys whose names match a Field.ID on
	// the target manifest.
	InputsFromPrevious InputsFrom = "previous"
	// InputsFromStatic uses Step.Inputs verbatim.
	InputsFromStatic InputsFrom = "static"
)

// OnFailure is the per-step policy applied when the invocation returns a
// non-nil error. The empty value is [OnFailureAbort].
type OnFailure string

// Recognised on_failure values.
const (
	// OnFailureAbort stops the chain. Subsequent steps are skipped.
	OnFailureAbort OnFailure = "abort"
	// OnFailureContinue runs the next step regardless. Used by
	// cleanup-style chains.
	OnFailureContinue OnFailure = "continue"
)

// Step is one entry in a composite's steps list. Exactly one of Run or
// Confirm must be set; the runner rejects steps that set both or neither.
//
// Run is the slash command of the target manifest (qualified or
// unqualified — the Lookup callback decides). Confirm is a literal
// message rendered as a one-line modal pause.
type Step struct {
	// Run is the slash command of the target manifest. Empty when this
	// is a confirm pseudo-step.
	Run string
	// Confirm is the modal message. Empty when this is a run step.
	Confirm string
	// InputsFrom selects the input source. Ignored when Confirm is set.
	// The empty value is treated as InputsFromPrompt.
	InputsFrom InputsFrom
	// Inputs is the literal input map used when InputsFrom is
	// InputsFromStatic. Ignored otherwise.
	Inputs action.Inputs
	// OnFailure is the per-step failure policy. The empty value is
	// treated as OnFailureAbort.
	OnFailure OnFailure
}

// isConfirm reports whether the step is a confirm pseudo-step.
func (s Step) isConfirm() bool { return s.Confirm != "" }

// inputsFrom returns the effective InputsFrom for the step, applying the
// default when the caller left the field empty.
func (s Step) inputsFrom() InputsFrom {
	if s.InputsFrom == "" {
		return InputsFromPrompt
	}
	return s.InputsFrom
}

// onFailure returns the effective OnFailure for the step.
func (s Step) onFailure() OnFailure {
	if s.OnFailure == "" {
		return OnFailureAbort
	}
	return s.OnFailure
}

// Spec is the composite runner's configuration. It is attached to a
// [Runner] instance by the caller (tests construct one inline; the
// manifest-loading PR will populate it from YAML).
type Spec struct {
	// Steps is the ordered list of steps to execute.
	Steps []Step

	// Lookup resolves a step's Run slash to its target manifest. The
	// composite runner does not invent its own registry; this delegation
	// keeps the package free of a dependency on the dispatcher and the
	// manifest store. Required when any step has Run set.
	Lookup func(slash string) (*manifest.Manifest, error)

	// Invoke runs the resolved target manifest. Production wiring passes
	// action/dispatch.Dispatch; tests supply a stub that returns canned
	// results. Required when any step has Run set.
	Invoke func(ctx context.Context, m *manifest.Manifest, in action.Inputs) (action.Result, error)

	// Prompt resolves a InputsFromPrompt step's inputs. The front-end
	// blocks here to render the target's form; in tests it returns
	// canned values. When nil, prompt-steps reuse the top-level inputs
	// passed to [Runner.Run].
	Prompt func(ctx context.Context, step int, target *manifest.Manifest) (action.Inputs, error)

	// Await is called for a confirm pseudo-step after the
	// ConfirmRequiredEvent has been published. It returns true to
	// continue and false to abort the chain. Required when any step is
	// a confirm pseudo-step.
	Await func(ctx context.Context, step int, message string) (bool, error)

	// Bus is the stream EventBus on which a ConfirmRequiredEvent is
	// published before each confirm step. Optional; nil disables event
	// publication (Await still gates progress).
	Bus *stream.EventBus

	// RequestID is the bus key under which ConfirmRequiredEvent is
	// published. Ignored when Bus is nil.
	RequestID string
}

// Result is the kind-specific value placed in action.Result.Value on a
// successful Run. Steps carries one entry per executed step (skipped
// steps after an abort are omitted); Aborted records the index of the
// step that triggered an abort, or -1 when the chain ran to completion.
type Result struct {
	// Steps records the per-step outcome in execution order.
	Steps []StepResult
	// Aborted is the index of the step that aborted the chain, or -1.
	// A step that fails with on_failure: continue does not abort and is
	// captured in Steps[i].Err only.
	Aborted int
}

// StepResult is the outcome of a single step.
type StepResult struct {
	// Index is the position of this step in Spec.Steps.
	Index int
	// Slash is the target slash command for run-steps; empty for
	// confirm pseudo-steps.
	Slash string
	// Confirm is the message of a confirm pseudo-step; empty for
	// run-steps.
	Confirm string
	// Accepted records whether a confirm step was accepted. Always
	// false for run-steps.
	Accepted bool
	// Inputs is the input map that was submitted for the step.
	Inputs action.Inputs
	// Value is the inner runner's Result.Value (e.g. *shell.Result).
	Value any
	// Err is the inner runner's error, if any. A confirm pseudo-step
	// that was rejected carries [ErrConfirmRejected] here.
	Err error
}

// ConfirmRequiredEvent is published on the stream EventBus before a
// confirm pseudo-step blocks. It satisfies the stream.Event interface
// without modifying the stream package; once a future PR formalises the
// event in stream, this type becomes a thin alias.
type ConfirmRequiredEvent struct {
	// Step is the index of the confirm pseudo-step in Spec.Steps.
	Step int
	// Message is the literal text from the manifest's confirm: entry.
	Message string
}

// EventKind implements stream.Event.
func (ConfirmRequiredEvent) EventKind() string { return "confirm_required" }

// ErrSpecMissing is returned by [Runner.Run] when invoked without an
// attached Spec. The init()-registered global instance has none; tests
// and the manifest-loading PR construct &Runner{Spec: ...} explicitly.
var ErrSpecMissing = errors.New("composite: spec not configured")

// ErrConfirmRejected is the sentinel surfaced when a confirm step's
// Await callback returns false. Callers can branch with errors.Is to
// distinguish a user rejection from an underlying runner error.
var ErrConfirmRejected = errors.New("composite: confirm rejected")

// Runner is the action.Runner for kind: composite. The zero value
// answers Kind and Validate correctly; Run returns ErrSpecMissing until
// a Spec is attached.
type Runner struct {
	// Spec is the composite configuration. Nil for the package-level
	// instance registered in init(); set by tests and the manifest-
	// loading PR.
	Spec *Spec
}

// Kind reports manifest.KindComposite.
func (*Runner) Kind() manifest.Kind { return manifest.KindComposite }

// Validate enforces the manifest-structure invariants (manifest non-nil,
// kind matches) and, when a Spec is attached, the per-Spec invariants
// (every step is well-formed and the callbacks needed by the declared
// step shapes are present).
func (r *Runner) Validate(m *manifest.Manifest) error {
	if m == nil {
		return errors.New("composite: manifest is nil")
	}
	if m.Kind != manifest.KindComposite {
		return fmt.Errorf("composite: manifest kind %q does not match runner kind %q",
			m.Kind, manifest.KindComposite)
	}
	if r.Spec != nil {
		return validateSpec(r.Spec)
	}
	return nil
}

// validateSpec runs spec-only checks that do not depend on a manifest:
// non-empty step list, well-formed steps, and the callbacks the
// configured steps actually need.
func validateSpec(s *Spec) error {
	if len(s.Steps) == 0 {
		return errors.New("composite: spec has no steps")
	}
	var needsLookup, needsAwait bool
	for i, step := range s.Steps {
		if err := validateStep(i, step); err != nil {
			return err
		}
		if step.isConfirm() {
			needsAwait = true
		} else {
			needsLookup = true
		}
	}
	if needsLookup {
		if s.Lookup == nil {
			return errors.New("composite: spec.lookup is required when any step has run")
		}
		if s.Invoke == nil {
			return errors.New("composite: spec.invoke is required when any step has run")
		}
	}
	if needsAwait && s.Await == nil {
		return errors.New("composite: spec.await is required when any step has confirm")
	}
	return nil
}

// validateStep checks the per-step invariants: exactly one of Run /
// Confirm; a recognised InputsFrom; static steps carry a non-empty
// Inputs map; OnFailure is recognised.
func validateStep(i int, s Step) error {
	switch {
	case s.Run == "" && s.Confirm == "":
		return fmt.Errorf("composite: steps[%d]: one of run or confirm is required", i)
	case s.Run != "" && s.Confirm != "":
		return fmt.Errorf("composite: steps[%d]: run and confirm are mutually exclusive", i)
	}
	if s.isConfirm() {
		// inputs_from / inputs / on_failure are meaningless for confirm
		// pseudo-steps; ignore them silently rather than rejecting so
		// authors can keep a single linter-friendly schema.
		return nil
	}
	switch s.inputsFrom() {
	case InputsFromPrompt, InputsFromPrevious, InputsFromStatic:
		// ok
	default:
		return fmt.Errorf("composite: steps[%d]: unknown inputs_from %q", i, s.InputsFrom)
	}
	if s.inputsFrom() == InputsFromStatic && len(s.Inputs) == 0 {
		return fmt.Errorf("composite: steps[%d]: inputs is required when inputs_from is %q",
			i, InputsFromStatic)
	}
	switch s.onFailure() {
	case OnFailureAbort, OnFailureContinue:
		// ok
	default:
		return fmt.Errorf("composite: steps[%d]: unknown on_failure %q", i, s.OnFailure)
	}
	return nil
}

// Run walks Spec.Steps in declared order, dispatching each run-step
// through Spec.Invoke and pausing on each confirm pseudo-step. It honors
// ctx cancellation at every step boundary and inside Spec.Await; the
// dispatcher itself is responsible for propagating ctx into the inner
// runner's blocking work.
func (r *Runner) Run(ctx context.Context, m *manifest.Manifest, in action.Inputs) (action.Result, error) {
	if r.Spec == nil {
		return action.Result{Kind: manifest.KindComposite}, ErrSpecMissing
	}
	if err := validateSpec(r.Spec); err != nil {
		return action.Result{Kind: manifest.KindComposite}, err
	}

	out := &Result{Aborted: -1}
	var prevInputs action.Inputs

	for i, step := range r.Spec.Steps {
		if err := ctx.Err(); err != nil {
			return action.Result{Kind: manifest.KindComposite, Value: out}, err
		}

		if step.isConfirm() {
			accepted, err := r.runConfirm(ctx, i, step)
			out.Steps = append(out.Steps, StepResult{
				Index:    i,
				Confirm:  step.Confirm,
				Accepted: accepted,
				Err:      err,
			})
			if err != nil {
				out.Aborted = i
				return action.Result{Kind: manifest.KindComposite, Value: out}, err
			}
			continue
		}

		target, inputs, runErr := r.runStep(ctx, i, step, prevInputs, in)
		sr := StepResult{
			Index:  i,
			Slash:  step.Run,
			Inputs: inputs,
		}
		if runErr != nil {
			sr.Err = runErr
		} else {
			sr.Value = target.Value
			prevInputs = inputs
		}
		out.Steps = append(out.Steps, sr)

		if runErr != nil && step.onFailure() == OnFailureAbort {
			out.Aborted = i
			return action.Result{Kind: manifest.KindComposite, Value: out}, runErr
		}
	}

	return action.Result{Kind: manifest.KindComposite, Value: out}, nil
}

// runConfirm publishes the ConfirmRequiredEvent (when a bus is wired)
// and then blocks on Spec.Await. A false return from Await maps to
// ErrConfirmRejected so callers branch on a typed sentinel rather than
// scraping error strings.
func (r *Runner) runConfirm(ctx context.Context, i int, step Step) (bool, error) {
	if r.Spec.Bus != nil {
		r.Spec.Bus.Publish(r.Spec.RequestID, ConfirmRequiredEvent{
			Step:    i,
			Message: step.Confirm,
		})
	}
	accepted, err := r.Spec.Await(ctx, i, step.Confirm)
	if err != nil {
		return false, err
	}
	if !accepted {
		return false, ErrConfirmRejected
	}
	return true, nil
}

// runStep resolves the step's target manifest, derives its inputs, and
// invokes it through Spec.Invoke. The returned action.Result is the
// inner runner's Result so the caller can capture .Value; inputs are
// returned alongside so the next "previous" step has them to carry
// forward.
func (r *Runner) runStep(
	ctx context.Context,
	i int,
	step Step,
	prevInputs action.Inputs,
	topLevel action.Inputs,
) (action.Result, action.Inputs, error) {
	target, err := r.Spec.Lookup(step.Run)
	if err != nil {
		return action.Result{}, nil, fmt.Errorf("composite: steps[%d] (%s): lookup: %w", i, step.Run, err)
	}
	if target == nil {
		return action.Result{}, nil, fmt.Errorf("composite: steps[%d] (%s): lookup returned nil manifest", i, step.Run)
	}
	inputs, err := r.resolveInputs(ctx, i, step, target, prevInputs, topLevel)
	if err != nil {
		return action.Result{}, nil, fmt.Errorf("composite: steps[%d] (%s): inputs: %w", i, step.Run, err)
	}
	res, err := r.Spec.Invoke(ctx, target, inputs)
	if err != nil {
		return res, inputs, err
	}
	return res, inputs, nil
}

func init() { action.Register(&Runner{}) }
