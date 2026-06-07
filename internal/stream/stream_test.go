package stream

import (
	"sync"
	"testing"
	"time"
)

func TestEvent_KindLabels(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		ev   Event
		want string
	}{
		{"LogLine", LogLine{}, "log_line"},
		{"CFNStackEvent", CFNStackEvent{}, "cfn_stack_event"},
		{"ShellExited", ShellExited{}, "shell_exited"},
		{"ProgressTick", ProgressTick{}, "progress_tick"},
		{"CancellingStarted", CancellingStarted{}, "cancelling_started"},
		{"CancellingDone", CancellingDone{}, "cancelling_done"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ev.EventKind(); got != tc.want {
				t.Fatalf("EventKind() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEventBus_TwoSubscribersReceiveEveryEvent is the headline
// guarantee from the plan: "Two subscribers on the same request id
// both receive every event."
func TestEventBus_TwoSubscribersReceiveEveryEvent(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(8)
	const id = "req-1"
	a := bus.Subscribe(id)
	b := bus.Subscribe(id)

	want := []Event{
		CancellingStarted{StackName: "s", Status: "UPDATE_IN_PROGRESS"},
		ProgressTick{Message: "still working"},
		CFNStackEvent{LogicalID: "Bucket", ResourceStatus: "DELETE_IN_PROGRESS"},
		LogLine{Stream: "stdout", Text: "hello"},
		CancellingDone{StackName: "s", Action: ActionCancelUpdateStack},
	}

	go func() {
		for _, ev := range want {
			bus.Publish(id, ev)
		}
		bus.Close(id)
	}()

	gotA := drain(t, a)
	gotB := drain(t, b)

	assertEventsEqual(t, "subscriber A", gotA, want)
	assertEventsEqual(t, "subscriber B", gotB, want)
}

// TestEventBus_SubscribersIsolatedByRequestID verifies that the
// shard key actually shards: events on one ID are not visible to a
// subscriber of another ID.
func TestEventBus_SubscribersIsolatedByRequestID(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(4)
	a := bus.Subscribe("req-A")
	b := bus.Subscribe("req-B")

	bus.Publish("req-A", LogLine{Text: "for-A"})
	bus.Publish("req-B", LogLine{Text: "for-B"})
	bus.Close("req-A")
	bus.Close("req-B")

	gotA := drain(t, a)
	gotB := drain(t, b)

	if len(gotA) != 1 || gotA[0].(LogLine).Text != "for-A" {
		t.Fatalf("subscriber A received %#v, want one LogLine{Text:\"for-A\"}", gotA)
	}
	if len(gotB) != 1 || gotB[0].(LogLine).Text != "for-B" {
		t.Fatalf("subscriber B received %#v, want one LogLine{Text:\"for-B\"}", gotB)
	}
}

// TestEventBus_CloseClosesAllSubscriberChannels exercises the
// "Closing the bus for an id closes all subscriber channels"
// requirement directly.
func TestEventBus_CloseClosesAllSubscriberChannels(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(1)
	subs := []<-chan Event{
		bus.Subscribe("req"),
		bus.Subscribe("req"),
		bus.Subscribe("req"),
	}

	bus.Close("req")

	for i, ch := range subs {
		select {
		case _, ok := <-ch:
			if ok {
				t.Fatalf("subscriber %d: received a value, want closed channel", i)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: channel not closed within 1s", i)
		}
	}
}

// TestEventBus_CloseIsIdempotent verifies that a second Close on the
// same requestID is a no-op rather than a double-close panic.
func TestEventBus_CloseIsIdempotent(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(1)
	ch := bus.Subscribe("req")

	bus.Close("req")
	bus.Close("req") // must not panic
	bus.Close("never-subscribed")

	// Channel still closed after the extra Close calls.
	if _, ok := <-ch; ok {
		t.Fatal("subscriber channel still open after Close")
	}
}

// TestEventBus_PublishWithoutSubscribersIsNoop sanity checks the
// "drop events when nobody's listening" behaviour and confirms
// Publish does not block when there's no one to receive.
func TestEventBus_PublishWithoutSubscribersIsNoop(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(1)
	done := make(chan struct{})
	go func() {
		bus.Publish("nobody-home", LogLine{Text: "x"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish with no subscribers blocked")
	}
}

// TestEventBus_ConcurrentPublishersDifferentIDs is the
// concurrent-safety smoke test that catches data races under -race.
// Many producers on distinct IDs, each with a dedicated subscriber
// that drains concurrently so producers never back-pressure-block.
func TestEventBus_ConcurrentPublishersDifferentIDs(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(16)
	const (
		producers       = 8
		eventsPerStream = 50
	)

	subs := make([]<-chan Event, producers)
	for i := 0; i < producers; i++ {
		subs[i] = bus.Subscribe(idFor(i))
	}

	counts := make([]int, producers)
	var consumers sync.WaitGroup
	for i, ch := range subs {
		consumers.Add(1)
		go func(i int, ch <-chan Event) {
			defer consumers.Done()
			for range ch {
				counts[i]++
			}
		}(i, ch)
	}

	var producersWG sync.WaitGroup
	for i := 0; i < producers; i++ {
		producersWG.Add(1)
		go func(i int) {
			defer producersWG.Done()
			id := idFor(i)
			for j := 0; j < eventsPerStream; j++ {
				bus.Publish(id, ProgressTick{Message: "tick"})
			}
			bus.Close(id)
		}(i)
	}
	producersWG.Wait()
	consumers.Wait()

	for i, got := range counts {
		if got != eventsPerStream {
			t.Fatalf("subscriber %d received %d events, want %d", i, got, eventsPerStream)
		}
	}
}

// TestEventBus_LateSubscriberMissesPriorEvents documents and
// verifies that the bus does not buffer "for later" subscribers —
// only events published *after* Subscribe returns are delivered.
func TestEventBus_LateSubscriberMissesPriorEvents(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(4)
	early := bus.Subscribe("req")

	bus.Publish("req", LogLine{Text: "first"})

	late := bus.Subscribe("req")
	bus.Publish("req", LogLine{Text: "second"})
	bus.Close("req")

	gotEarly := drain(t, early)
	gotLate := drain(t, late)

	if len(gotEarly) != 2 {
		t.Fatalf("early subscriber received %d events, want 2", len(gotEarly))
	}
	if len(gotLate) != 1 || gotLate[0].(LogLine).Text != "second" {
		t.Fatalf("late subscriber got %#v, want only the second event", gotLate)
	}
}

func TestNewEventBus_NegativeBufferTreatedAsZero(t *testing.T) {
	t.Parallel()

	bus := NewEventBus(-5)
	if bus.bufferSize != 0 {
		t.Fatalf("bufferSize = %d, want 0 for negative input", bus.bufferSize)
	}
}

// --- test helpers --------------------------------------------------

func idFor(i int) string {
	return "req-" + string(rune('A'+i))
}

func drain(t *testing.T, ch <-chan Event) []Event {
	t.Helper()
	var out []Event
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatalf("timed out draining channel after %d events", len(out))
			return out
		}
	}
}

func assertEventsEqual(t *testing.T, name string, got, want []Event) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d events, want %d\n  got:  %#v\n  want: %#v", name, len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: event %d = %#v, want %#v", name, i, got[i], want[i])
		}
	}
}
