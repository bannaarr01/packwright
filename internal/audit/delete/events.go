package delete

// This file defines the per-row progress events emitted by Executor
// onto the stream.EventBus. They implement stream.Event so the same
// bus consumers (TUI, GUI, tests) used by the rest of Packwright can
// branch on EventKind() / type-switch on the concrete struct.
//
// Per ADR-0043, the events are deliberately small: the bus is the
// channel for "this row started / succeeded / failed / was skipped"
// and nothing more. Richer rendering data lives in the Row itself,
// which subscribers can look up by RowID.

// DeleteStarted is emitted just before a per-row Delete* AWS call is
// issued.
type DeleteStarted struct {
	RowID string
	Kind  Kind
}

// EventKind implements stream.Event.
func (DeleteStarted) EventKind() string { return "delete_started" }

// DeleteSucceeded is emitted when a per-row Delete* AWS call
// returned without error.
type DeleteSucceeded struct {
	RowID string
	Kind  Kind
}

// EventKind implements stream.Event.
func (DeleteSucceeded) EventKind() string { return "delete_succeeded" }

// DeleteFailed is emitted when a per-row Delete* AWS call returned
// an error. The executor continues with subsequent rows; failures do
// not abort the batch.
type DeleteFailed struct {
	RowID string
	Kind  Kind
	Err   error
}

// EventKind implements stream.Event.
func (DeleteFailed) EventKind() string { return "delete_failed" }

// SkipReason explains why a row was skipped.
type SkipReason string

// Skip reasons. These are the closed set of values Executor will
// emit; consumers can render them verbatim.
const (
	// SkipUnselected means the user did not check the row's box in
	// the batch consent modal.
	SkipUnselected SkipReason = "unselected"
	// SkipBlocked means a blocking dependent prevented the row from
	// being checkable. The user cannot select it; the executor
	// emits this event for clarity in the progress strip.
	SkipBlocked SkipReason = "blocked"
	// SkipCancelled means the batch was cancelled (via context)
	// before this row's Delete* call was issued.
	SkipCancelled SkipReason = "cancelled"
)

// DeleteSkipped is emitted instead of Started/Succeeded/Failed when
// a row is not deleted at all.
type DeleteSkipped struct {
	RowID  string
	Kind   Kind
	Reason SkipReason
}

// EventKind implements stream.Event.
func (DeleteSkipped) EventKind() string { return "delete_skipped" }

// BatchStarted is emitted exactly once at the very start of an
// Execute call, after the typed-DELETE confirmation has been
// verified but before any per-row event is published. Total is the
// number of tray rows surfaced to the consent modal — the
// denominator a UI subscriber should reconcile per-row events
// against. Selected vs. skipped is reported via the per-row
// events, not via the Total field.
type BatchStarted struct {
	Total int
}

// EventKind implements stream.Event.
func (BatchStarted) EventKind() string { return "delete_batch_started" }

// BatchFinished is emitted exactly once at the end of Execute,
// regardless of outcome (success, partial failure, cancellation).
type BatchFinished struct {
	Total     int
	Succeeded int
	Failed    int
	Skipped   int
	// Cancelled is true when execution stopped because ctx was done.
	Cancelled bool
}

// EventKind implements stream.Event.
func (BatchFinished) EventKind() string { return "delete_batch_finished" }
