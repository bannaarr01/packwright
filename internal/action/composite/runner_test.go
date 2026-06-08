package composite

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/action"
	"github.com/bannaarr01/packwright/internal/stream"
	"github.com/bannaarr01/packwright/manifest"
)

// compositeManifest is the fixture composite handed to Run; its only
// role in tests is satisfying the Validate kind check, since step config
// lives on Spec until the manifest-loading PR ships a composite section.
func compositeManifest() *manifest.Manifest {
	return &manifest.Manifest{
		SchemaVersion: "packwright.manifest.v1",
		ID:            "test-composite",
		Kind:          manifest.KindComposite,
		Slash:         "/deploy-and-watch",
		Title:         "deploy and watch",
	}
}

// targetManifest returns a manifest fixture for slash with the supplied
// form fields. Used as the resolved target inside Spec.Lookup stubs so
// "previous" input filtering and structural Validate both have data to
// chew on.
func targetManifest(slash string, fields ...string) *manifest.Manifest {
	form := make([]manifest.Field, len(fields))
	for i, id := range fields {
		form[i] = manifest.Field{ID: id, Type: manifest.TypeString}
	}
	return &manifest.Manifest{
		SchemaVersion: "packwright.manifest.v1",
		Kind:          manifest.KindShell,
		Slash:         slash,
		Form:          form,
	}
}

// recordedCall captures one Invoke call so tests can assert ordering and
// per-step input shape.
type recordedCall struct {
	Slash  string
	Inputs action.Inputs
}

// invokeRecorder builds an Invoke callback that records the slash and
// inputs of every call and returns a canned value for that slash. A slash
// missing from results yields a nil-value successful Result so tests can
// stay terse.
func invokeRecorder(results map[string]any) (func(context.Context, *manifest.Manifest, action.Inputs) (action.Result, error), *[]recordedCall) {
	var (
		mu    sync.Mutex
		calls []recordedCall
	)
	invoke := func(_ context.Context, m *manifest.Manifest, in action.Inputs) (action.Result, error) {
		mu.Lock()
		calls = append(calls, recordedCall{Slash: m.Slash, Inputs: cloneInputs(in)})
		mu.Unlock()
		return action.Result{Kind: m.Kind, Value: results[m.Slash]}, nil
	}
	return invoke, &calls
}

// lookupFromMap builds a Lookup callback that returns the manifest for
// slash when present, or an error otherwise.
func lookupFromMap(m map[string]*manifest.Manifest) func(string) (*manifest.Manifest, error) {
	return func(slash string) (*manifest.Manifest, error) {
		if got, ok := m[slash]; ok {
			return got, nil
		}
		return nil, errors.New("unknown slash: " + slash)
	}
}

// TestRunner_Kind reports manifest.KindComposite so the dispatcher's
// registry lookup hits the composite entry.
func TestRunner_Kind(t *testing.T) {
	r := &Runner{}
	if got := r.Kind(); got != manifest.KindComposite {
		t.Errorf("Kind() = %q, want %q", got, manifest.KindComposite)
	}
}

// TestRunner_RegistersInActionRegistry confirms init() installs a
// *Runner under manifest.KindComposite, replacing the stub from
// action/kind.go.
func TestRunner_RegistersInActionRegistry(t *testing.T) {
	got, ok := action.Lookup(manifest.KindComposite)
	if !ok {
		t.Fatal("action.Lookup: no runner registered for KindComposite")
	}
	if _, ok := got.(*Runner); !ok {
		t.Errorf("action.Lookup: registered runner is %T, want *composite.Runner", got)
	}
}

// TestRunner_Validate_NilManifest is the first Validate guard: a nil
// manifest never reaches Run.
func TestRunner_Validate_NilManifest(t *testing.T) {
	r := &Runner{}
	if err := r.Validate(nil); err == nil {
		t.Fatal("Validate(nil): expected error, got nil")
	}
}

// TestRunner_Validate_WrongKind rejects manifests routed by mistake.
func TestRunner_Validate_WrongKind(t *testing.T) {
	r := &Runner{}
	m := &manifest.Manifest{Kind: manifest.KindResource}
	if err := r.Validate(m); err == nil {
		t.Fatal("Validate: expected error for wrong kind, got nil")
	}
}

