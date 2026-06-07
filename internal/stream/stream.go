// Package stream provides a framework-agnostic event bus and typed
// progress events for Packwright's long-running operations: CFN
// deploys, shell executions, monitor refreshes, and CloudWatch Logs
// queries.
//
// Both surfaces — the Wails GUI and the Bubble Tea TUI — subscribe to
// the same EventBus per request via small adapters that live in the
// front-end packages. Keeping the bus here means the engine layer can
// publish events without knowing or caring which surface(s) consume
// them, and tests can subscribe directly without spinning up a UI.
//
// See ADR-0017 for the cancellation protocol this package implements
// together with [SafeCancel].
package stream

import "sync"

// Event is the closed sum of progress events that flow through an
// [EventBus]. Concrete events are typed structs (no reflection,
// deliberately): consumers branch on the static type with a type
// switch. EventKind returns a stable, human-readable label that is
// useful for logging and as a discriminator in the wire payload sent
// to the GUI.
type Event interface {
	EventKind() string
}

// LogLine is a single line of plain text output from a long-running
// operation — shell stdout or stderr, deploy-driver progress
// messages, or anything else that produces line-oriented output.
//
// Stream is the conventional "stdout" or "stderr" for subprocess
// output, or an empty string when the originator does not distinguish
// between streams.
type LogLine struct {
	Stream string
	Text   string
}

// EventKind implements [Event].
func (LogLine) EventKind() string { return "log_line" }

// CFNStackEvent is a single CloudFormation stack event surfaced to
// the UI during a deploy or safe-cancel. ResourceStatus mirrors the
// CFN resource status string (e.g. "CREATE_IN_PROGRESS") verbatim, so
// the UI can colour or badge it without taking an AWS SDK dependency.
type CFNStackEvent struct {
	LogicalID      string
	ResourceType   string
	ResourceStatus string
	StatusReason   string
}

// EventKind implements [Event].
func (CFNStackEvent) EventKind() string { return "cfn_stack_event" }

// ShellExited is emitted once when a shell subprocess finishes, after
// all buffered stdout and stderr has already flowed through the bus
// as [LogLine] events. ExitCode is -1 when the process was killed by
// a signal.
type ShellExited struct {
	ExitCode int
}

// EventKind implements [Event].
func (ShellExited) EventKind() string { return "shell_exited" }

// ProgressTick is a generic "still working" heartbeat used by
// operations that have no natural event stream (polling Log Insights,
// waiting for a deploy to stabilise). Message is human-readable and
// may be displayed verbatim by the UI.
type ProgressTick struct {
	Message string
}

// EventKind implements [Event].
func (ProgressTick) EventKind() string { return "progress_tick" }

// CancellingStarted is emitted at the very start of a [SafeCancel]
// run, before any AWS API call. It is always emitted — even for
// stacks that turn out to be in a terminal state and trigger no
// further work — so the UI can show a "Cancelling…" indicator
// immediately.
//
// Status is the StackStatus that drove the cancellation decision, or
// the empty string when the stack does not exist.
type CancellingStarted struct {
	StackName string
	Status    string
}

// EventKind implements [Event].
func (CancellingStarted) EventKind() string { return "cancelling_started" }

// CancellingDone is emitted exactly once when [SafeCancel] returns,
// regardless of outcome. Action is one of "cancel_update_stack",
// "delete_stack", or "noop". Err is non-nil when the underlying AWS
// call failed; the same error value is returned from SafeCancel.
type CancellingDone struct {
	StackName string
	Action    string
	Err       error
}

// EventKind implements [Event].
func (CancellingDone) EventKind() string { return "cancelling_done" }

// EventBus is a thread-safe publish/subscribe hub keyed by request
// ID. Each long-running operation gets its own request ID; producers
// publish events under that ID, and zero or more subscribers receive
// every published event on their own channel in publication order.
//
// Bus state is sharded by request ID, so unrelated operations never
// contend on the same lock. The zero value is not usable; construct
// one with [NewEventBus].
//
// Concurrency contract:
//
//   - Subscribe, Publish, and Close on different request IDs are
//     safe from any goroutine and never block on each other.
//   - For a single request ID, the producer is expected to call
//     Close exactly once, happens-after every Publish for that ID.
//     This matches the natural shape of a single producer goroutine
//     (e.g. SafeCancel) emitting events and then closing the stream.
//   - Subscribers must drain their channel until it is closed; a
//     full subscriber channel back-pressures every other subscriber
//     and the producer.
type EventBus struct {
	bufferSize int

	mu      sync.Mutex
	streams map[string][]chan Event
}

// NewEventBus returns an empty bus. bufferSize is the capacity of
// each subscriber channel; a small, non-zero value (e.g. 64) lets a
// slow subscriber briefly fall behind without stalling the producer
// while still bounding memory. A bufferSize less than zero is treated
// as zero (synchronous delivery).
func NewEventBus(bufferSize int) *EventBus {
	if bufferSize < 0 {
		bufferSize = 0
	}
	return &EventBus{
		bufferSize: bufferSize,
		streams:    make(map[string][]chan Event),
	}
}

// Subscribe registers a new subscriber for requestID and returns a
// receive-only channel that will receive every event subsequently
// published under that ID, in order. The channel is closed when the
// bus is closed for that ID via [EventBus.Close].
//
// Multiple Subscribe calls with the same requestID return
// independent channels; each one observes every event. Subscribe is
// safe to call before any Publish — the subscriber simply waits.
func (b *EventBus) Subscribe(requestID string) <-chan Event {
	ch := make(chan Event, b.bufferSize)
	b.mu.Lock()
	b.streams[requestID] = append(b.streams[requestID], ch)
	b.mu.Unlock()
	return ch
}

// Publish sends ev to every subscriber registered under requestID,
// in subscription order. Publish blocks while any subscriber's
// channel is full; this is intentional back-pressure to prevent a
// slow consumer from ballooning memory.
//
// Publishing to a requestID with no subscribers is a no-op — events
// are not buffered "for later" subscribers.
//
// Per the [EventBus] concurrency contract, Publish must not race
// with [EventBus.Close] for the same requestID.
func (b *EventBus) Publish(requestID string, ev Event) {
	b.mu.Lock()
	subs := b.streams[requestID]
	snapshot := make([]chan Event, len(subs))
	copy(snapshot, subs)
	b.mu.Unlock()

	for _, ch := range snapshot {
		ch <- ev
	}
}

// Close closes every subscriber channel registered under requestID
// and removes the request from the bus. Subscribers detect the close
// via the standard "comma-ok" channel idiom and should exit their
// read loop.
//
// Close on an unknown requestID is a no-op. Close is idempotent: a
// second call for the same requestID does nothing.
func (b *EventBus) Close(requestID string) {
	b.mu.Lock()
	subs := b.streams[requestID]
	delete(b.streams, requestID)
	b.mu.Unlock()

	for _, ch := range subs {
		close(ch)
	}
}
