// Package dispatch routes a manifest to the Runner registered for its kind
// and forwards inputs. It is the single entry point that the front-ends
// (TUI / GUI) call once a user invokes a slash command; the rest of the
// engine code branches on typed Runner methods.
//
// Every Dispatch invocation — successful or not — produces one record
// in the local usage log (internal/usage; see ADR-0031). Surface
// ("tui" / "gui") is read from ctx via WithSurface; the build version
// is read from meta.Version. Recording is best-effort: a failure to
// write the usage log never propagates to the caller.
//
// The dispatch package imports the action package directly but reaches the
// individual engines (resource, and later shell / monitor / composite) only
// via the registry. A sibling file in this same package — resource_runner.go
// — imports action/resource and registers an adapter in its init(), so the
// resource engine is wired in without dispatch.go itself depending on it.
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bannaarr01/packwright/action"
	"github.com/bannaarr01/packwright/awsx"
	"github.com/bannaarr01/packwright/internal/usage"
	"github.com/bannaarr01/packwright/manifest"
	"github.com/bannaarr01/packwright/meta"
)

// ErrNoManifest is returned when Dispatch is called with a nil manifest. It
// is a sentinel so callers can branch with errors.Is.
var ErrNoManifest = errors.New("dispatch: manifest is nil")

// awsClientKey is the private context-key type used by WithAWSClient. Using
// a struct{} type keeps the key namespace strictly scoped to this package.
type awsClientKey struct{}

// surfaceKey is the private context-key type used by WithSurface.
type surfaceKey struct{}

// WithAWSClient binds an awsx.Client to ctx so kind-specific runners that
// need AWS credentials (resource today, others later) can retrieve it
// without threading the client through Dispatch's signature.
func WithAWSClient(ctx context.Context, c *awsx.Client) context.Context {
	return context.WithValue(ctx, awsClientKey{}, c)
}

// awsClientFromContext returns the awsx.Client previously bound with
// WithAWSClient, or nil if none was set. Kept unexported because only
// in-package adapters consume it.
func awsClientFromContext(ctx context.Context) *awsx.Client {
	c, _ := ctx.Value(awsClientKey{}).(*awsx.Client)
	return c
}

// WithSurface tags ctx with the front-end that originated the dispatch
// (typically usage.SurfaceTUI or usage.SurfaceGUI). The surface is read
// back by Dispatch when emitting a usage event so the local usage log
// can attribute each command invocation to the surface that triggered
// it. When unset, the recorded surface is the empty string — informative
// rather than fatal.
func WithSurface(ctx context.Context, s usage.Surface) context.Context {
	return context.WithValue(ctx, surfaceKey{}, s)
}

// surfaceFromContext returns the usage.Surface previously bound with
// WithSurface, or the empty Surface if none was set.
func surfaceFromContext(ctx context.Context) usage.Surface {
	s, _ := ctx.Value(surfaceKey{}).(usage.Surface)
	return s
}

// recordUsage is the seam through which Dispatch emits a UsageEvent.
// Tests reassign this to capture events without going through the
// rotated file. The default forwards to the package-level usage
// recorder (a no-op until usage.Init has been called).
var recordUsage = func(ev usage.UsageEvent) error { return usage.Record(ev) }

// Dispatch looks up the Runner registered for m.Kind, runs Validate, then
// forwards to Run. The returned Result mirrors the Runner's output; the
// returned error is the first non-nil result from Validate or Run, wrapped
// with a "dispatch:" prefix when it originates inside dispatch itself.
//
// On every non-nil-manifest invocation Dispatch emits a usage event to
// the local usage log (internal/usage). The nil-manifest guard returns
// before any timing or recording so a malformed call is not counted.
func Dispatch(ctx context.Context, m *manifest.Manifest, in action.Inputs) (action.Result, error) {
	if m == nil {
		return action.Result{}, ErrNoManifest
	}

	start := time.Now()
	res, err := dispatchInner(ctx, m, in)

	// Usage recording is best-effort and must never alter the caller-
	// visible return values. A failure to write the log is ignored —
	// the operational log will surface real I/O issues elsewhere.
	_ = recordUsage(usage.UsageEvent{
		Timestamp: time.Now().UTC(),
		Command:   m.Slash,
		Kind:      usage.Kind(m.Kind),
		Duration:  time.Since(start),
		Outcome:   classifyOutcome(err),
		Surface:   surfaceFromContext(ctx),
		Version:   meta.Version,
	})

	return res, err
}

// dispatchInner is the original Dispatch body, split out so Dispatch
// itself remains the timing boundary. Keeping the recording wrapper in
// the exported function (rather than inside dispatchInner) makes the
// time.Since(start) sample reflect both the registry lookup and the
// Runner's Validate + Run, which is what users expect "duration of the
// command" to mean.
func dispatchInner(ctx context.Context, m *manifest.Manifest, in action.Inputs) (action.Result, error) {
	r, ok := action.Lookup(m.Kind)
	if !ok {
		return action.Result{Kind: m.Kind}, fmt.Errorf("dispatch: no runner registered for kind %q", m.Kind)
	}
	if err := r.Validate(m); err != nil {
		return action.Result{Kind: m.Kind}, err
	}
	return r.Run(ctx, m, in)
}

// classifyOutcome maps a Dispatch error to one of the three documented
// outcome values from ADR-0031. context.Canceled and
// context.DeadlineExceeded are reported as cancelled so user-driven
// quits don't get bucketed with real failures; every other non-nil
// error is failed; nil is success.
func classifyOutcome(err error) usage.Outcome {
	switch {
	case err == nil:
		return usage.OutcomeSuccess
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return usage.OutcomeCancelled
	default:
		return usage.OutcomeFailed
	}
}