// TestRunner_Validate_SpecChecks verifies the per-Spec invariants when
// a Spec is attached: empty steps, bad inputs_from, mutually exclusive
// run/confirm, and missing required callbacks.
func TestRunner_Validate_SpecChecks(t *testing.T) {
	noopLookup := func(string) (*manifest.Manifest, error) { return nil, nil }
	noopInvoke := func(context.Context, *manifest.Manifest, action.Inputs) (action.Result, error) {
		return action.Result{}, nil
	}

	cases := []struct {
		name string
		spec *Spec
	}{
		{"no steps", &Spec{}},
		{"both run and confirm", &Spec{
			Steps:  []Step{{Run: "/a", Confirm: "really?"}},
			Lookup: noopLookup, Invoke: noopInvoke,
		}},
		{"neither run nor confirm", &Spec{
			Steps:  []Step{{}},
			Lookup: noopLookup, Invoke: noopInvoke,
		}},
		{"unknown inputs_from", &Spec{
			Steps:  []Step{{Run: "/a", InputsFrom: "wat"}},
			Lookup: noopLookup, Invoke: noopInvoke,
		}},
		{"static without inputs", &Spec{
			Steps:  []Step{{Run: "/a", InputsFrom: InputsFromStatic}},
			Lookup: noopLookup, Invoke: noopInvoke,
		}},
		{"unknown on_failure", &Spec{
			Steps:  []Step{{Run: "/a", OnFailure: "yolo"}},
			Lookup: noopLookup, Invoke: noopInvoke,
		}},
		{"run without lookup", &Spec{
			Steps:  []Step{{Run: "/a"}},
			Invoke: noopInvoke,
		}},
		{"run without invoke", &Spec{
			Steps:  []Step{{Run: "/a"}},
			Lookup: noopLookup,
		}},
		{"confirm without await", &Spec{
			Steps: []Step{{Confirm: "ok?"}},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runner{Spec: tc.spec}
			if err := r.Validate(compositeManifest()); err == nil {
				t.Fatal("Validate: expected error, got nil")
			}
		})
	}
}

// TestRunner_Run_NoSpec returns ErrSpecMissing for the registered global
// instance until the manifest-loading PR wires a Spec into it.
func TestRunner_Run_NoSpec(t *testing.T) {
	r := &Runner{}
	_, err := r.Run(context.Background(), compositeManifest(), nil)
	if !errors.Is(err, ErrSpecMissing) {
		t.Errorf("Run: error = %v, want ErrSpecMissing", err)
	}
}

