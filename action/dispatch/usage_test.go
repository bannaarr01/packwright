package dispatch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/action"
	"github.com/bannaarr01/packwright/internal/usage"
	"github.com/bannaarr01/packwright/manifest"
	"github.com/bannaarr01/packwright/meta"
)

// withRecorder swaps the package-level usage recorder for the duration
// of the test and returns the slice the swap writes into. The cleanup
// hook restores the original recorder so test ordering cannot leak.
func withRecorder(t *testing.T) *eventBuffer {
	t.Helper()
	buf := &eventBuffer{}
	orig := recordUsage
	recordUsage = buf.Record
	t.Cleanup(func() { recordUsage = orig })
	return buf
}

// eventBuffer collects every UsageEvent Dispatch emits while a test is
// running. It is safe to call from any goroutine that Dispatch fans out
// to.
type eventBuffer struct {
	mu     sync.Mutex
	events []usage.UsageEvent
}

func (b *eventBuffer) Record(ev usage.UsageEvent) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, ev)
	return nil
}

func (b *eventBuffer) snapshot() []usage.UsageEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]usage.UsageEvent, len(b.events))
	copy(out, b.events)
	return out
}

// stubKindRunner is a hand-rolled action.Runner that returns a fixed
// (Result, error) pair. It lets the usage tests exercise success /
// failed / cancelled outcomes without leaning on the real resource or
// stub kinds — those have their own error semantics and shapes.
type stubKindRunner struct {
	kind manifest.Kind
	err  error
}

func (s stubKindRunner) Kind() manifest.Kind                 { return s.kind }
func (s stubKindRunner) Validate(m *manifest.Manifest) error { return nil }
func (s stubKindRunner) Run(ctx context.Context, m *manifest.Manifest, in action.Inputs) (action.Result, error) {
	return action.Result{Kind: s.kind}, s.err
}

// stubManifest builds a structurally valid manifest pinned to a custom
// kind. The slash and id are deterministic so assertions on the
// recorded event can compare exactly.
func stubManifest(kind manifest.Kind, slash string) *manifest.Manifest {
	return &manifest.Manifest{
		SchemaVersion: "packwright.manifest.v1",
		ID:            "stub",
		Kind:          kind,
		Slash:         slash,
		Title:         "stub",
	}
}

// withRegisteredKind installs a runner against a fresh manifest.Kind.
// Each test uses a unique kind string so the package-global registry
// can safely accumulate registrations across a test binary.
func withRegisteredKind(t *testing.T, k manifest.Kind, err error) {
	t.Helper()
	action.Register(stubKindRunner{kind: k, err: err})
}

// TestDispatch_RecordsSuccess verifies that a successful Dispatch
// produces one usage event with the expected schema values.
func TestDispatch_RecordsSuccess(t *testing.T) {
	buf := withRecorder(t)

	const k manifest.Kind = "usagetest-success"
	withRegisteredKind(t, k, nil)

	ctx := WithSurface(context.Background(), usage.SurfaceTUI)
	if _, err := Dispatch(ctx, stubManifest(k, "/ok"), nil); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	events := buf.snapshot()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1: %#v", len(events), events)
	}
	ev := events[0]
	if ev.Command != "/ok" {
		t.Errorf("Command = %q, want %q", ev.Command, "/ok")
	}
	if string(ev.Kind) != string(k) {
		t.Errorf("Kind = %q, want %q", ev.Kind, k)
	}
	if ev.Outcome != usage.OutcomeSuccess {
		t.Errorf("Outcome = %q, want %q", ev.Outcome, usage.OutcomeSuccess)
	}
	if ev.Surface != usage.SurfaceTUI {
		t.Errorf("Surface = %q, want %q", ev.Surface, usage.SurfaceTUI)
	}
	if ev.Version != meta.Version {
		t.Errorf("Version = %q, want %q", ev.Version, meta.Version)
	}
	if ev.Timestamp.IsZero() {
		t.Error("Timestamp is zero, want non-zero")
	}
	if ev.Duration < 0 {
		t.Errorf("Duration = %v, want non-negative", ev.Duration)
	}
}

