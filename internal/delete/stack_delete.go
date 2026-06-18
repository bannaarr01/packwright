package delete

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bannaarr01/packwright/internal/stream"
)

// CFN is the minimal CloudFormation surface stack_delete.go needs.
// The shape extends stream.CFN with the event-stream query
// (DescribeStackEvents) required to drive the MVP-1 poller to
// DELETE_COMPLETE. Tests inject a fake; production wires the AWS SDK
// CloudFormation client via a thin adapter (in a follow-up cmd PR).
//
// All methods take a context and are expected to return promptly on
// cancellation.
type CFN interface {
	stream.CFN
	// DescribeStackEvents returns the stack's event history newest-
	// first, mirroring the AWS API ordering. Implementations should
	// paginate fully before returning so callers can scan for the
	// terminal status without re-querying.
	DescribeStackEvents(ctx context.Context, stackName string) ([]StackEvent, error)
}

// StackEvent is the trimmed view of a CFN stack event surfaced to
// the bus during a delete. The shape mirrors errors.StackEvent /
// stream.CFNStackEvent so callers can adapt without ceremony.
type StackEvent struct {
	EventID            string
	StackName          string
	LogicalResourceID  string
	PhysicalResourceID string
	ResourceType       string
	ResourceStatus     string
	Reason             string
	Time               time.Time
}

// DeleteStackOptions controls a single stack delete.
type DeleteStackOptions struct {
	// AfterSafeCancel must be set to true by callers that have
	// already run stream.SafeCancel for an *_IN_PROGRESS stack.
	// DeleteStack refuses to proceed against an in-progress stack
	// without this acknowledgement — ADR-0053 §"Pre-flight".
	AfterSafeCancel bool

	// PollInterval is how often DescribeStackEvents is polled while
	// streaming to DELETE_COMPLETE. Zero defaults to 5s.
	PollInterval time.Duration
	// PollTimeout caps the total time DeleteStack waits for
	// DELETE_COMPLETE. Zero defaults to 30m (CFN's own delete is
	// usually faster). The context's deadline also applies.
	PollTimeout time.Duration

	// Now is the wall-clock used for the "events newer than start"
	// filter on the poll loop. Tests override; zero defaults to
	// time.Now.
	Now func() time.Time

	// RequestID is the bus key used for event publishing. Empty
	// defaults to "delete:" + stackName so each delete has its own
	// stream.
	RequestID string
}

// ErrInProgressRequiresCancel is returned by DeleteStack pre-flight
// when the stack is *_IN_PROGRESS and the caller did not pass
// AfterSafeCancel. The cmd layer detects this and runs SafeCancel
// before retrying — keeping the SafeCancel choice in the caller
// avoids hidden destructive sub-actions inside DeleteStack.
var ErrInProgressRequiresCancel = errors.New("delete: stack is in progress; run stream.SafeCancel first or pass AfterSafeCancel")

// ErrStackDeleteFailed is the sentinel returned when the poll loop
// observes a terminal status other than DELETE_COMPLETE
// (DELETE_FAILED most commonly). Callers can errors.Is to branch.
var ErrStackDeleteFailed = errors.New("delete: stack delete reached terminal failure")