// TestRunner_Run_TwoStepsWithConfirm is the PR DoD: a composite with two
// run-steps and a confirm between them executes both steps in order and
// pauses for the confirm. The confirm pause is observed by subscribing
// to the EventBus before Run; the test releases the pause via the Await
// callback once the event arrives.
func TestRunner_Run_TwoStepsWithConfirm(t *testing.T) {
	bus := stream.NewEventBus(8)
	const reqID = "req-1"
	events := bus.Subscribe(reqID)

	lookup := lookupFromMap(map[string]*manifest.Manifest{
		"/alb":           targetManifest("/alb", "Name"),
		"/payments-prod": targetManifest("/payments-prod", "Name"),
	})
	invoke, calls := invokeRecorder(map[string]any{
		"/alb":           "alb-result",
		"/payments-prod": "payments-result",
	})

	// awaitCalled signals that the confirm step has published its event
	// and is now blocked on Await; the test asserts the event arrived
	// before unblocking so the "pauses for confirm" property holds.
	awaitCalled := make(chan int, 1)
	await := func(_ context.Context, step int, _ string) (bool, error) {
		awaitCalled <- step
		return true, nil
	}

	spec := &Spec{
		Steps: []Step{
			{Run: "/alb", InputsFrom: InputsFromPrompt},
			{Confirm: "ALB deployed. Open the health dashboard?"},
			{Run: "/payments-prod", InputsFrom: InputsFromPrompt},
		},
		Lookup:    lookup,
		Invoke:    invoke,
		Await:     await,
		Bus:       bus,
		RequestID: reqID,
	}
	r := &Runner{Spec: spec}

	// Subscriber goroutine: asserts the first event from the bus is a
	// ConfirmRequiredEvent for step 1 and that the runner is paused on
	// Await when the event lands.
	confirmObserved := make(chan struct{})
	go func() {
		for ev := range events {
			cr, ok := ev.(ConfirmRequiredEvent)
			if !ok {
				continue
			}
			if cr.Step != 1 {
				t.Errorf("ConfirmRequiredEvent.Step = %d, want 1", cr.Step)
			}
			if cr.Message != "ALB deployed. Open the health dashboard?" {
				t.Errorf("ConfirmRequiredEvent.Message = %q", cr.Message)
			}
			close(confirmObserved)
			return
		}
	}()

	res, err := r.Run(context.Background(), compositeManifest(), action.Inputs{"Name": "alb1"})
	bus.Close(reqID)
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}

	// Both run-steps fired in declared order, with the confirm in between.
	if got := len(*calls); got != 2 {
		t.Fatalf("Invoke calls = %d, want 2", got)
	}
	if (*calls)[0].Slash != "/alb" {
		t.Errorf("call[0].Slash = %q, want /alb", (*calls)[0].Slash)
	}
	if (*calls)[1].Slash != "/payments-prod" {
		t.Errorf("call[1].Slash = %q, want /payments-prod", (*calls)[1].Slash)
	}

	// The bus saw the ConfirmRequiredEvent.
	select {
	case <-confirmObserved:
	case <-time.After(time.Second):
		t.Fatal("never observed ConfirmRequiredEvent on the bus")
	}

	// Await was called for step index 1 (the confirm).
	select {
	case got := <-awaitCalled:
		if got != 1 {
			t.Errorf("Await step = %d, want 1", got)
		}
	default:
		t.Fatal("Await was not called for the confirm pseudo-step")
	}

	// Result captures all three step outcomes in order.
	cres, ok := res.Value.(*Result)
	if !ok {
		t.Fatalf("Result.Value = %T, want *composite.Result", res.Value)
	}
	if cres.Aborted != -1 {
		t.Errorf("Aborted = %d, want -1 (no abort)", cres.Aborted)
	}
	if got := len(cres.Steps); got != 3 {
		t.Fatalf("Result.Steps len = %d, want 3", got)
	}
	if cres.Steps[0].Slash != "/alb" || cres.Steps[0].Value != "alb-result" {
		t.Errorf("step[0] = %+v", cres.Steps[0])
	}
	if cres.Steps[1].Confirm == "" || !cres.Steps[1].Accepted {
		t.Errorf("step[1] confirm not recorded as accepted: %+v", cres.Steps[1])
	}
	if cres.Steps[2].Slash != "/payments-prod" || cres.Steps[2].Value != "payments-result" {
		t.Errorf("step[2] = %+v", cres.Steps[2])
	}
}

// TestRunner_Run_ConfirmRejected aborts the chain when Await returns
// false. The rejection surfaces as ErrConfirmRejected and subsequent
// steps are not executed.
func TestRunner_Run_ConfirmRejected(t *testing.T) {
	lookup := lookupFromMap(map[string]*manifest.Manifest{
		"/a": targetManifest("/a"),
		"/b": targetManifest("/b"),
	})
	invoke, calls := invokeRecorder(nil)
	await := func(context.Context, int, string) (bool, error) { return false, nil }

	r := &Runner{Spec: &Spec{
		Steps: []Step{
			{Run: "/a", InputsFrom: InputsFromStatic, Inputs: action.Inputs{"x": 1}},
			{Confirm: "really?"},
			{Run: "/b", InputsFrom: InputsFromStatic, Inputs: action.Inputs{"x": 2}},
		},
		Lookup: lookup,
		Invoke: invoke,
		Await:  await,
	}}
	res, err := r.Run(context.Background(), compositeManifest(), nil)
	if !errors.Is(err, ErrConfirmRejected) {
		t.Fatalf("Run: error = %v, want ErrConfirmRejected", err)
	}
	if got := len(*calls); got != 1 {
		t.Errorf("Invoke calls = %d, want 1 (second step skipped after rejection)", got)
	}
	cres := res.Value.(*Result)
	if cres.Aborted != 1 {
		t.Errorf("Aborted = %d, want 1", cres.Aborted)
	}
}

// TestRunner_Run_InputsFromStatic feeds Step.Inputs verbatim to Invoke
// regardless of the composite Run's top-level inputs.
func TestRunner_Run_InputsFromStatic(t *testing.T) {
	lookup := lookupFromMap(map[string]*manifest.Manifest{"/a": targetManifest("/a", "X")})
	invoke, calls := invokeRecorder(nil)
	r := &Runner{Spec: &Spec{
		Steps: []Step{{
			Run:        "/a",
			InputsFrom: InputsFromStatic,
			Inputs:     action.Inputs{"X": "static-value"},
		}},
		Lookup: lookup,
		Invoke: invoke,
	}}
	_, err := r.Run(context.Background(), compositeManifest(), action.Inputs{"X": "prompt-value"})
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if got := (*calls)[0].Inputs["X"]; got != "static-value" {
		t.Errorf("X = %v, want %q (static wins over top-level)", got, "static-value")
	}
}