// TestDispatch_RecordsFailedOutcome confirms a generic runner error
// surfaces as outcome=failed.
func TestDispatch_RecordsFailedOutcome(t *testing.T) {
	buf := withRecorder(t)

	const k manifest.Kind = "usagetest-failed"
	withRegisteredKind(t, k, errors.New("boom"))

	ctx := WithSurface(context.Background(), usage.SurfaceGUI)
	if _, err := Dispatch(ctx, stubManifest(k, "/boom"), nil); err == nil {
		t.Fatal("Dispatch: expected error, got nil")
	}

	events := buf.snapshot()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if got := events[0].Outcome; got != usage.OutcomeFailed {
		t.Errorf("Outcome = %q, want %q", got, usage.OutcomeFailed)
	}
	if got := events[0].Surface; got != usage.SurfaceGUI {
		t.Errorf("Surface = %q, want %q", got, usage.SurfaceGUI)
	}
}

// TestDispatch_RecordsCancelledOutcome verifies that context.Canceled
// and context.DeadlineExceeded are bucketed as cancelled rather than
// failed — they reflect user / timeout actions, not engine faults.
func TestDispatch_RecordsCancelledOutcome(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"canceled", context.Canceled},
		{"deadline", context.DeadlineExceeded},
		{"wrapped-canceled", &wrappedErr{inner: context.Canceled}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := withRecorder(t)

			k := manifest.Kind("usagetest-cancel-" + tc.name)
			withRegisteredKind(t, k, tc.err)

			ctx := WithSurface(context.Background(), usage.SurfaceTUI)
			if _, err := Dispatch(ctx, stubManifest(k, "/cancel"), nil); err == nil {
				t.Fatal("Dispatch: expected error, got nil")
			}
			events := buf.snapshot()
			if len(events) != 1 {
				t.Fatalf("got %d events, want 1", len(events))
			}
			if got := events[0].Outcome; got != usage.OutcomeCancelled {
				t.Errorf("Outcome = %q, want %q", got, usage.OutcomeCancelled)
			}
		})
	}
}

// TestDispatch_RecordsRegistryMiss verifies that an unknown kind still
// produces a usage record — the user invoked something, even if the
// engine refused. Outcome is failed; recorded kind is the unknown
// value as-supplied.
func TestDispatch_RecordsRegistryMiss(t *testing.T) {
	buf := withRecorder(t)
	const k manifest.Kind = "usagetest-bogus"

	_, err := Dispatch(context.Background(), stubManifest(k, "/bogus"), nil)
	if err == nil {
		t.Fatal("Dispatch: expected registry error, got nil")
	}

	events := buf.snapshot()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if got := events[0].Kind; string(got) != string(k) {
		t.Errorf("Kind = %q, want %q", got, k)
	}
	if got := events[0].Outcome; got != usage.OutcomeFailed {
		t.Errorf("Outcome = %q, want failed", got)
	}
}

// TestDispatch_NilManifestSkipsRecording protects the nil-manifest
// guard: a malformed call must not pollute the usage log, since there
// is no command name to record against.
func TestDispatch_NilManifestSkipsRecording(t *testing.T) {
	buf := withRecorder(t)
	if _, err := Dispatch(context.Background(), nil, nil); err == nil {
		t.Fatal("Dispatch: expected ErrNoManifest, got nil")
	}
	if events := buf.snapshot(); len(events) != 0 {
		t.Errorf("got %d events for nil manifest, want 0: %#v", len(events), events)
	}
}

// TestDispatch_RecordsDurationApproximately samples a runner that
// sleeps briefly and asserts the recorded duration is at least the
// sleep window. The upper bound is generous to absorb CI jitter.
func TestDispatch_RecordsDurationApproximately(t *testing.T) {
	buf := withRecorder(t)
	const k manifest.Kind = "usagetest-slow"

	action.Register(slowRunner{kind: k, sleep: 20 * time.Millisecond})

	if _, err := Dispatch(context.Background(), stubManifest(k, "/slow"), nil); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	events := buf.snapshot()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Duration < 20*time.Millisecond {
		t.Errorf("Duration = %v, want >= 20ms", events[0].Duration)
	}
}

