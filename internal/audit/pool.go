package audit

import (
	"context"
	"sync"
)

// RunOptions configures a [Run] invocation. The zero value is valid.
type RunOptions struct {
	// Concurrency caps how many scanners run in parallel. A zero or
	// negative value falls through to [DefaultConcurrency].
	Concurrency int
}

// DefaultConcurrency is the worker-pool cap when RunOptions.Concurrency
// is unset. ADR-0040 calls for 8: enough that a multi-service audit
// finishes quickly, low enough that a small AWS account never sees
// itself rate-limited even before the per-service buckets kick in.
const DefaultConcurrency = 8

// Result is the final outcome of an audit. Resources is the flat union
// of every scanner's output in completion order; Errors maps each
// failed scanner's Kind to the error it returned. A scanner that
// succeeds with zero resources contributes an empty slice and no entry
// in Errors.
type Result struct {
	Resources []Resource
	Errors    map[string]error
}

// Run drives every scanner concurrently against client, respecting the
// configured concurrency cap. It returns two channels:
//
//   - events streams every Started/Progress/Done/Error/Warn event in the
//     order the scanner pool produces them. Closed when the run finishes.
//   - result delivers exactly one [Result] (the final aggregate) and is
//     closed immediately after. Callers can range over events and read
//     the single Result without coordination.
//
// Every channel send is selected against ctx.Done(), so cancelling ctx
// unblocks the pool even when the caller has stopped draining events.
// In that case in-flight scanners abandon their emits, finish promptly,
// and the goroutine still closes both channels cleanly.
func Run(ctx context.Context, scanners []Scanner, client *Client, opts RunOptions) (<-chan Event, <-chan Result) {
	cap := opts.Concurrency
	if cap <= 0 {
		cap = DefaultConcurrency
	}
	if len(scanners) > 0 && cap > len(scanners) {
		cap = len(scanners)
	}

	events := make(chan Event, 64)
	result := make(chan Result, 1)

	go func() {
		defer close(events)
		defer close(result)

		var (
			mu        sync.Mutex
			resources []Resource
			errs      = map[string]error{}
		)

		sem := make(chan struct{}, cap)
		var wg sync.WaitGroup

		for _, s := range scanners {
			s := s
			// Check cancellation first so an already-done context
			// deterministically wins over an available semaphore slot
			// — Go's select picks at random when both cases are ready,
			// which would otherwise let the first scanner sneak in
			// after a Cancel before Run.
			if err := ctx.Err(); err != nil {
				sendOrDrop(ctx, events, Event{Type: EventError, Kind: s.Kind(), Err: err})
				mu.Lock()
				errs[s.Kind()] = err
				mu.Unlock()
				continue
			}
			select {
			case <-ctx.Done():
				sendOrDrop(ctx, events, Event{Type: EventError, Kind: s.Kind(), Err: ctx.Err()})
				mu.Lock()
				errs[s.Kind()] = ctx.Err()
				mu.Unlock()
				continue
			case sem <- struct{}{}:
			}

			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				sendOrDrop(ctx, events, Event{Type: EventStarted, Kind: s.Kind()})
				emit := &scannerEmitter{ctx: ctx, ch: events, kind: s.Kind()}
				out, err := s.Scan(ctx, client, emit)
				if err != nil {
					sendOrDrop(ctx, events, Event{Type: EventError, Kind: s.Kind(), Err: err})
					mu.Lock()
					errs[s.Kind()] = err
					mu.Unlock()
					return
				}
				sendOrDrop(ctx, events, Event{Type: EventDone, Kind: s.Kind(), Count: len(out)})
				mu.Lock()
				resources = append(resources, out...)
				mu.Unlock()
			}()
		}

		wg.Wait()
		result <- Result{Resources: resources, Errors: errs}
	}()

	return events, result
}

// sendOrDrop sends ev on ch, unblocking on ctx.Done() so a caller that
// stops draining events does not deadlock the pool goroutines. The
// "drop" path is the intended escape hatch — once the caller has given
// up on the audit, the events it never read are not interesting.
func sendOrDrop(ctx context.Context, ch chan<- Event, ev Event) {
	select {
	case ch <- ev:
	case <-ctx.Done():
	}
}

// scannerEmitter is the per-scanner ScannerEmitter the pool hands to
// Scan. It tags every event with the scanner's kind so consumers never
// have to thread that through themselves, and selects against ctx so a
// cancelled audit unblocks Progress/Warn the same way as Started/Done.
type scannerEmitter struct {
	ctx  context.Context
	ch   chan<- Event
	kind string
}

// Progress implements ScannerEmitter.
func (e *scannerEmitter) Progress(count int) {
	sendOrDrop(e.ctx, e.ch, Event{Type: EventProgress, Kind: e.kind, Count: count})
}

// Warn implements ScannerEmitter.
func (e *scannerEmitter) Warn(msg string) {
	sendOrDrop(e.ctx, e.ch, Event{Type: EventWarn, Kind: e.kind, Msg: msg})
}
