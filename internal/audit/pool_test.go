package audit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"testing"
	"time"
)

// stubScanner is an in-package Scanner used to exercise the pool
// without pulling in an AWS service fake. The closure-based shape lets
// each test case define its own behaviour inline.
type stubScanner struct {
	kind  string
	perms []string
	fn    func(ctx context.Context, c *Client, emit ScannerEmitter) ([]Resource, error)
}

func (s stubScanner) Kind() string          { return s.kind }
func (s stubScanner) Permissions() []string { return s.perms }
func (s stubScanner) Scan(ctx context.Context, c *Client, emit ScannerEmitter) ([]Resource, error) {
	return s.fn(ctx, c, emit)
}

// drainEvents collects every event from ch into a slice and returns it
// when the channel closes. The tests assert on the order, kind, and
// type of the resulting events.
func drainEvents(ch <-chan Event) []Event {
	var out []Event
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

// TestRunEmitsStartedDoneAndAggregatesResources is the happy path: two
// scanners both succeed and the pool emits Started/Done for each in
// addition to the final Result.
func TestRunEmitsStartedDoneAndAggregatesResources(t *testing.T) {
	scanners := []Scanner{
		stubScanner{kind: "a", perms: []string{"x:GetA"}, fn: func(_ context.Context, _ *Client, e ScannerEmitter) ([]Resource, error) {
			e.Progress(1)
			return []Resource{{Kind: "a", ID: "a-1"}}, nil
		}},
		stubScanner{kind: "b", perms: []string{"x:GetB"}, fn: func(_ context.Context, _ *Client, _ ScannerEmitter) ([]Resource, error) {
			return []Resource{{Kind: "b", ID: "b-1"}, {Kind: "b", ID: "b-2"}}, nil
		}},
	}

	events, result := Run(context.Background(), scanners, NewForTest(), RunOptions{Concurrency: 2})
	got := drainEvents(events)
	res := <-result

	if got := countOfType(got, EventStarted); got != 2 {
		t.Errorf("EventStarted count = %d, want 2", got)
	}
	if got := countOfType(got, EventDone); got != 2 {
		t.Errorf("EventDone count = %d, want 2", got)
	}
	if got := countOfType(got, EventProgress); got != 1 {
		t.Errorf("EventProgress count = %d, want 1", got)
	}
	if len(res.Resources) != 3 {
		t.Errorf("Result.Resources len = %d, want 3", len(res.Resources))
	}
	if len(res.Errors) != 0 {
		t.Errorf("Result.Errors len = %d, want 0", len(res.Errors))
	}
}

// TestRunCapturesScannerError asserts a failing scanner emits an Error
// event, lands in Result.Errors, and does not poison the other scanner.
func TestRunCapturesScannerError(t *testing.T) {
	ouch := errors.New("ouch")
	scanners := []Scanner{
		stubScanner{kind: "bad", perms: []string{"x:GetBad"}, fn: func(_ context.Context, _ *Client, _ ScannerEmitter) ([]Resource, error) {
			return nil, ouch
		}},
		stubScanner{kind: "good", perms: []string{"x:GetGood"}, fn: func(_ context.Context, _ *Client, _ ScannerEmitter) ([]Resource, error) {
			return []Resource{{Kind: "good", ID: "g-1"}}, nil
		}},
	}

	events, result := Run(context.Background(), scanners, NewForTest(), RunOptions{Concurrency: 1})
	got := drainEvents(events)
	res := <-result

	if !hasErrorFor(got, "bad", ouch) {
		t.Errorf("expected EventError for kind \"bad\" wrapping ouch; got %v", got)
	}
	if len(res.Resources) != 1 || res.Resources[0].Kind != "good" {
		t.Errorf("Result.Resources = %v, want one \"good\" resource", res.Resources)
	}
	if !errors.Is(res.Errors["bad"], ouch) {
		t.Errorf("Result.Errors[\"bad\"] = %v, want ouch", res.Errors["bad"])
	}
}

// TestRunRespectsConcurrencyCap launches more scanners than the cap and
// counts the maximum number observed running at the same time.
func TestRunRespectsConcurrencyCap(t *testing.T) {
	const fleet = 6
	const cap = 2

	var inflight, peak int64
	scanners := make([]Scanner, fleet)
	for i := 0; i < fleet; i++ {
		i := i
		scanners[i] = stubScanner{
			kind:  fmt.Sprintf("scan/%d", i),
			perms: []string{"x:GetIt"},
			fn: func(_ context.Context, _ *Client, _ ScannerEmitter) ([]Resource, error) {
				now := atomic.AddInt64(&inflight, 1)
				for {
					p := atomic.LoadInt64(&peak)
					if now <= p || atomic.CompareAndSwapInt64(&peak, p, now) {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
				atomic.AddInt64(&inflight, -1)
				return nil, nil
			},
		}
	}

	events, result := Run(context.Background(), scanners, NewForTest(), RunOptions{Concurrency: cap})
	drainEvents(events)
	<-result

	if got := atomic.LoadInt64(&peak); got > cap {
		t.Errorf("peak concurrent scanners = %d, want <= %d", got, cap)
	}
}

// TestRunCancelledBeforeStart cancels the context before Run is invoked
// and asserts every scanner lands in Result.Errors with the
// cancellation reason instead of executing.
func TestRunCancelledBeforeStart(t *testing.T) {
	called := atomic.Int64{}
	scanners := []Scanner{
		stubScanner{kind: "a", perms: []string{"x:GetA"}, fn: func(_ context.Context, _ *Client, _ ScannerEmitter) ([]Resource, error) {
			called.Add(1)
			return nil, nil
		}},
		stubScanner{kind: "b", perms: []string{"x:GetB"}, fn: func(_ context.Context, _ *Client, _ ScannerEmitter) ([]Resource, error) {
			called.Add(1)
			return nil, nil
		}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	events, result := Run(ctx, scanners, NewForTest(), RunOptions{Concurrency: 4})
	drainEvents(events)
	res := <-result

	if called.Load() != 0 {
		t.Errorf("expected zero scanner invocations after cancel, got %d", called.Load())
	}
	if len(res.Errors) != 2 {
		t.Errorf("expected both scanners to land in Errors, got %d entries", len(res.Errors))
	}
}

// countOfType returns how many events have the given type.
func countOfType(events []Event, want EventType) int {
	n := 0
	for _, e := range events {
		if e.Type == want {
			n++
		}
	}
	return n
}

// hasErrorFor reports whether the slice has an EventError for kind
// whose wrapped error is target.
func hasErrorFor(events []Event, kind string, target error) bool {
	for _, e := range events {
		if e.Type == EventError && e.Kind == kind && errors.Is(e.Err, target) {
			return true
		}
	}
	return false
}

// TestRunUnblocksWhenCallerStopsDraining pins down the regression
// guarded by sendOrDrop: a caller that cancels ctx and stops reading
// events must not leak the pool goroutine. The pool has a buffered
// events channel (capacity 64); we ship enough scanners with
// Progress-heavy Scans to overflow it, then cancel without draining
// and assert the Result still arrives within a tight deadline.
func TestRunUnblocksWhenCallerStopsDraining(t *testing.T) {
	const fleet = 32
	scanners := make([]Scanner, fleet)
	for i := 0; i < fleet; i++ {
		scanners[i] = stubScanner{
			kind:  fmt.Sprintf("scan/%d", i),
			perms: []string{"x:GetIt"},
			fn: func(ctx context.Context, _ *Client, e ScannerEmitter) ([]Resource, error) {
				// Pump enough Progress events to overflow the channel
				// buffer if the caller stops draining.
				for j := 0; j < 100; j++ {
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					default:
					}
					e.Progress(j)
				}
				return nil, nil
			},
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	_, result := Run(ctx, scanners, NewForTest(), RunOptions{Concurrency: 8})

	// Intentionally do NOT drain events. Cancel and wait for Result.
	cancel()
	select {
	case <-result:
	case <-time.After(2 * time.Second):
		t.Fatal("Run goroutine leaked: result never arrived after cancel")
	}
}

// TestRunReturnsKindsInSortedOrder pins down a small contract surface:
// while the *order* of completion is non-deterministic with parallel
// scanners, the Registry.Kinds() helper sorts lexically so the audit
// summary is stable. This is a registry test parked here because the
// pool is what consumes the slice in production.
func TestRunReturnsKindsInSortedOrder(t *testing.T) {
	r := NewRegistry()
	for _, k := range []string{"c/x", "a/x", "b/x"} {
		if err := r.Register(stubScanner{kind: k, perms: []string{"x:GetIt"}, fn: nil}); err != nil {
			t.Fatalf("Register %q: %v", k, err)
		}
	}
	got := r.Kinds()
	want := []string{"a/x", "b/x", "c/x"}
	if !sort.StringsAreSorted(got) || len(got) != len(want) {
		t.Fatalf("Kinds() = %v, want sorted %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("Kinds()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
