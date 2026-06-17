package tools

import (
	"context"
	"errors"
)

// Decision is a consent Gate's reply for a single write-tool call. Per
// ADR-0036 the user-visible UI offers three buttons (Approve once, Approve
// session, Deny) which map directly to these values; PR-04 wires that flow
// into the default Gate. PR-03 ships with a deny-everything Gate so write
// tools are inert until consent is wired up.
type Decision int

// Decision values. The zero value is DecisionDeny so an uninitialised
// Decision-returning Gate never accidentally grants access.
const (
	// DecisionDeny refuses the call. Execute is never invoked; the caller
	// receives a *ToolError with Code == ErrCodeConsentDenied.
	DecisionDeny Decision = iota
	// DecisionApproveOnce allows this single call. The next call of the
	// same tool prompts again.
	DecisionApproveOnce
	// DecisionApproveSession allows this and every subsequent call of the
	// same tool name within the current session (per ADR-0036).
	DecisionApproveSession
)

// String renders the Decision for logs and error messages.
func (d Decision) String() string {
	switch d {
	case DecisionApproveOnce:
		return "approve_once"
	case DecisionApproveSession:
		return "approve_session"
	default:
		return "deny"
	}
}

// ConsentRequest is the payload the registry hands a Gate before running a
// write tool. PR-04 enriches the request with account / region / target /
// blast-radius hints derived from the tool's args; PR-03 keeps the shape
// minimal so the Gate interface is stable across PRs.
type ConsentRequest struct {
	// Tool is the catalogue name of the write tool being requested.
	Tool string
	// Permission is the tool's declared tier (always PermissionWrite for
	// PR-03; the field is here so the Gate can branch defensively).
	Permission Permission
	// Args is the verbatim arg map the LLM supplied. Gates that render a
	// diff (cfn/update-stack, manifest/edit, file/write) read the relevant
	// keys out of this map. Reason MUST be present under args["reason"]
	// per ADR-0036; the Gate enforces that, not the registry, so PR-03
	// stays free of UX policy.
	Args map[string]any
}

// Gate is the consent-decision contract write tools defer to. PR-03's
// default implementation refuses everything; PR-04 swaps in a real Gate
// that drives the consent modal.
//
// Implementations must be safe for concurrent use — a long-running LLM
// session may issue write-tool calls from more than one goroutine and the
// registry does not serialise them.
type Gate interface {
	// Allow returns a Decision for req. A Decision of DecisionDeny — or a
	// non-nil error — refuses the call. An error is wrapped into the
	// *ToolError surfaced to callers via Cause, so audit pipelines can
	// distinguish "user denied" from "gate broken".
	Allow(ctx context.Context, req ConsentRequest) (Decision, error)
}

// ErrConsentDenied is the canonical sentinel a Gate returns when the user
// (or the default deny-all Gate) refuses a write-tool call. Callers may
// errors.Is(err, ErrConsentDenied) to detect refusal without inspecting
// the *ToolError envelope.
var ErrConsentDenied = errors.New("ai tools: consent denied")

// denyAll is the PR-03 default Gate. Every call returns DecisionDeny and
// ErrConsentDenied so write tools are inert until PR-04 wires the real
// consent flow.
type denyAll struct{}

// Allow always denies.
func (denyAll) Allow(_ context.Context, _ ConsentRequest) (Decision, error) {
	return DecisionDeny, ErrConsentDenied
}

// DefaultGate is the package-level consent Gate. PR-04's init() replaces
// it with the real consent implementation; until then it denies every
// write call.
//
// Tests that need to exercise the write path may assign their own Gate
// here and restore the original via t.Cleanup. Production code should not
// reassign DefaultGate outside of init().
var DefaultGate Gate = denyAll{}

// gateContextKey is the private context-key type used by WithGate so the
// key namespace cannot collide with another package's context keys.
type gateContextKey struct{}

// WithGate binds g to ctx so the registry — or any tool that wants to
// honour a non-default Gate for the lifetime of a call chain — can find
// it via GateFromContext. The most common use is in tests; PR-04 will
// also use it to install per-session Gates without mutating DefaultGate.
func WithGate(ctx context.Context, g Gate) context.Context {
	return context.WithValue(ctx, gateContextKey{}, g)
}

// GateFromContext returns the Gate previously bound with WithGate, or
// nil if none was set. Callers should fall back to DefaultGate on a nil
// return.
func GateFromContext(ctx context.Context) Gate {
	g, _ := ctx.Value(gateContextKey{}).(Gate)
	return g
}

// resolveGate returns the Gate to use for the current call: context-bound
// Gate first (test injection / PR-04 session Gate), then DefaultGate.
// Callers that hit nil get a deny-all so a misconfiguration cannot
// silently allow writes.
func resolveGate(ctx context.Context) Gate {
	if g := GateFromContext(ctx); g != nil {
		return g
	}
	if DefaultGate != nil {
		return DefaultGate
	}
	return denyAll{}
}
