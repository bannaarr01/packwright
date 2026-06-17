package delete

import (
	"context"
	"errors"
	"log/slog"

	"github.com/bannaarr01/packwright/internal/stream"
)

// EventPublisher is the narrow interface Executor uses to emit
// per-row progress events. *stream.EventBus satisfies it
// structurally; tests pass a hand-rolled publisher that records
// every emitted event.
//
// A nil EventPublisher is supported — the executor skips Publish
// calls in that case so production code that does not want a bus
// (e.g. a headless CLI batch run) can omit one.
type EventPublisher interface {
	Publish(requestID string, ev stream.Event)
}

// Executor drives a batch consent → delete sequence to completion.
// Every field other than Clients is optional; the zero value is not
// usable (Clients is required) but a minimally-configured executor
// (no Log, no Bus) still works — it just produces no audit trail
// and no progress events.
type Executor struct {
	// Clients is the AWS service bundle. Required.
	Clients *Clients
	// Log appends one LogEntry per row. Optional; a nil Log means
	// the audit trail is skipped, but the deletes still run.
	Log LogWriter
	// Bus publishes per-row progress events under RequestID.
	// Optional — see EventPublisher.
	Bus EventPublisher
	// RequestID is the bus key used by Publish. Empty defaults to
	// "delete" so a single Executor without an explicit ID is
	// still a usable producer.
	RequestID string
}

// Execute runs a batch of selected, dependency-ordered rows. It
// performs the typed-DELETE check before issuing any AWS call;
// callers that test this short-circuit can call Execute with a
// Batch whose TypedConfirm is empty and assert no Delete* call
// fires.
//
// tray is the snapshot of staged rows that the user consented to.
// deps may be empty (no probe was run); when present it is used to
// reject Batches that selected a blocked row.
//
// The error returned is non-nil only when the preflight fails
// (typed-DELETE missing, blocked row selected, cyclic deps). A
// row-level Delete* failure is logged and surfaced via
// DeleteFailed events but does not cause Execute to return an
// error — ADR-0043 §"Failures don't abort the batch".
//
// Cancellation: a done ctx between rows stops further Delete*
// dispatch; remaining rows emit DeleteSkipped(SkipCancelled). An
// AWS call already issued is allowed to complete (this is a "no
// new calls fired" guarantee, not a hard cancel).
func (e *Executor) Execute(ctx context.Context, tray []Row, deps []RowDependencies, batch Batch) error {
	if e.Clients == nil {
		return errors.New("delete: Executor.Clients is nil")
	}
	if err := batch.Validate(tray, deps); err != nil {
		return err
	}

	consentHash := batch.Hash()
	selected := batch.SelectedRows(tray)
	ordered, err := Order(selected)
	if err != nil {
		return err
	}

	// BatchStarted brackets the whole run so subscribers can
	// initialise their progress display before any per-row event
	// fires. Total counts every row the user staged at consent
	// time — selected, unselected, and blocked — so a consumer can
	// reconcile per-row events against the announced total without
	// tracking selection separately.
	e.publish(BatchStarted{Total: len(tray)})

	// Emit skip entries for rows that were on the tray but not
	// selected. Done before the selected loop so the audit log and
	// event stream capture the user's full intent at consent time,
	// not just the rows that ran.
	selSet := make(map[string]bool, len(selected))
	for _, r := range selected {
		selSet[r.ID] = true
	}
	blockedSet := make(map[string]bool, len(deps))
	for _, d := range deps {
		blockedSet[d.RowID] = d.Blocked
	}
	var skipped int
	for _, r := range tray {
		if selSet[r.ID] {
			continue
		}
		reason := SkipUnselected
		if blockedSet[r.ID] {
			reason = SkipBlocked
		}
		e.publish(DeleteSkipped{RowID: r.ID, Kind: r.Resource.Kind, Reason: reason})
		e.writeSkip(r, consentHash, reason)
		skipped++
	}
	var succeeded, failed int
	var cancelled bool
	for _, row := range ordered {
		if err := ctx.Err(); err != nil {
			cancelled = true
			e.publish(DeleteSkipped{RowID: row.ID, Kind: row.Resource.Kind, Reason: SkipCancelled})
			e.writeSkip(row, consentHash, SkipCancelled)
			skipped++
			continue
		}
		e.publish(DeleteStarted{RowID: row.ID, Kind: row.Resource.Kind})
		callErr := DeleteResource(ctx, e.Clients, row.Resource)
		if callErr != nil {
			failed++
			e.publish(DeleteFailed{RowID: row.ID, Kind: row.Resource.Kind, Err: callErr})
			e.writeEntry(row, consentHash, OutcomeFailed, callErr.Error())
			continue
		}
		succeeded++
		e.publish(DeleteSucceeded{RowID: row.ID, Kind: row.Resource.Kind})
		e.writeEntry(row, consentHash, OutcomeDeleted, "")
	}
	e.publish(BatchFinished{
		Total:     len(tray),
		Succeeded: succeeded,
		Failed:    failed,
		Skipped:   skipped,
		Cancelled: cancelled,
	})
	return nil
}

func (e *Executor) publish(ev stream.Event) {
	if e.Bus == nil {
		return
	}
	id := e.RequestID
	if id == "" {
		id = "delete"
	}
	e.Bus.Publish(id, ev)
}

func (e *Executor) writeEntry(row Row, consentHash string, outcome Outcome, reason string) {
	if e.Log == nil {
		return
	}
	err := e.Log.Write(LogEntry{
		RowID:       row.ID,
		Kind:        string(row.Resource.Kind),
		Identifier:  row.Resource.Identifier,
		Account:     row.Resource.AccountID,
		Region:      row.Resource.Region,
		Profile:     row.Resource.Profile,
		ConsentHash: consentHash,
		Outcome:     outcome,
		Reason:      reason,
	})
	if err != nil {
		slog.Warn("delete: audit log write failed",
			slog.String("row_id", row.ID),
			slog.String("kind", string(row.Resource.Kind)),
			slog.Any("err", err),
		)
	}
}

func (e *Executor) writeSkip(row Row, consentHash string, reason SkipReason) {
	e.writeEntry(row, consentHash, OutcomeSkipped, string(reason))
}