// DeleteStack runs the full stack-delete flow:
//
//  1. DescribeStackStatus pre-flight. Refuses *_IN_PROGRESS unless
//     AfterSafeCancel is set.
//  2. cloudformation.DeleteStack(stackName).
//  3. Poll DescribeStackEvents until DELETE_COMPLETE or terminal
//     failure, streaming each new event to the EventBus as a
//     stream.CFNStackEvent.
//
// bus may be nil — events are dropped in that case.
//
// Returns nil only on DELETE_COMPLETE. A terminal failure wraps
// ErrStackDeleteFailed; an in-progress refusal returns
// ErrInProgressRequiresCancel; ctx cancellation returns ctx.Err().
func DeleteStack(ctx context.Context, bus *stream.EventBus, cfn CFN, stackName string, opts DeleteStackOptions) error {
	if cfn == nil {
		return errors.New("delete: DeleteStack: cfn is nil")
	}
	if stackName == "" {
		return errors.New("delete: DeleteStack: stackName is empty")
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 5 * time.Second
	}
	if opts.PollTimeout <= 0 {
		opts.PollTimeout = 30 * time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	requestID := opts.RequestID
	if requestID == "" {
		requestID = "delete:" + stackName
	}

	status, err := cfn.DescribeStackStatus(ctx, stackName)
	if err != nil {
		if errors.Is(err, stream.ErrStackNotFound) {
			// Already gone — emit a noop terminal event for the UI.
			publishEvent(bus, requestID, stream.CFNStackEvent{
				LogicalID:      stackName,
				ResourceType:   "AWS::CloudFormation::Stack",
				ResourceStatus: "DELETE_COMPLETE",
				StatusReason:   "stack not found (already deleted)",
			})
			return nil
		}
		return fmt.Errorf("delete: describe %q: %w", stackName, err)
	}
	if isInProgress(status) && !opts.AfterSafeCancel {
		return fmt.Errorf("%w: %s is %s", ErrInProgressRequiresCancel, stackName, status)
	}

	start := opts.Now()
	if err := cfn.DeleteStack(ctx, stackName); err != nil {
		return fmt.Errorf("delete: DeleteStack %q: %w", stackName, err)
	}
	return waitForDelete(ctx, bus, cfn, stackName, requestID, start, opts)
}

// isInProgress reports whether a CFN StackStatus string is one of
// the *_IN_PROGRESS variants ADR-0053 §"Pre-flight" guards.
func isInProgress(status string) bool {
	return strings.HasSuffix(status, "_IN_PROGRESS")
}

// publishEvent forwards an event to the bus when one is configured.
func publishEvent(bus *stream.EventBus, requestID string, ev stream.Event) {
	if bus == nil {
		return
	}
	bus.Publish(requestID, ev)
}

// waitForDelete drives the poll loop. It uses a deadline derived
// from PollTimeout AND honours ctx; whichever fires first wins.
//
// On every tick it fetches the event history, emits any rows newer
// than `since` (the last event timestamp it saw), and inspects the
// stack-level rows for the terminal status. start is the wall-clock
// at the moment DeleteStack returned, used so the loop ignores any
// stale events from a previous lifecycle of the same stack name.
func waitForDelete(ctx context.Context, bus *stream.EventBus, cfn CFN, stackName, requestID string, start time.Time, opts DeleteStackOptions) error {
	deadline := start.Add(opts.PollTimeout)
	since := start
	ticker := time.NewTicker(opts.PollInterval)
	defer ticker.Stop()
	for {
		// Poll immediately on entry so callers with a 0-interval
		// fake see at least one iteration without sleeping.
		events, err := cfn.DescribeStackEvents(ctx, stackName)
		if err != nil {
			if errors.Is(err, stream.ErrStackNotFound) {
				publishEvent(bus, requestID, stream.CFNStackEvent{
					LogicalID:      stackName,
					ResourceType:   "AWS::CloudFormation::Stack",
					ResourceStatus: "DELETE_COMPLETE",
					StatusReason:   "stack not found after delete",
				})
				return nil
			}
			return fmt.Errorf("delete: describe events %q: %w", stackName, err)
		}
		newSince, terminal := drainEvents(bus, requestID, stackName, events, since)
		since = newSince
		if terminal != "" {
			if terminal == "DELETE_COMPLETE" {
				return nil
			}
			return fmt.Errorf("%w: %s status %s", ErrStackDeleteFailed, stackName, terminal)
		}
		if !opts.Now().Before(deadline) {
			return fmt.Errorf("delete: timed out waiting for %q to reach DELETE_COMPLETE", stackName)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// drainEvents publishes every event in `events` newer than since
// (yaml.v3 ordering: API returns newest-first; we publish in chrono
// order so the UI sees a coherent stream) and reports whether a
// terminal stack-level status appears.
//
// The returned newSince is the maximum event time observed, so the
// next poll skips already-published rows.
func drainEvents(bus *stream.EventBus, requestID, stackName string, events []StackEvent, since time.Time) (newSince time.Time, terminal string) {
	// Filter and reverse so older events publish first.
	var fresh []StackEvent
	for _, ev := range events {
		if ev.Time.After(since) {
			fresh = append(fresh, ev)
		}
	}
	// events came in newest-first; flip for chronological publish.
	for i, j := 0, len(fresh)-1; i < j; i, j = i+1, j-1 {
		fresh[i], fresh[j] = fresh[j], fresh[i]
	}
	newSince = since
	for _, ev := range fresh {
		publishEvent(bus, requestID, stream.CFNStackEvent{
			LogicalID:      ev.LogicalResourceID,
			ResourceType:   ev.ResourceType,
			ResourceStatus: ev.ResourceStatus,
			StatusReason:   ev.Reason,
		})
		if ev.Time.After(newSince) {
			newSince = ev.Time
		}
		// Stack-level terminal status. CFN reports the stack itself
		// as ResourceType AWS::CloudFormation::Stack with LogicalID
		// equal to the stack name on terminal transitions.
		if ev.ResourceType == "AWS::CloudFormation::Stack" && ev.LogicalResourceID == stackName {
			switch ev.ResourceStatus {
			case "DELETE_COMPLETE",
				"DELETE_FAILED",
				"ROLLBACK_FAILED":
				terminal = ev.ResourceStatus
			}
		}
	}
	return newSince, terminal
}