// TestDispatch_RecordingFailureDoesNotPropagate confirms the recording
// best-effort contract: even when the recorder returns an error,
// Dispatch's own return value is unchanged.
func TestDispatch_RecordingFailureDoesNotPropagate(t *testing.T) {
	orig := recordUsage
	recordUsage = func(usage.UsageEvent) error { return errors.New("disk full") }
	t.Cleanup(func() { recordUsage = orig })

	const k manifest.Kind = "usagetest-recfail"
	withRegisteredKind(t, k, nil)

	if _, err := Dispatch(context.Background(), stubManifest(k, "/ok"), nil); err != nil {
		t.Fatalf("Dispatch leaked recorder error: %v", err)
	}
}

// TestSurfaceFromContextDefault confirms an absent ctx-bound surface
// falls back to the value SetDefaultSurface recorded — bootstrap sets
// that at startup so usage events are tagged with the running surface
// even before per-call dispatch.WithSurface plumbing exists. With the
// default unset the function returns the empty string, matching the
// pre-SetDefaultSurface contract.
func TestSurfaceFromContextDefault(t *testing.T) {
	t.Cleanup(func() { SetDefaultSurface("") })

	// Empty default: legacy behaviour.
	SetDefaultSurface("")
	if got := surfaceFromContext(context.Background()); got != "" {
		t.Errorf("surfaceFromContext with empty default = %q, want empty", got)
	}

	// Bootstrap-style default: callers without WithSurface get the
	// running surface.
	SetDefaultSurface(usage.SurfaceTUI)
	if got := surfaceFromContext(context.Background()); got != usage.SurfaceTUI {
		t.Errorf("surfaceFromContext with default=%q on bare ctx = %q",
			usage.SurfaceTUI, got)
	}

	// Explicit WithSurface still wins over the default.
	SetDefaultSurface(usage.SurfaceTUI)
	ctx := WithSurface(context.Background(), usage.SurfaceGUI)
	if got := surfaceFromContext(ctx); got != usage.SurfaceGUI {
		t.Errorf("surfaceFromContext with WithSurface=%q over default=%q = %q",
			usage.SurfaceGUI, usage.SurfaceTUI, got)
	}
}

// TestClassifyOutcomeFlowsThroughErrors tightens the contract beyond
// the per-Dispatch tests — every error class maps to the expected
// outcome value via classifyOutcome directly.
func TestClassifyOutcomeFlowsThroughErrors(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want usage.Outcome
	}{
		{"nil", nil, usage.OutcomeSuccess},
		{"canceled", context.Canceled, usage.OutcomeCancelled},
		{"deadline", context.DeadlineExceeded, usage.OutcomeCancelled},
		{"wrapped-canceled", &wrappedErr{inner: context.Canceled}, usage.OutcomeCancelled},
		{"plain", errors.New("oops"), usage.OutcomeFailed},
	}
	for _, tc := range cases {
		if got := classifyOutcome(tc.in); got != tc.want {
			t.Errorf("classifyOutcome(%v) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// slowRunner sleeps briefly inside Run so the duration assertion has
// something concrete to measure against.
type slowRunner struct {
	kind  manifest.Kind
	sleep time.Duration
}

func (s slowRunner) Kind() manifest.Kind                 { return s.kind }
func (s slowRunner) Validate(m *manifest.Manifest) error { return nil }
func (s slowRunner) Run(ctx context.Context, m *manifest.Manifest, in action.Inputs) (action.Result, error) {
	select {
	case <-time.After(s.sleep):
	case <-ctx.Done():
	}
	return action.Result{Kind: s.kind}, nil
}

// wrappedErr lets the cancelled-outcome tests prove errors.Is unwrapping
// flows through Dispatch's classifier.
type wrappedErr struct{ inner error }

func (w *wrappedErr) Error() string { return "wrapped: " + w.inner.Error() }
func (w *wrappedErr) Unwrap() error { return w.inner }

// Sanity check that the test file imports compile against the public
// usage symbols.
var (
	_ usage.Surface = usage.SurfaceTUI
	_ usage.Outcome = usage.OutcomeSuccess
	_               = strings.Contains
)
