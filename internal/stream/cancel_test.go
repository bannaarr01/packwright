package stream

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// fakeCFN is the test double for the CFN surface SafeCancel
// depends on. Each method records the names it was called with and
// returns whatever the corresponding fields dictate, so a single
// table-driven test can drive every branch of the decision tree.
type fakeCFN struct {
	status    string
	statusErr error

	cancelErr error
	deleteErr error

	describeCalls []string
	cancelCalls   []string
	deleteCalls   []string
}

func (f *fakeCFN) DescribeStackStatus(_ context.Context, name string) (string, error) {
	f.describeCalls = append(f.describeCalls, name)
	return f.status, f.statusErr
}

func (f *fakeCFN) CancelUpdateStack(_ context.Context, name string) error {
	f.cancelCalls = append(f.cancelCalls, name)
	return f.cancelErr
}

func (f *fakeCFN) DeleteStack(_ context.Context, name string) error {
	f.deleteCalls = append(f.deleteCalls, name)
	return f.deleteErr
}

func TestSafeCancel_CreateInProgress_CallsDeleteStack(t *testing.T) {
	t.Parallel()

	bus, sub := busWithSubscriber(t, "req")
	cfn := &fakeCFN{status: "CREATE_IN_PROGRESS"}

	err := SafeCancel(context.Background(), bus, "req", "stack-A", cfn)
	bus.Close("req")
	if err != nil {
		t.Fatalf("SafeCancel: %v", err)
	}

	assertCalls(t, "DescribeStackStatus", cfn.describeCalls, []string{"stack-A"})
	assertCalls(t, "DeleteStack", cfn.deleteCalls, []string{"stack-A"})
	assertCalls(t, "CancelUpdateStack", cfn.cancelCalls, nil)

	events := drain(t, sub)
	assertCancelEvents(t, events, CancellingStarted{StackName: "stack-A", Status: "CREATE_IN_PROGRESS"}, CancellingDone{StackName: "stack-A", Action: ActionDeleteStack})
}

func TestSafeCancel_UpdateInProgress_CallsCancelUpdateStack(t *testing.T) {
	t.Parallel()

	bus, sub := busWithSubscriber(t, "req")
	cfn := &fakeCFN{status: "UPDATE_IN_PROGRESS"}

	err := SafeCancel(context.Background(), bus, "req", "stack-B", cfn)
	bus.Close("req")
	if err != nil {
		t.Fatalf("SafeCancel: %v", err)
	}

	assertCalls(t, "DescribeStackStatus", cfn.describeCalls, []string{"stack-B"})
	assertCalls(t, "CancelUpdateStack", cfn.cancelCalls, []string{"stack-B"})
	assertCalls(t, "DeleteStack", cfn.deleteCalls, nil)

	events := drain(t, sub)
	assertCancelEvents(t, events, CancellingStarted{StackName: "stack-B", Status: "UPDATE_IN_PROGRESS"}, CancellingDone{StackName: "stack-B", Action: ActionCancelUpdateStack})
}

func TestSafeCancel_TerminalStatuses_AreNoop(t *testing.T) {
	t.Parallel()

	terminal := []string{
		"CREATE_COMPLETE",
		"CREATE_FAILED",
		"ROLLBACK_COMPLETE",
		"ROLLBACK_IN_PROGRESS",
		"DELETE_IN_PROGRESS",
		"DELETE_COMPLETE",
		"UPDATE_COMPLETE",
		"UPDATE_ROLLBACK_IN_PROGRESS",
		"REVIEW_IN_PROGRESS",
	}
	for _, status := range terminal {
		status := status
		t.Run(status, func(t *testing.T) {
			t.Parallel()

			bus, sub := busWithSubscriber(t, "req")
			cfn := &fakeCFN{status: status}

			err := SafeCancel(context.Background(), bus, "req", "stack", cfn)
			bus.Close("req")
			if err != nil {
				t.Fatalf("SafeCancel: %v", err)
			}

			assertCalls(t, "CancelUpdateStack", cfn.cancelCalls, nil)
			assertCalls(t, "DeleteStack", cfn.deleteCalls, nil)

			events := drain(t, sub)
			assertCancelEvents(t, events, CancellingStarted{StackName: "stack", Status: status}, CancellingDone{StackName: "stack", Action: ActionNoop})
		})
	}
}

func TestSafeCancel_StackNotFound_IsNoop(t *testing.T) {
	t.Parallel()

	bus, sub := busWithSubscriber(t, "req")
	cfn := &fakeCFN{statusErr: fmt.Errorf("ValidationError: %w", ErrStackNotFound)}

	err := SafeCancel(context.Background(), bus, "req", "missing", cfn)
	bus.Close("req")
	if err != nil {
		t.Fatalf("SafeCancel: %v, want nil for missing stack", err)
	}

	assertCalls(t, "CancelUpdateStack", cfn.cancelCalls, nil)
	assertCalls(t, "DeleteStack", cfn.deleteCalls, nil)

	events := drain(t, sub)
	assertCancelEvents(t, events, CancellingStarted{StackName: "missing"}, CancellingDone{StackName: "missing", Action: ActionNoop})
}