// TestRunner_Run_InputsFromPrevious carries matching field IDs forward
// from the prior step and drops keys absent from the target's form.
func TestRunner_Run_InputsFromPrevious(t *testing.T) {
	lookup := lookupFromMap(map[string]*manifest.Manifest{
		"/a": targetManifest("/a", "Name", "Region"),
		// /b declares only Name; Region must be filtered out.
		"/b": targetManifest("/b", "Name"),
	})
	invoke, calls := invokeRecorder(nil)
	r := &Runner{Spec: &Spec{
		Steps: []Step{
			{Run: "/a", InputsFrom: InputsFromStatic, Inputs: action.Inputs{"Name": "n1", "Region": "us-east-1"}},
			{Run: "/b", InputsFrom: InputsFromPrevious},
		},
		Lookup: lookup,
		Invoke: invoke,
	}}
	_, err := r.Run(context.Background(), compositeManifest(), nil)
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	got := (*calls)[1].Inputs
	if got["Name"] != "n1" {
		t.Errorf("step[1] Name = %v, want %q", got["Name"], "n1")
	}
	if _, present := got["Region"]; present {
		t.Errorf("step[1] Region must be filtered out (target has no Region field)")
	}
}

// TestRunner_Run_InputsFromPrompt_UsesCallback honours Spec.Prompt when
// configured.
func TestRunner_Run_InputsFromPrompt_UsesCallback(t *testing.T) {
	lookup := lookupFromMap(map[string]*manifest.Manifest{"/a": targetManifest("/a", "Name")})
	invoke, calls := invokeRecorder(nil)
	r := &Runner{Spec: &Spec{
		Steps:  []Step{{Run: "/a", InputsFrom: InputsFromPrompt}},
		Lookup: lookup,
		Invoke: invoke,
		Prompt: func(_ context.Context, _ int, _ *manifest.Manifest) (action.Inputs, error) {
			return action.Inputs{"Name": "from-prompt"}, nil
		},
	}}
	_, err := r.Run(context.Background(), compositeManifest(), action.Inputs{"Name": "ignored"})
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if got := (*calls)[0].Inputs["Name"]; got != "from-prompt" {
		t.Errorf("Name = %v, want %q", got, "from-prompt")
	}
}

// TestRunner_Run_InputsFromPrompt_FallbackToTopLevel uses the composite's
// top-level inputs when Spec.Prompt is nil.
func TestRunner_Run_InputsFromPrompt_FallbackToTopLevel(t *testing.T) {
	lookup := lookupFromMap(map[string]*manifest.Manifest{"/a": targetManifest("/a", "Name")})
	invoke, calls := invokeRecorder(nil)
	r := &Runner{Spec: &Spec{
		Steps:  []Step{{Run: "/a"}},
		Lookup: lookup,
		Invoke: invoke,
	}}
	_, err := r.Run(context.Background(), compositeManifest(), action.Inputs{"Name": "top"})
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if got := (*calls)[0].Inputs["Name"]; got != "top" {
		t.Errorf("Name = %v, want %q", got, "top")
	}
}

// TestRunner_Run_OnFailureAbort halts the chain when the first step
// returns an error and OnFailure is the default.
func TestRunner_Run_OnFailureAbort(t *testing.T) {
	lookup := lookupFromMap(map[string]*manifest.Manifest{
		"/a": targetManifest("/a"),
		"/b": targetManifest("/b"),
	})
	boom := errors.New("boom")
	invoke := func(_ context.Context, m *manifest.Manifest, _ action.Inputs) (action.Result, error) {
		if m.Slash == "/a" {
			return action.Result{}, boom
		}
		return action.Result{}, nil
	}
	r := &Runner{Spec: &Spec{
		Steps:  []Step{{Run: "/a"}, {Run: "/b"}},
		Lookup: lookup,
		Invoke: invoke,
	}}
	res, err := r.Run(context.Background(), compositeManifest(), nil)
	if !errors.Is(err, boom) {
		t.Fatalf("Run: error = %v, want boom", err)
	}
	cres := res.Value.(*Result)
	if cres.Aborted != 0 {
		t.Errorf("Aborted = %d, want 0", cres.Aborted)
	}
	if got := len(cres.Steps); got != 1 {
		t.Errorf("Steps len = %d, want 1 (step 1 skipped)", got)
	}
}

