package cfn_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/render/cfn"
)

// fakeEventsAPI returns a fixed list of events, newest-first (matching the
// AWS API contract). It records how many times DescribeStackEvents was
// called so dedup tests can assert the poller invoked it more than once.
type fakeEventsAPI struct {
	events []cfn.StackEvent
	err    error
	calls  int32
}

func (f *fakeEventsAPI) DescribeStackEvents(_ context.Context, _ string) ([]cfn.StackEvent, error) {
	atomic.AddInt32(&f.calls, 1)
	if f.err != nil {
		return nil, f.err
	}
	out := make([]cfn.StackEvent, len(f.events))
	copy(out, f.events)
	return out, nil
}

func TestPoller_EmitsEventsChronologicallyAndDedups(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	api := &fakeEventsAPI{
		events: []cfn.StackEvent{
			// AWS returns newest-first; poller flips to chronological.
			{EventID: "e3", Time: t0.Add(3 * time.Second), ResourceType: "AWS::EC2::SecurityGroup", ResourceStatus: "CREATE_COMPLETE"},
			{EventID: "e2", Time: t0.Add(2 * time.Second), ResourceType: "AWS::CloudFormation::Stack", ResourceStatus: "CREATE_IN_PROGRESS"},
			{EventID: "e1", Time: t0.Add(1 * time.Second), ResourceType: "AWS::CloudFormation::Stack", ResourceStatus: "REVIEW_IN_PROGRESS"},
		},
	}
	p := &cfn.Poller{API: api, Interval: 10 * time.Millisecond}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	var got []string
	for ev := range p.Poll(ctx, "stack") {
		got = append(got, ev.EventID)
	}
	want := []string{"e1", "e2", "e3"}
	if !equalSlice(got, want) {
		t.Errorf("emitted IDs = %v, want %v (chronological, deduped)", got, want)
	}
	if atomic.LoadInt32(&api.calls) < 2 {
		t.Errorf("expected at least 2 API calls to test dedup; got %d", api.calls)
	}
}

func TestPoller_StopsOnTerminalStackStatus(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	api := &fakeEventsAPI{
		events: []cfn.StackEvent{
			{EventID: "done", Time: t0.Add(2 * time.Second), ResourceType: "AWS::CloudFormation::Stack", ResourceStatus: "CREATE_COMPLETE"},
			{EventID: "start", Time: t0.Add(1 * time.Second), ResourceType: "AWS::CloudFormation::Stack", ResourceStatus: "CREATE_IN_PROGRESS"},
		},
	}
	p := &cfn.Poller{API: api, Interval: time.Hour} // long interval so we know the loop exited on terminal status, not timeout

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	count := 0
	for range p.Poll(ctx, "stack") {
		count++
	}
	if count != 2 {
		t.Errorf("expected 2 events before terminal exit, got %d", count)
	}
}

func TestPoller_CancellationClosesChannel(t *testing.T) {
	api := &fakeEventsAPI{} // empty event list
	p := &cfn.Poller{API: api, Interval: 10 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	ch := p.Poll(ctx, "stack")

	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel closed after cancel")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("channel did not close within 500ms of cancel")
	}
}

func TestPoller_NilAPIReturnsClosedChannel(t *testing.T) {
	p := &cfn.Poller{}
	ch := p.Poll(context.Background(), "stack")
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel closed when no API configured")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("channel not closed immediately when API is nil")
	}
}

func TestPoller_ToleratesTransientErrors(t *testing.T) {
	api := &fakeEventsAPI{err: errors.New("throttled")}
	p := &cfn.Poller{API: api, Interval: 10 * time.Millisecond}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// We don't expect any events, but the poller should keep ticking
	// (i.e. it shouldn't crash or exit early). Just drain until the
	// context expires.
	for range p.Poll(ctx, "stack") {
		// no-op
	}
	if atomic.LoadInt32(&api.calls) < 2 {
		t.Errorf("expected poller to retry after error; got %d calls", api.calls)
	}
}
