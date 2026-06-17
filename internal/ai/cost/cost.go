// Package cost implements Packwright's AI cost meter and budget caps
// per ADR-0039. The package is the boundary between the LLM provider
// layer (MVP-5 PR-02) and the on-disk usage stream that powers the
// always-visible meter in the chat header.
//
// Responsibilities:
//
//   - Pre-call estimation: convert a [Request] into a USD projection
//     using the embedded pricing table (see the pricing subpackage).
//   - Cap enforcement: compare projected and accumulated spend against
//     a [Caps] policy. When a blocking cap (per-session or per-day
//     hard) would be breached, publish a [CapReached] event onto the
//     supplied EventBus and return [ErrCapExceeded] so the provider
//     never sends the request. The day-soft cap is advisory: it emits
//     a [CapReached] event with Kind=[CapDaySoft] but does not block.
//   - Post-call recording: write the actual token counts and resulting
//     USD to <home>/ai/usage.jsonl through a separate slog handler so
//     the operational log redactor cannot mangle this stream
//     (ADR-0039, mirroring the MVP-4 PR-05 pattern).
//
// The package only depends on:
//
//   - github.com/bannaarr01/packwright/internal/ai/cost/pricing for the
//     embedded pricing tables and schema validator;
//   - github.com/bannaarr01/packwright/internal/stream for the event
//     interface and bus type. [CapReached] satisfies stream.Event by
//     structural conformance — its [CapReached.EventKind] method is
//     all the bus requires.
//
// The chat UI's "cap reached" modal is out of scope for PR-07; the UI
// surfaces the modal in response to the published event.
package cost

import "errors"

// Request describes a single LLM call to the cost meter. TokensIn is
// the count the provider has already computed from the outgoing prompt
// (whichever tokenizer the provider uses); BudgetOut is the maximum
// output the caller intends to allow (typically max_tokens on the
// outgoing request).
//
// RequestID is the same identifier used elsewhere in the stack to fan
// progress events out to subscribers on the [EventBus]; cost events
// published for this call use the same key so the chat UI's subscriber
// for that turn receives them in line with the other progress events.
type Request struct {
	RequestID string
	Provider  string
	Model     string
	TokensIn  int
	BudgetOut int
}

// Usage describes the actual token consumption of a completed LLM
// call. The provider supplies it after the response stream finishes
// (or after the call fails partway, where TokensOut is whatever was
// actually emitted before the abort).
type Usage struct {
	RequestID string
	Provider  string
	Model     string
	TokensIn  int
	TokensOut int
}

// CapKind identifies which budget policy fired. The string form is
// stable: it is emitted on the event bus, recorded in the operational
// log via the chat UI, and may appear in user-facing strings — so we
// keep it human-readable and snake-cased.
type CapKind string

const (
	// CapSession is the per-session blocking cap (default $1.00). Hit
	// triggers a blocking modal in the chat UI.
	CapSession CapKind = "session"
	// CapDaySoft is the per-day advisory cap (default $5.00). The
	// meter publishes a [CapReached] event but does not block.
	CapDaySoft CapKind = "day_soft"
	// CapDayHard is the per-day blocking cap (unset by default). When
	// set and exceeded, behaves like [CapSession].
	CapDayHard CapKind = "day_hard"
)

// CapReached is the EventBus event emitted by the meter when a budget
// cap would be exceeded by the projected cost of the next call. It is
// emitted before the provider is asked to dispatch the request.
//
// CapReached deliberately lives in this package rather than in
// internal/stream/ so the PR-07 file ownership rules (only touch
// internal/ai/cost/) can be honoured; structural conformance to the
// stream.Event interface — implemented by EventKind below — is enough
// for the bus to deliver it to subscribers.
type CapReached struct {
	// Kind is the policy that fired. Always one of the [CapKind]
	// constants.
	Kind CapKind
	// LimitUSD is the configured cap value in USD.
	LimitUSD float64
	// SpentUSD is the amount already accumulated against this cap
	// (session-to-date for [CapSession], today-to-date for the day
	// caps).
	SpentUSD float64
	// ProjectedUSD is the pre-call estimate that, when added to
	// SpentUSD, exceeds LimitUSD.
	ProjectedUSD float64
	// Provider and Model identify the call whose pre-flight check
	// fired the cap. They give the modal enough context to display
	// "Claude Sonnet would have cost ~$0.18, putting you over the
	// session cap" without round-tripping back to the meter.
	Provider string
	Model    string
}

// EventKind implements the stream.Event interface. The label is
// snake-cased to match the existing event kinds (e.g. log_line,
// cfn_stack_event).
func (CapReached) EventKind() string { return "cap_reached" }

// ErrCapExceeded is returned by [Meter.PreCall] when a blocking cap
// would be breached. The provider sees this error and skips the HTTP
// call; the CapReached event has already been published on the bus so
// the UI can react. Callers may use errors.Is to branch on this
// sentinel without depending on string matches.
var ErrCapExceeded = errors.New("cost: budget cap exceeded")
