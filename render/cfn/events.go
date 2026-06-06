// Package cfn renders CloudFormation parameters files, drives the script-based
// deploy flow from ADR-0008, and polls CloudFormation stack events.
//
// This file owns the event-poller half of the package; the file-rendering and
// subprocess-driving half lives in renderer.go.
package cfn

import (
	"context"
	"time"
)

// StackEvent is the subset of a CloudFormation::DescribeStackEvents row that
// the engine surfaces to the front-end. EventID is the deduplication key the
// poller uses to avoid emitting the same row twice across polling rounds.
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

// EventsAPI is the narrow interface the poller depends on. PR-04 will provide
// an implementation backed by aws-sdk-go-v2's cloudformation client; for now
// callers wire in their own (a test fake, an SDK-backed adapter, etc.).
//
// DescribeStackEvents must return events newest-first, matching the AWS API.
type EventsAPI interface {
	DescribeStackEvents(ctx context.Context, stackName string) ([]StackEvent, error)
}

// Poller polls EventsAPI on a fixed interval, deduplicates by EventID, and
// emits each newly-seen event on a channel. It exits when ctx is cancelled or
// the stack reaches a terminal status.
type Poller struct {
	API      EventsAPI
	Interval time.Duration // defaults to 1 second
}

// Poll starts the polling loop in a goroutine and returns a receive-only
// channel of StackEvents. The channel is closed when the goroutine exits.
//
// Events are emitted in chronological (oldest-first) order so consumers see
// the stack timeline build up naturally, even though the API returns them
// newest-first.
//
// A nil API is treated as "no polling configured": Poll returns an
// already-closed channel so callers can fan-in unconditionally.
func (p *Poller) Poll(ctx context.Context, stackName string) <-chan StackEvent {
	out := make(chan StackEvent)
	if p == nil || p.API == nil {
		close(out)
		return out
	}

	interval := p.Interval
	if interval <= 0 {
		interval = time.Second
	}

	go func() {
		defer close(out)
		seen := make(map[string]struct{})

		// Poll once immediately so the caller doesn't have to wait an
		// interval to receive the first batch.
		if !pollOnce(ctx, p.API, stackName, seen, out) {
			return
		}

		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if !pollOnce(ctx, p.API, stackName, seen, out) {
					return
				}
			}
		}
	}()
	return out
}

// pollOnce fetches the latest events and emits each unseen one on out. It
// returns false when the poll loop should exit — either ctx was cancelled
// mid-emit, or the stack reached a terminal status.
func pollOnce(
	ctx context.Context,
	api EventsAPI,
	stackName string,
	seen map[string]struct{},
	out chan<- StackEvent,
) bool {
	events, err := api.DescribeStackEvents(ctx, stackName)
	if err != nil {
		// Transient errors are tolerated; the next tick retries.
		// TODO(MVP-2): surface repeated errors to the caller.
		return ctx.Err() == nil
	}

	terminal := false
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if _, dup := seen[e.EventID]; dup {
			continue
		}
		seen[e.EventID] = struct{}{}
		select {
		case out <- e:
		case <-ctx.Done():
			return false
		}
		if isStackTerminal(e) {
			terminal = true
		}
	}
	return !terminal
}

// isStackTerminal reports whether e marks the parent stack reaching a
// terminal CloudFormation state (success or definitive failure).
func isStackTerminal(e StackEvent) bool {
	if e.ResourceType != "AWS::CloudFormation::Stack" {
		return false
	}
	switch e.ResourceStatus {
	case "CREATE_COMPLETE",
		"UPDATE_COMPLETE",
		"DELETE_COMPLETE",
		"CREATE_FAILED",
		"DELETE_FAILED",
		"ROLLBACK_COMPLETE",
		"ROLLBACK_FAILED",
		"UPDATE_ROLLBACK_COMPLETE",
		"UPDATE_ROLLBACK_FAILED":
		return true
	}
	return false
}