// TestRunner_Run_OnFailureContinue keeps running after a failed step
// when on_failure is continue; the chain returns success even though one
// step recorded an error.
func TestRunner_Run_OnFailureContinue(t *testing.T) {
	lookup := lookupFromMap(map[string]*manifest.Manifest{
		"/a": targetManifest("/a"),
		"/b": targetManifest("/b"),
	})
	boom := errors.New("boom")
	invoke := func(_ context.Context, m *manifest.Manifest, _ action.Inputs) (action.Result, error) {
		if m.Slash == "/a" {
			return action.Result{}, boom
		}
		return action.Result{Value: "b-ok"}, nil
	}
	r := &Runner{Spec: &Spec{
		Steps:  []Step{{Run: "/a", OnFailure: OnFailureContinue}, {Run: "/b"}},
		Lookup: lookup,
		Invoke: invoke,
	}}
	res, err := r.Run(context.Background(), compositeManifest(), nil)
	if err != nil {
		t.Fatalf("Run: error = %v, want nil (on_failure: continue swallows step error)", err)
	}
	cres := res.Value.(*Result)
	if cres.Aborted != -1 {
		t.Errorf("Aborted = %d, want -1", cres.Aborted)
	}
	if got := len(cres.Steps); got != 2 {
		t.Fatalf("Steps len = %d, want 2", got)
	}
	if !errors.Is(cres.Steps[0].Err, boom) {
		t.Errorf("step[0].Err = %v, want boom", cres.Steps[0].Err)
	}
	if cres.Steps[1].Value != "b-ok" {
		t.Errorf("step[1].Value = %v, want b-ok", cres.Steps[1].Value)
	}
}

// TestRunner_Run_CancellationBetweenSteps stops the chain as soon as the
// context is cancelled and returns ctx.Err to the caller. The composite
// must observe the cancellation at the step boundary even if Invoke
// itself does not honour ctx.
func TestRunner_Run_CancellationBetweenSteps(t *testing.T) {
	lookup := lookupFromMap(map[string]*manifest.Manifest{
		"/a": targetManifest("/a"),
		"/b": targetManifest("/b"),
	})
	ctx, cancel := context.WithCancel(context.Background())
	invoke := func(_ context.Context, m *manifest.Manifest, _ action.Inputs) (action.Result, error) {
		if m.Slash == "/a" {
			cancel() // simulate user cancelling mid-chain
		}
		return action.Result{}, nil
	}
	r := &Runner{Spec: &Spec{
		Steps:  []Step{{Run: "/a"}, {Run: "/b"}},
		Lookup: lookup,
		Invoke: invoke,
	}}
	_, err := r.Run(ctx, compositeManifest(), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: error = %v, want context.Canceled", err)
	}
}

// TestRunner_Run_CancellationInsideAwait honours ctx cancellation while
// blocked on a confirm pseudo-step.
func TestRunner_Run_CancellationInsideAwait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runner{Spec: &Spec{
		Steps: []Step{{Confirm: "ok?"}},
		Await: func(ctx context.Context, _ int, _ string) (bool, error) {
			<-ctx.Done()
			return false, ctx.Err()
		},
	}}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := r.Run(ctx, compositeManifest(), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: error = %v, want context.Canceled", err)
	}
}

// TestRunner_Run_LookupError surfaces a slash-resolution failure
// verbatim and aborts the chain.
func TestRunner_Run_LookupError(t *testing.T) {
	r := &Runner{Spec: &Spec{
		Steps:  []Step{{Run: "/nope"}},
		Lookup: lookupFromMap(nil),
		Invoke: func(context.Context, *manifest.Manifest, action.Inputs) (action.Result, error) {
			t.Fatal("Invoke should not be called when Lookup fails")
			return action.Result{}, nil
		},
	}}
	_, err := r.Run(context.Background(), compositeManifest(), nil)
	if err == nil {
		t.Fatal("Run: expected lookup error, got nil")
	}
}

// TestConfirmRequiredEvent_EventKind keeps the event-discriminator
// stable; downstream wire encodings rely on this string.
func TestConfirmRequiredEvent_EventKind(t *testing.T) {
	if got := (ConfirmRequiredEvent{}).EventKind(); got != "confirm_required" {
		t.Errorf("EventKind() = %q, want %q", got, "confirm_required")
	}
}