func TestSafeCancel_DescribeError_PropagatesAndEmits(t *testing.T) {
	t.Parallel()

	boom := errors.New("network is sad")
	bus, sub := busWithSubscriber(t, "req")
	cfn := &fakeCFN{statusErr: boom}

	err := SafeCancel(context.Background(), bus, "req", "stack", cfn)
	bus.Close("req")
	if !errors.Is(err, boom) {
		t.Fatalf("SafeCancel error = %v, want it to wrap %v", err, boom)
	}

	assertCalls(t, "CancelUpdateStack", cfn.cancelCalls, nil)
	assertCalls(t, "DeleteStack", cfn.deleteCalls, nil)

	events := drain(t, sub)
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %#v", len(events), events)
	}
	if _, ok := events[0].(CancellingStarted); !ok {
		t.Fatalf("event[0] = %#v, want CancellingStarted", events[0])
	}
	done, ok := events[1].(CancellingDone)
	if !ok {
		t.Fatalf("event[1] = %#v, want CancellingDone", events[1])
	}
	if done.Action != ActionNoop {
		t.Fatalf("CancellingDone.Action = %q, want %q", done.Action, ActionNoop)
	}
	if !errors.Is(done.Err, boom) {
		t.Fatalf("CancellingDone.Err = %v, want it to wrap %v", done.Err, boom)
	}
}

func TestSafeCancel_DeleteStackError_PropagatesAndEmits(t *testing.T) {
	t.Parallel()

	boom := errors.New("delete unavailable")
	bus, sub := busWithSubscriber(t, "req")
	cfn := &fakeCFN{status: "CREATE_IN_PROGRESS", deleteErr: boom}

	err := SafeCancel(context.Background(), bus, "req", "stack", cfn)
	bus.Close("req")
	if !errors.Is(err, boom) {
		t.Fatalf("SafeCancel error = %v, want it to wrap %v", err, boom)
	}

	assertCalls(t, "DeleteStack", cfn.deleteCalls, []string{"stack"})

	events := drain(t, sub)
	done := mustCancellingDone(t, events)
	if done.Action != ActionDeleteStack {
		t.Fatalf("CancellingDone.Action = %q, want %q", done.Action, ActionDeleteStack)
	}
	if !errors.Is(done.Err, boom) {
		t.Fatalf("CancellingDone.Err = %v, want it to wrap %v", done.Err, boom)
	}
}

func TestSafeCancel_CancelUpdateStackError_PropagatesAndEmits(t *testing.T) {
	t.Parallel()

	boom := errors.New("cancel unavailable")
	bus, sub := busWithSubscriber(t, "req")
	cfn := &fakeCFN{status: "UPDATE_IN_PROGRESS", cancelErr: boom}

	err := SafeCancel(context.Background(), bus, "req", "stack", cfn)
	bus.Close("req")
	if !errors.Is(err, boom) {
		t.Fatalf("SafeCancel error = %v, want it to wrap %v", err, boom)
	}

	assertCalls(t, "CancelUpdateStack", cfn.cancelCalls, []string{"stack"})

	events := drain(t, sub)
	done := mustCancellingDone(t, events)
	if done.Action != ActionCancelUpdateStack {
		t.Fatalf("CancellingDone.Action = %q, want %q", done.Action, ActionCancelUpdateStack)
	}
	if !errors.Is(done.Err, boom) {
		t.Fatalf("CancellingDone.Err = %v, want it to wrap %v", done.Err, boom)
	}
}

// TestSafeCancel_TwoSubscribersBothObserveEvents is the integrated
// proof-of-life for the headline plan requirement, exercised through
// the SafeCancel entry point rather than the bus directly.
func TestSafeCancel_TwoSubscribersBothObserveEvents(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(8)
	a := bus.Subscribe("req")
	b := bus.Subscribe("req")
	cfn := &fakeCFN{status: "UPDATE_IN_PROGRESS"}

	if err := SafeCancel(context.Background(), bus, "req", "stack", cfn); err != nil {
		t.Fatalf("SafeCancel: %v", err)
	}
	bus.Close("req")

	want := []Event{
		CancellingStarted{StackName: "stack", Status: "UPDATE_IN_PROGRESS"},
		CancellingDone{StackName: "stack", Action: ActionCancelUpdateStack},
	}
	assertEventsEqual(t, "subscriber A", drain(t, a), want)
	assertEventsEqual(t, "subscriber B", drain(t, b), want)
}

// --- helpers -------------------------------------------------------

func busWithSubscriber(t *testing.T, id string) (*EventBus, <-chan Event) {
	t.Helper()
	bus := NewEventBus(8)
	return bus, bus.Subscribe(id)
}

func assertCalls(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s calls: got %v, want %v", name, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s calls: got %v, want %v", name, got, want)
		}
	}
}

func assertCancelEvents(t *testing.T, events []Event, started CancellingStarted, done CancellingDone) {
	t.Helper()
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %#v", len(events), events)
	}
	if got := events[0]; got != started {
		t.Fatalf("event[0] = %#v, want %#v", got, started)
	}
	gotDone, ok := events[1].(CancellingDone)
	if !ok {
		t.Fatalf("event[1] = %#v, want CancellingDone", events[1])
	}
	if gotDone.StackName != done.StackName || gotDone.Action != done.Action {
		t.Fatalf("event[1] = %#v, want %#v", gotDone, done)
	}
	if gotDone.Err != nil {
		t.Fatalf("event[1].Err = %v, want nil", gotDone.Err)
	}
}

func mustCancellingDone(t *testing.T, events []Event) CancellingDone {
	t.Helper()
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %#v", len(events), events)
	}
	done, ok := events[1].(CancellingDone)
	if !ok {
		t.Fatalf("event[1] = %#v, want CancellingDone", events[1])
	}
	return done
}
